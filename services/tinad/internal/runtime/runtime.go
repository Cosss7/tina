// Package runtime owns execution only: admission, sessions and durable facts.
package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

func newID() string { return rand.Text() }

type Config struct {
	NodeID        string                `json:"nodeId"`
	Name          string                `json:"name"`
	URL           string                `json:"url"`
	Token         string                `json:"token"`
	Database      string                `json:"database"`
	MaxConcurrent int                   `json:"maxConcurrent"`
	Roots         []string              `json:"roots"`
	Agents        []p.AgentRegistration `json:"agents"`
}
type Runtime struct {
	config         Config
	registry       *engine.Registry
	registration   p.Register
	journal        *journal
	mu             sync.Mutex
	sessions       map[string]engine.Session
	active         map[string]bool
	wake           chan struct{}
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	cancelTasks    map[string]bool
	sessionConfigs map[string]p.SessionConfig
	reconnect      chan struct{}
	shutdownOnce   sync.Once
	shutdownErr    error
}

func New(config Config, registry *engine.Registry, engines []p.EngineRegistration) (*Runtime, error) {
	config.Roots = append([]string(nil), config.Roots...)
	config.Agents = append([]p.AgentRegistration(nil), config.Agents...)
	registration := p.Register{NodeID: config.NodeID, Name: config.Name, Version: "dev", MaxConcurrent: config.MaxConcurrent, Agents: config.Agents, Engines: engines}
	if err := registration.Validate(); err != nil {
		return nil, err
	}
	if config.Database == "" || len(config.Roots) == 0 {
		return nil, fmt.Errorf("database and local roots are required")
	}
	for i, root := range config.Roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, err
		}
		config.Roots[i] = absolute
	}
	j, err := openJournal(config.Database)
	if err != nil {
		return nil, err
	}
	records, err := j.all()
	if err != nil {
		j.close()
		return nil, err
	}
	for _, record := range records {
		if record.Execution.NodeID != config.NodeID {
			j.close()
			return nil, fmt.Errorf("execution database belongs to another node")
		}
	}
	if err := j.recover(); err != nil {
		j.close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	active := map[string]bool{}
	for _, rec := range records {
		unresolved := rec.Result == nil || (rec.Cleanup != nil && !rec.Cleanup.ConfirmsStop()) || (rec.Cleanup == nil && rec.Result != nil && (rec.Result.Stop == nil || !rec.Result.Stop.ConfirmsStop()))
		if rec.Kind != "cancel" && unresolved && rec.Receipt.Admission != p.Rejected {
			active[rec.CommandID] = true
		}
	}
	return &Runtime{config: config, registry: registry, registration: registration, journal: j, sessions: map[string]engine.Session{}, active: active, wake: make(chan struct{}, 1), reconnect: make(chan struct{}, 1), ctx: lifetime, cancel: cancel, cancelTasks: map[string]bool{}, sessionConfigs: map[string]p.SessionConfig{}}, nil
}
func (r *Runtime) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *Runtime) Handle(ctx context.Context, message p.Message) error {
	if err := message.Validate(); err != nil {
		return err
	}
	switch m := message.(type) {
	case *p.Dispatch:
		return r.dispatch(ctx, *m)
	case *p.Cancel:
		return r.cancelCommand(*m)
	case *p.Input:
		return r.input(ctx, *m)
	case *p.Ack:
		if m.NodeID != r.config.NodeID {
			return fmt.Errorf("ack belongs to another node")
		}
		return r.journal.ack(m.MessageID)
	case *p.ReconcileRequest:
		return r.reconcile(*m)
	default:
		return fmt.Errorf("unsupported runtime command %T", message)
	}
}
func (r *Runtime) allow(d p.Dispatch) error {
	if d.Execution.NodeID != r.config.NodeID {
		return fmt.Errorf("dispatch belongs to another node")
	}
	allowed := false
	for _, agent := range r.config.Agents {
		if agent.ID == d.Execution.AgentID && agent.ProjectID == d.Execution.ProjectID && agent.EngineID == d.Config.EngineID {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("Agent/project/engine binding is not locally authorized")
	}
	absolute, err := filepath.Abs(d.Config.Cwd)
	if err != nil {
		return err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("working directory is unavailable")
	}
	for _, root := range r.config.Roots {
		rel, err := filepath.Rel(root, absolute)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return fmt.Errorf("working directory is outside locally authorized roots")
}
func (r *Runtime) dispatch(ctx context.Context, d p.Dispatch) error {
	fingerprint, err := p.Encode("command", d)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prior, exists, err := r.journal.get(d.CommandID)
	if err != nil {
		return err
	}
	if exists {
		if prior.Fingerprint != string(fingerprint) {
			return fmt.Errorf("command identity conflict")
		}
		err := r.journal.enqueue(prior.Receipt)
		r.notify()
		return err
	}
	receipt := p.Receipt{CommandID: d.CommandID, Execution: d.Execution, Admission: p.Admitted}
	if r.closed {
		receipt.Admission = p.Rejected
		receipt.Failure = &p.Failure{Code: "node_shutdown", Message: "node is shutting down; no input was submitted"}
	}
	if err := r.allow(d); err != nil {
		receipt.Admission = p.Rejected
		receipt.Failure = &p.Failure{Code: "local_policy", Message: err.Error()}
	}
	if receipt.Admission == p.Admitted && len(r.active) >= r.config.MaxConcurrent {
		receipt.Admission = p.Rejected
		receipt.Failure = &p.Failure{Code: "capacity", Message: "local capacity is exhausted"}
	}
	if receipt.Admission == p.Admitted {
		records, err := r.journal.all()
		if err != nil {
			return err
		}
		for _, prior := range records {
			if prior.Kind == "cancel" {
				continue
			}
			if prior.Execution.SessionID == d.Execution.SessionID && d.Resume == nil {
				receipt.Admission = p.Rejected
				receipt.Failure = &p.Failure{Code: "session_exists", Message: "existing logical session requires an explicit native resume handle"}
				break
			}
			if prior.Execution.SessionID == d.Execution.SessionID && prior.Result == nil && prior.Receipt.Admission != p.Rejected {
				receipt.Admission = p.Rejected
				receipt.Failure = &p.Failure{Code: "session_busy", Message: "session has an unresolved activity"}
				break
			}
		}
	}
	rec := record{Kind: "dispatch", Stage: "admitted", CommandID: d.CommandID, Execution: d.Execution, Fingerprint: string(fingerprint), Receipt: receipt}
	// Durably register command identity before engine construction or submission.
	if err := r.journal.put(rec, receipt); err != nil {
		return err
	}
	r.notify()
	if receipt.Admission == p.Rejected {
		return nil
	}
	r.active[d.CommandID] = true
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.execute(r.ctx, d) }()
	return nil
}
func (r *Runtime) execute(ctx context.Context, d p.Dispatch) {
	r.mu.Lock()
	initial, _, readErr := r.journal.get(d.CommandID)
	if readErr != nil || initial.Result != nil {
		r.mu.Unlock()
		return
	}
	if initial.CancelRequested {
		r.cancelUnsubmittedLocked(&initial, nil)
		r.mu.Unlock()
		return
	}
	initial.Stage = "opening"
	if err := r.journal.put(initial, initial.Receipt); err != nil {
		r.mu.Unlock()
		return
	}
	cached := r.sessions[d.Execution.SessionID]
	cachedConfig := r.sessionConfigs[d.Execution.SessionID]
	r.mu.Unlock()
	options := engine.OpenOptions{SessionID: d.Execution.SessionID, Config: d.Config}
	var session engine.Session
	var err error
	reused := false
	if d.Resume != nil && cached != nil && cached.Info().Handle != nil && cached.Info().Handle.NativeID == d.Resume.NativeID && reflect.DeepEqual(cachedConfig, d.Config) {
		session = cached
		reused = true
	} else {
		if cached != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			proof, closeErr := cached.Close(cleanup)
			cancel()
			if closeErr != nil || !proof.ConfirmsStop() {
				r.refuse(d.CommandID, "cleanup_unproven", engine.ErrUncertain)
				return
			}
			r.mu.Lock()
			r.publishReleaseLocked(cached, proof)
			delete(r.sessions, d.Execution.SessionID)
			delete(r.sessionConfigs, d.Execution.SessionID)
			r.mu.Unlock()
		}
		if d.Resume != nil {
			session, err = r.registry.Resume(ctx, *d.Resume, options)
		} else {
			session, err = r.registry.Open(ctx, options)
		}
	}
	if err != nil {
		r.refuse(d.CommandID, "engine_open", err)
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		session.Close(cleanup)
		cancel()
		return
	}
	r.sessions[d.Execution.SessionID] = session
	r.sessionConfigs[d.Execution.SessionID] = d.Config
	rec, _, err := r.journal.get(d.CommandID)
	if err == nil {
		rec.Receipt.InstanceID = session.Info().InstanceID
	}
	if err == nil && rec.CancelRequested {
		r.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		proof, closeErr := session.Close(cleanup)
		cancel()
		r.mu.Lock()
		if closeErr == nil && proof.ConfirmsStop() {
			r.cancelUnsubmittedLocked(&rec, &proof)
			delete(r.sessions, d.Execution.SessionID)
			delete(r.sessionConfigs, d.Execution.SessionID)
		}
		r.mu.Unlock()
		return
	}
	if err == nil {
		rec.Receipt.InstanceID = session.Info().InstanceID
		rec.Stage = "submitting"
		err = r.journal.put(rec, rec.Receipt)
	}
	r.mu.Unlock()
	if err != nil {
		log.Printf("persist execution identity: %v", err)
		return
	}
	if !reused {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.collect(session) }()
	}
	receipt, submitErr := session.Submit(ctx, p.Input{CommandID: d.CommandID, Execution: d.Execution, InstanceID: session.Info().InstanceID, Text: d.Input})
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, _, err = r.journal.get(d.CommandID)
	if err != nil {
		log.Printf("read execution receipt: %v", err)
		return
	}
	if submitErr != nil {
		receipt = p.Receipt{CommandID: d.CommandID, Execution: d.Execution, InstanceID: session.Info().InstanceID, Admission: p.Unknown, Failure: &p.Failure{Code: "submission_unknown", Message: submitErr.Error()}}
		if errors.Is(submitErr, engine.ErrClosed) || errors.Is(submitErr, engine.ErrConflict) || errors.Is(submitErr, engine.ErrUnsupported) {
			receipt.Admission = p.Rejected
		}
	}
	// A fast terminal event may have already settled this command.
	if rec.Result == nil {
		rec.Receipt = receipt
		if err := r.journal.put(rec, receipt); err != nil {
			log.Printf("persist receipt: %v", err)
		}
	}
	r.notify()
}
func (r *Runtime) refuse(commandID, code string, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, exists, err := r.journal.get(commandID)
	if err != nil || !exists {
		log.Printf("read refused command: %v", err)
		return
	}
	rec.Receipt.Admission = p.Rejected
	if errors.Is(cause, engine.ErrUncertain) {
		rec.Receipt.Admission = p.Unknown
	}
	if rec.CancelRequested && rec.Receipt.Admission == p.Rejected {
		r.cancelUnsubmittedLocked(&rec, nil)
		return
	}
	rec.Receipt.Failure = &p.Failure{Code: code, Message: cause.Error()}
	if err := r.journal.put(rec, rec.Receipt); err != nil {
		log.Printf("persist refusal: %v", err)
	}
	if rec.Receipt.Admission == p.Rejected {
		delete(r.active, commandID)
	}
	r.notify()
}
func (r *Runtime) collect(session engine.Session) {
	for update := range session.Updates() {
		message, err := update.Message()
		if err != nil {
			log.Printf("invalid engine update: %v", err)
			continue
		}
		var commandID string
		if update.Event != nil {
			commandID = update.Event.CommandID
		} else {
			commandID = update.Result.CommandID
		}
		r.mu.Lock()
		rec, exists, err := r.journal.get(commandID)
		if err == nil && !exists {
			r.mu.Unlock()
			log.Printf("engine update refers to an unknown command")
			continue
		}
		if err == nil && exists {
			refs, instance, seq := rec.Execution, session.Info().InstanceID, uint64(0)
			if update.Event != nil {
				if update.Event.Execution != refs || update.Event.InstanceID != instance {
					r.mu.Unlock()
					continue
				}
				seq = update.Event.Seq
			} else {
				if update.Result.Execution != refs || update.Result.InstanceID != instance {
					r.mu.Unlock()
					continue
				}
				seq = update.Result.Seq
			}
			if rec.Result != nil || seq <= rec.LastSeq {
				r.mu.Unlock()
				continue
			}
			if update.Event != nil {
				rec.LastSeq = seq
				if update.Event.Kind == p.ActivityStarted {
					rec.Started = true
				}
			} else {
				rec.LastSeq = seq
				rec.Result = update.Result
			}
			err = r.journal.put(rec, message)
		}
		r.mu.Unlock()
		if err != nil {
			log.Printf("persist engine fact: %v", err)
			continue
		}
		r.notify()
		if update.Result != nil {
			if update.Result.State == p.Cancelled && update.Result.Stop != nil && update.Result.Stop.SessionUsable {
				r.mu.Lock()
				keep := len(r.sessions) <= r.config.MaxConcurrent
				if keep {
					delete(r.active, commandID)
				}
				r.mu.Unlock()
				if keep {
					r.notify()
					continue
				}
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			proof, closeErr := session.Close(closeCtx)
			cancel()
			if closeErr != nil {
				proof.Detail = closeErr.Error()
				if proof.Mode == "" {
					proof.Mode = p.InterruptProcess
					proof.Root = p.ExitUnproven
					proof.Workload = p.ExitUnproven
				}
			}
			event := p.Event{Execution: update.Result.Execution, InstanceID: update.Result.InstanceID, CommandID: commandID, Seq: update.Result.Seq + 1, OccurredAt: time.Now().UTC(), Kind: p.StopReported, Stop: &proof}
			r.mu.Lock()
			if latest, ok, err := r.journal.get(commandID); err == nil && ok {
				event.Seq = latest.LastSeq + 1
				latest.LastSeq = event.Seq
				latest.Cleanup = &proof
				if err := r.journal.put(latest, event); err != nil {
					log.Printf("persist cleanup: %v", err)
				}
			}
			if proof.ConfirmsStop() {
				if current := r.sessions[session.Info().SessionID]; current != nil && current.Info().InstanceID == session.Info().InstanceID {
					delete(r.sessions, session.Info().SessionID)
					delete(r.sessionConfigs, session.Info().SessionID)
				}
				delete(r.active, commandID)
			}
			r.mu.Unlock()
			r.notify()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.journal.all()
	if err != nil {
		return
	}
	for _, rec := range records {
		if rec.Kind != "cancel" && rec.Execution.SessionID == session.Info().SessionID && rec.Receipt.InstanceID == session.Info().InstanceID && rec.Result == nil && rec.Receipt.Admission != p.Rejected {
			rec.Receipt.Admission = p.Unknown
			rec.Receipt.Failure = &p.Failure{Code: "stream_closed", Message: "engine stream closed without a proven terminal result"}
			if err := r.journal.put(rec, rec.Receipt); err != nil {
				log.Printf("persist unknown execution: %v", err)
			}
		}
	}
	r.notify()
}
func (r *Runtime) reconcile(request p.ReconcileRequest) error {
	if request.NodeID != r.config.NodeID {
		return fmt.Errorf("reconciliation belongs to another node")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	records, err := r.journal.all()
	if err != nil {
		return err
	}
	response := p.ReconcileResponse{NodeID: r.config.NodeID, Observations: []p.Observation{}}
	seen := map[p.Execution]bool{}
	requested := map[p.Execution]bool{}
	for _, cursor := range request.Known {
		requested[cursor.Execution] = true
	}
	for _, rec := range records {
		if rec.Kind == "cancel" {
			continue
		}
		if rec.Result != nil && (rec.Cleanup == nil || !rec.Cleanup.ConfirmsStop()) && (rec.Result.Stop == nil || !rec.Result.Stop.ConfirmsStop()) {
			if session := r.sessions[rec.Execution.SessionID]; session != nil && session.Info().InstanceID == rec.Receipt.InstanceID {
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				proof, closeErr := session.Close(cleanup)
				cancel()
				if closeErr != nil {
					proof.Detail = closeErr.Error()
				}
				if proof.Mode == "" {
					proof.Mode = p.InterruptProcess
					proof.Root = p.ExitUnproven
					proof.Workload = p.ExitUnproven
				}
				rec.LastSeq++
				rec.Cleanup = &proof
				event := p.Event{Execution: rec.Execution, InstanceID: rec.Receipt.InstanceID, CommandID: rec.CommandID, Seq: rec.LastSeq, OccurredAt: time.Now().UTC(), Kind: p.StopReported, Stop: &proof}
				if err := r.journal.put(rec, event); err != nil {
					return err
				}
				if proof.ConfirmsStop() {
					delete(r.sessions, rec.Execution.SessionID)
					delete(r.sessionConfigs, rec.Execution.SessionID)
					delete(r.active, rec.CommandID)
				}
			}
		}
		if !requested[rec.Execution] && (rec.Result != nil || rec.Receipt.Admission == p.Rejected) {
			continue
		}
		observation := p.Observation{Cursor: p.Cursor{Execution: rec.Execution, InstanceID: rec.Receipt.InstanceID, LastSeq: rec.LastSeq}, State: "unknown", Result: rec.Result}
		if rec.Result != nil {
			observation.State = string(rec.Result.State)
			observation.LastSeq = rec.Result.Seq
			observation.InstanceID = rec.Result.InstanceID
		} else if r.active[rec.CommandID] && rec.Started && r.sessions[rec.Execution.SessionID] != nil && !r.sessions[rec.Execution.SessionID].Info().Uncertain {
			observation.State = "running"
		}
		response.Observations = append(response.Observations, observation)
		seen[rec.Execution] = true
	}
	for _, cursor := range request.Known {
		if !seen[cursor.Execution] {
			response.Observations = append(response.Observations, p.Observation{Cursor: cursor, State: "unknown"})
		}
	}
	err = r.journal.enqueue(response)
	r.notify()
	return err
}
func (r *Runtime) quiesce() error {
	r.mu.Lock()
	r.closed = true
	r.registration.Agents = []p.AgentRegistration{}
	select {
	case r.reconnect <- struct{}{}:
	default:
	}
	r.cancel()
	sessions := []engine.Session{}
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	var problems []error
	type released struct {
		session engine.Session
		proof   p.StopEvidence
	}
	proofs := []released{}
	for _, s := range sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		proof, err := s.Close(ctx)
		proofs = append(proofs, released{s, proof})
		cancel()
		problems = append(problems, err)
	}
	r.wg.Wait()
	r.mu.Lock()
	for _, entry := range proofs {
		if entry.proof.Validate() == nil {
			r.publishReleaseLocked(entry.session, entry.proof)
		}
	}
	r.mu.Unlock()
	return errors.Join(problems...)
}
func (r *Runtime) Quiesce() error {
	r.shutdownOnce.Do(func() { r.shutdownErr = r.quiesce() })
	return r.shutdownErr
}
func (r *Runtime) Close() error { return errors.Join(r.Quiesce(), r.journal.close()) }
func (r *Runtime) Flush(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		frames, err := r.journal.pending()
		if err != nil {
			return err
		}
		if len(frames) == 0 {
			return nil
		}
		r.notify()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runtime) UpdateAuthorization(roots []string, agents []p.AgentRegistration) error {
	canonical := make([]string, len(roots))
	if len(roots) == 0 {
		return fmt.Errorf("at least one local root is required")
	}
	for i, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("local root is not a directory")
		}
		canonical[i] = absolute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return engine.ErrClosed
	}
	registration := r.registration
	registration.Agents = append([]p.AgentRegistration(nil), agents...)
	if err := registration.Validate(); err != nil {
		return err
	}
	if reflect.DeepEqual(r.config.Roots, canonical) && reflect.DeepEqual(r.config.Agents, agents) {
		return nil
	}
	r.config.Roots = canonical
	r.config.Agents = append([]p.AgentRegistration(nil), agents...)
	r.registration = registration
	select {
	case r.reconnect <- struct{}{}:
	default:
	}
	return nil
}

// Caller holds mu. Releasing an idle native runtime gets its own fact and cannot
// rewrite the original result or steal a sequence from a later active turn.
func (r *Runtime) publishReleaseLocked(session engine.Session, proof p.StopEvidence) {
	records, err := r.journal.all()
	if err != nil {
		return
	}
	var latest *record
	var maxSeq uint64
	for _, rec := range records {
		if rec.Kind == "cancel" || rec.Receipt.InstanceID != session.Info().InstanceID {
			continue
		}
		if rec.Result == nil && rec.Receipt.Admission != p.Rejected {
			return
		}
		if rec.LastSeq > maxSeq {
			maxSeq = rec.LastSeq
			copy := rec
			latest = &copy
		}
	}
	if latest == nil || (latest.Cleanup != nil && latest.Cleanup.ConfirmsStop() && !latest.Cleanup.SessionUsable) {
		return
	}
	latest.LastSeq = maxSeq + 1
	latest.Cleanup = &proof
	event := p.Event{Execution: latest.Execution, CommandID: latest.CommandID, InstanceID: session.Info().InstanceID, Seq: latest.LastSeq, OccurredAt: time.Now().UTC(), Kind: p.StopReported, Stop: &proof}
	if err := r.journal.put(*latest, event); err != nil {
		log.Printf("persist runtime release: %v", err)
	}
	r.notify()
}
