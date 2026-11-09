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
	config       Config
	registry     *engine.Registry
	registration p.Register
	journal      *journal
	mu           sync.Mutex
	sessions     map[string]engine.Session
	active       map[string]bool
	wake         chan struct{}
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
}

func New(config Config, registry *engine.Registry, engines []p.EngineRegistration) (*Runtime, error) {
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
		j.db.Close()
		return nil, err
	}
	for _, record := range records {
		if record.Execution.NodeID != config.NodeID {
			j.db.Close()
			return nil, fmt.Errorf("execution database belongs to another node")
		}
	}
	if err := j.recover(); err != nil {
		j.db.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	active := map[string]bool{}
	for _, rec := range records {
		if rec.Result == nil && rec.Receipt.Admission != p.Rejected {
			active[rec.CommandID] = true
		}
	}
	return &Runtime{config: config, registry: registry, registration: registration, journal: j, sessions: map[string]engine.Session{}, active: active, wake: make(chan struct{}, 1), ctx: lifetime, cancel: cancel}, nil
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
	if r.closed {
		return engine.ErrClosed
	}
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
			if prior.Execution.SessionID == d.Execution.SessionID && prior.Result == nil && prior.Receipt.Admission != p.Rejected {
				receipt.Admission = p.Rejected
				receipt.Failure = &p.Failure{Code: "session_busy", Message: "session has an unresolved activity"}
				break
			}
		}
	}
	rec := record{CommandID: d.CommandID, Execution: d.Execution, Fingerprint: string(fingerprint), Receipt: receipt}
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
	options := engine.OpenOptions{SessionID: d.Execution.SessionID, Config: d.Config}
	var session engine.Session
	var err error
	if d.Resume != nil {
		session, err = r.registry.Resume(ctx, *d.Resume, options)
	} else {
		session, err = r.registry.Open(ctx, options)
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
	rec, _, err := r.journal.get(d.CommandID)
	if err == nil {
		rec.Receipt.InstanceID = session.Info().InstanceID
		err = r.journal.put(rec, rec.Receipt)
	}
	r.mu.Unlock()
	if err != nil {
		log.Printf("persist execution identity: %v", err)
		return
	}
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.collect(session) }()
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
			if err := event.Validate(); err == nil {
				if err := r.journal.enqueue(event); err != nil {
					log.Printf("persist cleanup: %v", err)
				}
			}
			r.mu.Lock()
			if proof.ConfirmsStop() {
				delete(r.sessions, session.Info().SessionID)
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
		if rec.Execution.SessionID == session.Info().SessionID && rec.Result == nil && rec.Receipt.Admission != p.Rejected {
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
		if !requested[rec.Execution] && (rec.Result != nil || rec.Receipt.Admission == p.Rejected) {
			continue
		}
		observation := p.Observation{Cursor: p.Cursor{Execution: rec.Execution, InstanceID: rec.Receipt.InstanceID, LastSeq: rec.LastSeq}, State: "unknown", Result: rec.Result}
		if rec.Result != nil {
			observation.State = string(rec.Result.State)
			observation.LastSeq = rec.Result.Seq
			observation.InstanceID = rec.Result.InstanceID
		} else if r.active[rec.CommandID] && rec.Started && r.sessions[rec.Execution.SessionID] != nil {
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
func (r *Runtime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	sessions := []engine.Session{}
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	var problems []error
	for _, s := range sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := s.Close(ctx)
		cancel()
		problems = append(problems, err)
	}
	r.wg.Wait()
	return errors.Join(append(problems, r.journal.db.Close())...)
}
