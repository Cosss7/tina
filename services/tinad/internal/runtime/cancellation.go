package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

func (r *Runtime) input(ctx context.Context, input p.Input) error {
	r.mu.Lock()
	session := r.sessions[input.Execution.SessionID]
	config := r.sessionConfigs[input.Execution.SessionID]
	r.mu.Unlock()
	if session == nil || session.Info().InstanceID != input.InstanceID {
		return engine.ErrStaleActivity
	}
	return r.dispatch(ctx, p.Dispatch{CommandID: input.CommandID, Execution: input.Execution, Config: config, Input: input.Text, Resume: session.Info().Handle})
}
func (r *Runtime) cancelUnsubmittedLocked(rec *record, proof *p.StopEvidence) {
	if rec.Result != nil {
		return
	}
	if proof == nil {
		proof = &p.StopEvidence{Mode: p.InterruptNone, ActivityStopped: true, Root: p.ExitNotApplicable, Workload: p.ExitNotApplicable, Detail: "input was not submitted"}
	}
	instance := rec.Receipt.InstanceID
	if instance == "" {
		instance = "admission-" + newID()
	}
	rec.Receipt.InstanceID = instance
	rec.LastSeq++
	rec.Result = &p.Result{Execution: rec.Execution, InstanceID: instance, CommandID: rec.CommandID, Seq: rec.LastSeq, OccurredAt: time.Now().UTC(), State: p.Cancelled, Stop: proof}
	if err := r.journal.put(*rec, rec.Result); err != nil {
		log.Printf("persist unsubmitted cancellation: %v", err)
		return
	}
	delete(r.active, rec.CommandID)
	r.notify()
}
func (r *Runtime) cancelCommand(command p.Cancel) error {
	if command.Execution.NodeID != r.config.NodeID {
		return fmt.Errorf("cancel targets another node")
	}
	fingerprint, err := p.Encode("command", command)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return engine.ErrClosed
	}
	prior, exists, err := r.journal.get(command.CommandID)
	if err != nil {
		return err
	}
	if exists && (prior.Kind != "cancel" || prior.Fingerprint != string(fingerprint)) {
		return engine.ErrConflict
	}
	if exists && (prior.Receipt.Admission == p.Accepted || prior.Receipt.Admission == p.Rejected) {
		err := r.journal.enqueue(prior.Receipt)
		r.notify()
		return err
	}
	if !exists {
		prior = record{Kind: "cancel", CommandID: command.CommandID, Execution: command.Execution, Fingerprint: string(fingerprint), Receipt: p.Receipt{CommandID: command.CommandID, Execution: command.Execution, InstanceID: command.InstanceID, Admission: p.Admitted}}
		if err := r.journal.put(prior, prior.Receipt); err != nil {
			return err
		}
	}
	records, err := r.journal.all()
	if err != nil {
		return err
	}
	for _, target := range records {
		if target.Kind == "cancel" || target.Execution != command.Execution {
			continue
		}
		if command.InstanceID != "" && target.Receipt.InstanceID != "" && command.InstanceID != target.Receipt.InstanceID {
			return engine.ErrStaleActivity
		}
		target.CancelRequested = true
		if err := r.journal.put(target, target.Receipt); err != nil {
			return err
		}
		if target.Result == nil && target.Stage == "admitted" {
			r.cancelUnsubmittedLocked(&target, nil)
		}
		break
	}
	if !r.cancelTasks[command.CommandID] {
		r.cancelTasks[command.CommandID] = true
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() { r.mu.Lock(); delete(r.cancelTasks, command.CommandID); r.mu.Unlock() }()
			r.performCancel(command)
		}()
	}
	r.notify()
	return nil
}
func (r *Runtime) performCancel(command p.Cancel) {
	ctx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var proof *p.StopEvidence
	for {
		r.mu.Lock()
		records, err := r.journal.all()
		var target *record
		if err == nil {
			for _, rec := range records {
				if rec.Kind != "cancel" && rec.Execution == command.Execution {
					copy := rec
					target = &copy
					break
				}
			}
		}
		if target != nil && (target.Result != nil || target.Receipt.Admission == p.Rejected) {
			receipt := p.Receipt{CommandID: command.CommandID, Execution: command.Execution, InstanceID: target.Receipt.InstanceID, Admission: p.Accepted, Stop: proof}
			if receipt.InstanceID == "" {
				receipt.InstanceID = "admission-" + newID()
			}
			rec, _, readErr := r.journal.get(command.CommandID)
			if readErr == nil {
				rec.Receipt = receipt
				err = r.journal.put(rec, receipt)
			}
			r.mu.Unlock()
			if err != nil {
				log.Printf("persist cancel receipt: %v", err)
			}
			r.notify()
			return
		}
		var session engine.Session
		if target != nil {
			session = r.sessions[target.Execution.SessionID]
			if session != nil && target.Receipt.InstanceID != "" && session.Info().InstanceID != target.Receipt.InstanceID {
				session = nil
			}
		}
		r.mu.Unlock()
		if err != nil {
			return
		}
		if session != nil && proof == nil {
			var result p.StopEvidence
			var interruptErr error
			if interrupter, ok := session.(engine.Interrupter); ok && session.Info().Capabilities.Interrupt != p.InterruptNone {
				result, interruptErr = interrupter.Interrupt(ctx, command.Execution.ActivityID)
			} else {
				result, interruptErr = session.Close(ctx)
			}
			if interruptErr == nil {
				proof = &result
			} else if !errors.Is(interruptErr, engine.ErrStaleActivity) {
				proof = &result
			}
		}
		select {
		case <-ctx.Done():
			r.mu.Lock()
			rec, ok, err := r.journal.get(command.CommandID)
			if err == nil && ok {
				rec.Receipt.Admission = p.Unknown
				rec.Receipt.Failure = &p.Failure{Code: "stop_unproven", Message: "target activity or tool workload stop is not proven; execution was not replayed"}
				if proof != nil && proof.Validate() == nil {
					rec.Receipt.Stop = proof
				}
				err = r.journal.put(rec, rec.Receipt)
			}
			r.mu.Unlock()
			if err != nil {
				log.Printf("persist unknown cancel: %v", err)
			}
			r.notify()
			return
		case <-ticker.C:
		}
	}
}
