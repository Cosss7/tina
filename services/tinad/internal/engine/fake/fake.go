// Package fake provides a controlled, in-memory engine for offline contract tests.
package fake

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

var capabilities = protocol.Capabilities{Resume: true, Interrupt: protocol.InterruptTurn}

type history struct {
	sessionID string
	cwd       string
	entries   []string
}

// History survives Session.Close, but deliberately not the lifetime of Driver.
type Driver struct {
	mu        sync.Mutex
	identity  string
	nextID    uint64
	histories map[string]*history
	live      map[string]bool
}

func New() *Driver {
	return &Driver{identity: rand.Text(), histories: make(map[string]*history), live: make(map[string]bool)}
}

func (*Driver) Definition() engine.Definition {
	return engine.Definition{ID: "fake", Capabilities: capabilities}
}

func (*Driver) Probe(ctx context.Context, _ protocol.SessionConfig) (engine.Probe, error) {
	if err := ctx.Err(); err != nil {
		return engine.Probe{}, err
	}
	return engine.Probe{Available: true, Version: "fake-v1", Capabilities: capabilities}, nil
}

func (d *Driver) Create(ctx context.Context, options engine.OpenOptions) (engine.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if options.Config.EngineID != "fake" {
		return nil, engine.ErrUnavailable
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live[options.SessionID] {
		return nil, engine.ErrConflict
	}
	d.nextID++
	nativeID := fmt.Sprintf("fake-native-%s-%d", d.identity, d.nextID)
	d.histories[nativeID] = &history{sessionID: options.SessionID, cwd: options.Config.Cwd}
	return d.openLocked(options, nativeID), nil
}

func (d *Driver) Resume(ctx context.Context, handle protocol.SessionHandle, options engine.OpenOptions) (engine.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := handle.Validate(); err != nil {
		return nil, err
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	stored := d.histories[handle.NativeID]
	if options.Config.EngineID != "fake" || handle.EngineID != "fake" || handle.SessionID != options.SessionID || stored == nil ||
		stored.sessionID != options.SessionID || stored.cwd != options.Config.Cwd {
		return nil, engine.ErrResumeRejected
	}
	if d.live[options.SessionID] {
		return nil, engine.ErrConflict
	}
	return d.openLocked(options, handle.NativeID), nil
}

func (d *Driver) openLocked(options engine.OpenOptions, nativeID string) *Session {
	d.nextID++
	d.live[options.SessionID] = true
	return &Session{
		driver: d,
		info: engine.SessionInfo{
			SessionID: options.SessionID, InstanceID: fmt.Sprintf("fake-instance-%s-%d", d.identity, d.nextID),
			Capabilities: capabilities,
			Handle:       &protocol.SessionHandle{EngineID: "fake", SessionID: options.SessionID, NativeID: nativeID},
		},
		updates: make(chan engine.Update, 64), commands: make(map[string]command),
		activities: make(map[string]bool),
	}
}

func (d *Driver) History(nativeID string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if stored := d.histories[nativeID]; stored != nil {
		return append([]string(nil), stored.entries...)
	}
	return nil
}

func (d *Driver) record(nativeID, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.histories[nativeID].entries = append(d.histories[nativeID].entries, text)
}

type command struct {
	fingerprint string
	receipt     protocol.Receipt
}

type Session struct {
	mu         sync.Mutex
	driver     *Driver
	info       engine.SessionInfo
	updates    chan engine.Update
	commands   map[string]command
	activities map[string]bool
	active     *protocol.Input
	seq        uint64
	closed     bool
}

func (s *Session) Info() engine.SessionInfo {
	info := s.info
	handle := *info.Handle
	info.Handle = &handle
	return info
}

func (s *Session) Updates() <-chan engine.Update { return s.updates }

func (s *Session) Submit(ctx context.Context, input protocol.Input) (protocol.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Receipt{}, err
	}
	if err := input.Validate(); err != nil {
		return protocol.Receipt{}, err
	}
	if input.Execution.SessionID != s.info.SessionID || input.InstanceID != s.info.InstanceID {
		return protocol.Receipt{}, engine.ErrConflict
	}
	fingerprint, err := json.Marshal(input)
	if err != nil {
		return protocol.Receipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.Receipt{}, engine.ErrClosed
	}
	if prior, exists := s.commands[input.CommandID]; exists {
		if prior.fingerprint != string(fingerprint) {
			return protocol.Receipt{}, engine.ErrConflict
		}
		return prior.receipt, nil
	}
	if s.activities[input.Execution.ActivityID] {
		return protocol.Receipt{}, engine.ErrConflict
	}
	receipt := protocol.Receipt{CommandID: input.CommandID, Execution: input.Execution,
		InstanceID: s.info.InstanceID, Admission: protocol.Accepted}
	if s.active != nil {
		receipt.Admission = protocol.Rejected
		receipt.Failure = &protocol.Failure{Code: "busy", Message: "another activity is running"}
	} else {
		if len(s.updates) == cap(s.updates) {
			return protocol.Receipt{}, fmt.Errorf("fake update stream must be drained")
		}
		s.active = &input
		s.activities[input.Execution.ActivityID] = true
		s.driver.record(s.info.Handle.NativeID, input.Text)
		s.seq++
		s.updates <- engine.Update{Event: &protocol.Event{
			Execution: input.Execution, InstanceID: s.info.InstanceID, CommandID: input.CommandID,
			Seq: s.seq, OccurredAt: time.Now().UTC(), Kind: protocol.ActivityStarted,
		}}
	}
	s.commands[input.CommandID] = command{fingerprint: string(fingerprint), receipt: receipt}
	return receipt, nil
}

// Complete is controlled by the test, never by a model, timer, or background loop.
func (s *Session) Complete(activityID, output string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return engine.ErrClosed
	}
	if s.active == nil || s.active.Execution.ActivityID != activityID {
		return engine.ErrStaleActivity
	}
	if err := s.finishLocked(protocol.Completed, output, nil); err != nil {
		return err
	}
	s.driver.record(s.info.Handle.NativeID, output)
	return nil
}

