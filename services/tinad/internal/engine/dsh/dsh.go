// Package dsh drives the native newline-delimited stdio JSON-RPC protocol.
package dsh

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
	"github.com/Cosss7/tina/services/tinad/internal/process"
)

type Options struct {
	Command  []string `json:"command"`
	Profile  string   `json:"profile"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Home     string   `json:"home"`
}
type Driver struct{ Options Options }

var caps = p.Capabilities{Resume: true, Interrupt: p.InterruptProcess}

func (*Driver) Definition() engine.Definition {
	return engine.Definition{ID: "dsh", Capabilities: caps}
}
func (d *Driver) Probe(ctx context.Context, _ p.SessionConfig) (engine.Probe, error) {
	if err := ctx.Err(); err != nil {
		return engine.Probe{}, err
	}
	if len(d.Options.Command) == 0 {
		return engine.Probe{Diagnostic: "dsh command is not configured", Capabilities: caps}, nil
	}
	_, err := exec.LookPath(d.Options.Command[0])
	return engine.Probe{Available: err == nil, Version: "stdio-v1", Diagnostic: fmt.Sprint(err), Capabilities: caps}, nil
}
func (d *Driver) Create(ctx context.Context, o engine.OpenOptions) (engine.Session, error) {
	return d.open(ctx, o, nil)
}
func (d *Driver) Resume(ctx context.Context, h p.SessionHandle, o engine.OpenOptions) (engine.Session, error) {
	return d.open(ctx, o, &h)
}
func (d *Driver) open(ctx context.Context, o engine.OpenOptions, resume *p.SessionHandle) (engine.Session, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if len(d.Options.Command) == 0 {
		return nil, engine.ErrUnavailable
	}
	args := append([]string(nil), d.Options.Command[1:]...)
	if d.Options.Profile != "" {
		args = append(args, "--profile", d.Options.Profile)
	}
	cmd := exec.Command(d.Options.Command[0], args...)
	cmd.Dir = o.Config.Cwd
	cmd.Env = os.Environ()
	if d.Options.Home != "" {
		cmd.Env = append(cmd.Env, "DSH_HOME="+d.Options.Home)
	}
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdin.Close()
		return nil, err
	}
	stderr, stderrWrite, err := os.Pipe()
	if err != nil {
		stdinRead.Close()
		stdin.Close()
		stdout.Close()
		stdoutWrite.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinRead, stdoutWrite, stderrWrite
	scope, err := process.Start(cmd)
	stdinRead.Close()
	stdoutWrite.Close()
	stderrWrite.Close()
	if err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("start dsh %q: %w", d.Options.Command[0], err)
	}
	s := &Session{scope: scope, stdin: stdin, updates: make(chan engine.Update, 256), nativeID: "tina-" + rand.Text(), info: engine.SessionInfo{SessionID: o.SessionID, InstanceID: rand.Text(), Capabilities: p.Capabilities{Interrupt: p.InterruptProcess}}, commands: map[string]p.Receipt{}}
	s.rpc = newRPC(stdin, stdout, stderr, s.onEvent)
	cleanup := func(cause error) (engine.Session, error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, closeErr := s.Close(cleanupCtx)
		if closeErr != nil {
			return nil, errors.Join(engine.ErrUncertain, cause, closeErr)
		}
		return nil, errors.Join(cause, closeErr)
	}
	model := o.Config.Model
	if model == "" {
		model = d.Options.Model
	}
	if model == "" {
		model = "deepseek-flash"
	}
	provider := d.Options.Provider
	if provider == "" {
		provider = "deepseek-official"
	}
	init := map[string]any{"cwd": o.Config.Cwd, "provider": provider, "model": model, "reasoningEffort": "off", "maxTokens": 8192}
	for _, key := range []string{"reasoningEffort", "maxTokens"} {
		if raw, ok := o.Config.Options[key]; ok {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return cleanup(err)
			}
			init[key] = value
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.rpc.call(callCtx, "initialize", init, nil); err != nil {
		return cleanup(fmt.Errorf("initialize dsh: %w", err))
	}
	if resume != nil {
		if resume.EngineID != "dsh" || resume.SessionID != o.SessionID {
			return cleanup(engine.ErrResumeRejected)
		}
		if resume.Metadata["home"] != d.Options.Home {
			return cleanup(fmt.Errorf("%w: persistence home changed", engine.ErrResumeRejected))
		}
		s.nativeID = resume.NativeID
		if err := s.rpc.call(callCtx, "session/resume", map[string]any{"sessionId": resume.NativeID}, nil); err != nil {
			return cleanup(fmt.Errorf("%w: %v", engine.ErrResumeRejected, err))
		}
		s.setHandle(d.Options.Home)
	}
	s.home = d.Options.Home
	go s.watch()
	return s, nil
}

type Session struct {
	mu             sync.Mutex
	info           engine.SessionInfo
	nativeID, home string
	scope          *process.Scope
	rpc            *rpc
	updates        chan engine.Update
	active         *p.Input
	output         string
	seq            uint64
	closed         bool
	stopping       bool
	stdin          *os.File
	commands       map[string]p.Receipt
}

func (s *Session) Info() engine.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.info
	if info.Handle != nil {
		h := *info.Handle
		h.Metadata = map[string]string{"home": s.home}
		info.Handle = &h
	}
	return info
}
func (s *Session) setHandle(home string) {
	s.info.Handle = &p.SessionHandle{EngineID: "dsh", SessionID: s.info.SessionID, NativeID: s.nativeID, Metadata: map[string]string{"home": home}}
	s.info.Capabilities.Resume = true
}
func (s *Session) Updates() <-chan engine.Update { return s.updates }
func (s *Session) Submit(ctx context.Context, in p.Input) (p.Receipt, error) {
	if err := in.Validate(); err != nil {
		return p.Receipt{}, err
	}
	s.mu.Lock()
	if s.closed || s.stopping {
		s.mu.Unlock()
		return p.Receipt{}, engine.ErrClosed
	}
	if in.Execution.SessionID != s.info.SessionID || in.InstanceID != s.info.InstanceID {
		s.mu.Unlock()
		return p.Receipt{}, engine.ErrConflict
	}
	if prior, ok := s.commands[in.CommandID]; ok {
		s.mu.Unlock()
		return prior, nil
	}
	if s.active != nil {
		s.mu.Unlock()
		return p.Receipt{}, engine.ErrConflict
	}
	s.active = &in
	s.output = ""
	s.mu.Unlock()
	var reply struct {
		MessageID string `json:"messageId"`
	}
	err := s.rpc.call(ctx, "session/prompt", map[string]any{"sessionId": s.nativeID, "contentBlocks": []map[string]string{{"type": "text", "text": in.Text}}}, &reply)
	receipt := p.Receipt{CommandID: in.CommandID, Execution: in.Execution, InstanceID: in.InstanceID, Admission: p.Unknown}
	if err != nil || reply.MessageID == "" {
		receipt.Failure = &p.Failure{Code: "native_acceptance_unknown", Message: "native prompt acceptance requires reconciliation"}
	} else {
		receipt.Admission = p.Accepted
	}
	s.mu.Lock()
	s.commands[in.CommandID] = receipt
	s.mu.Unlock()
	return receipt, nil
}
func (s *Session) event(kind p.EventKind) *p.Event {
	s.seq++
	return &p.Event{Execution: s.active.Execution, InstanceID: s.info.InstanceID, CommandID: s.active.CommandID, Seq: s.seq, OccurredAt: time.Now().UTC(), Kind: kind}
}
func (s *Session) finish(state p.TerminalState, failure *p.Failure, stop *p.StopEvidence) {
	if s.active == nil {
		return
	}
	s.seq++
	s.updates <- engine.Update{Result: &p.Result{Execution: s.active.Execution, InstanceID: s.info.InstanceID, CommandID: s.active.CommandID, Seq: s.seq, OccurredAt: time.Now().UTC(), State: state, Output: s.output, Failure: failure, Stop: stop, Handle: s.info.Handle}}
	s.active = nil
}
func (s *Session) onEvent(params json.RawMessage) {
	var notification struct {
		SessionID string `json:"sessionId"`
		Event     struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"event"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.active == nil || notification.SessionID != s.nativeID {
		return
	}
	var data struct {
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			ToolCallID string `json:"toolCallId"`
			IsError    bool   `json:"isError"`
		} `json:"message"`
		CallID    string          `json:"callId"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(notification.Event.Data, &data) != nil {
		return
	}
	text := strings.Builder{}
	for _, block := range data.Message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	switch notification.Event.Type {
	case "turn/start":
		s.setHandle(s.home)
		s.updates <- engine.Update{Event: s.event(p.ActivityStarted)}
	case "assistant/message":
		if text.Len() > 0 {
			s.output = text.String()
			event := s.event(p.MessageText)
			event.Text = s.output
			s.updates <- engine.Update{Event: event}
		}
	case "tool/call":
		if data.CallID != "" && data.Name != "" {
			event := s.event(p.ToolCall)
			event.Tool = &p.Tool{CallID: data.CallID, Name: data.Name, Input: data.Arguments}
			s.updates <- engine.Update{Event: event}
		}
	case "tool/result":
		if data.Message.ToolCallID != "" {
			event := s.event(p.ToolResult)
			event.Tool = &p.Tool{CallID: data.Message.ToolCallID, Output: text.String(), IsError: data.Message.IsError}
			s.updates <- engine.Update{Event: event}
		}
	case "turn/end":
		if data.Reason.Kind == "completed" {
			s.finish(p.Completed, nil, nil)
		} else if !s.stopping {
			s.finish(p.Failed, &p.Failure{Code: "native_" + data.Reason.Kind, Message: "dsh turn ended with reason " + data.Reason.Kind}, nil)
		}
	}
}
func (s *Session) watch() {
	<-s.scope.Done()
	// Drain the native pipe before deciding that a terminal event was missing.
	<-s.rpc.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping {
		return
	}
	if s.active != nil {
		proof := s.scope.Evidence()
		s.finish(p.Failed, &p.Failure{Code: "engine_exited", Message: "dsh exited before a native terminal result"}, &proof)
	}
}
func (s *Session) Interrupt(ctx context.Context, activityID string) (p.StopEvidence, error) {
	s.mu.Lock()
	if s.active == nil || s.active.Execution.ActivityID != activityID {
		s.mu.Unlock()
		return p.StopEvidence{}, engine.ErrStaleActivity
	}
	s.stopping = true
	s.mu.Unlock()
	return s.Close(ctx)
}
func (s *Session) Close(ctx context.Context) (p.StopEvidence, error) {
	// Stop is deliberately process-scoped. No session-cancel claim is made for
	// dsh versions whose public SDK has no activity interrupt method.
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	proof, err := s.scope.Stop(ctx)
	s.stdin.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && proof.ConfirmsStop() {
		if s.active != nil {
			s.finish(p.Cancelled, nil, &proof)
		}
		s.closed = true
		close(s.updates)
	}
	return proof, err
}

var _ engine.Driver = (*Driver)(nil)
var _ engine.Resumer = (*Driver)(nil)
var _ engine.Interrupter = (*Session)(nil)
