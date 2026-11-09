// Package process starts engines in an owned OS workload scope. Signals and
// observed root/workload exits are separate; a timeout never proves cleanup.
package process

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
)

type Scope struct {
	Cmd      *exec.Cmd
	done     chan struct{}
	mu       sync.Mutex
	waitErr  error
	platform platformScope
}

func Start(cmd *exec.Cmd) (*Scope, error) {
	s := &Scope{Cmd: cmd, done: make(chan struct{})}
	if err := s.platform.prepare(cmd); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		s.platform.release()
		return nil, err
	}
	if err := s.platform.attach(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		s.platform.release()
		return nil, err
	}
	go func() { err := cmd.Wait(); s.mu.Lock(); s.waitErr = err; s.mu.Unlock(); close(s.done) }()
	return s, nil
}
func (s *Scope) Done() <-chan struct{} { return s.done }
func (s *Scope) WaitError() error      { s.mu.Lock(); defer s.mu.Unlock(); return s.waitErr }
func (s *Scope) Evidence() p.StopEvidence {
	root := p.ExitLive
	select {
	case <-s.done:
		root = p.ExitExited
	default:
	}
	workload := p.ExitUnproven
	if empty, err := s.platform.empty(s.Cmd); err == nil {
		if empty {
			workload = p.ExitExited
		} else {
			workload = p.ExitLive
		}
	}
	return p.StopEvidence{Mode: p.InterruptProcess, ActivityStopped: root == p.ExitExited, Root: root, Workload: workload}
}
func (s *Scope) Stop(ctx context.Context) (p.StopEvidence, error) {
	requestErr := s.platform.terminate(s.Cmd)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		proof := s.Evidence()
		proof.Requested = true
		if proof.ConfirmsStop() {
			s.platform.release()
			return proof, nil
		}
		select {
		case <-ctx.Done():
			proof.Detail = "process cleanup was not confirmed"
			return proof, errors.Join(ctx.Err(), requestErr)
		case <-ticker.C:
		}
	}
}