func (s *Session) Interrupt(ctx context.Context, activityID string) (protocol.StopEvidence, error) {
	if err := ctx.Err(); err != nil {
		return protocol.StopEvidence{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.StopEvidence{}, engine.ErrClosed
	}
	stop := protocol.StopEvidence{Mode: protocol.InterruptTurn, ActivityStopped: true,
		SessionUsable: true, Root: protocol.ExitNotApplicable, Workload: protocol.ExitNotApplicable}
	if s.active == nil || s.active.Execution.ActivityID != activityID {
		if s.activities[activityID] {
			return stop, nil
		}
		return protocol.StopEvidence{}, engine.ErrStaleActivity
	}
	stop.Requested = true
	if err := s.finishLocked(protocol.Cancelled, "", &stop); err != nil {
		return protocol.StopEvidence{}, err
	}
	return stop, nil
}

func (s *Session) Close(ctx context.Context) (protocol.StopEvidence, error) {
	if err := ctx.Err(); err != nil {
		return protocol.StopEvidence{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stop := protocol.StopEvidence{Mode: protocol.InterruptProcess, ActivityStopped: true,
		Root: protocol.ExitNotApplicable, Workload: protocol.ExitNotApplicable}
	if s.closed {
		return stop, nil
	}
	if s.active != nil {
		stop.Requested = true
		if err := s.finishLocked(protocol.Cancelled, "", &stop); err != nil {
			return protocol.StopEvidence{}, err
		}
	}
	s.closed = true
	close(s.updates)
	s.driver.mu.Lock()
	delete(s.driver.live, s.info.SessionID)
	s.driver.mu.Unlock()
	return stop, nil
}

func (s *Session) finishLocked(state protocol.TerminalState, output string, stop *protocol.StopEvidence) error {
	if len(s.updates) == cap(s.updates) {
		return fmt.Errorf("fake update stream must be drained")
	}
	s.seq++
	result := &protocol.Result{
		Execution: s.active.Execution, InstanceID: s.info.InstanceID, CommandID: s.active.CommandID,
		Seq: s.seq, OccurredAt: time.Now().UTC(), State: state, Output: output, Stop: stop,
		Handle: s.Info().Handle,
	}
	s.updates <- engine.Update{Result: result}
	s.active = nil
	return nil
}

var _ engine.Driver = (*Driver)(nil)
var _ engine.Resumer = (*Driver)(nil)
var _ engine.Session = (*Session)(nil)
var _ engine.Interrupter = (*Session)(nil)
