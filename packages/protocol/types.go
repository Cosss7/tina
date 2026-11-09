// Package protocol defines the versioned control-plane/tinad execution contract.
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

const Version = 1

type InterruptMode string

const (
	InterruptNone    InterruptMode = "none"
	InterruptTurn    InterruptMode = "turn"
	InterruptProcess InterruptMode = "process"
)

type Capabilities struct {
	Resume    bool          `json:"resume"`
	Interrupt InterruptMode `json:"interrupt"`
	Respond   bool          `json:"respond"`
}

func (c Capabilities) Validate() error {
	if c.Interrupt != InterruptNone && c.Interrupt != InterruptTurn && c.Interrupt != InterruptProcess {
		return fmt.Errorf("invalid interrupt mode %q", c.Interrupt)
	}
	return nil
}

// Narrows forbids a live session from advertising more than its driver declared.
func (c Capabilities) Narrows(parent Capabilities) bool {
	return (!c.Resume || parent.Resume) && (!c.Respond || parent.Respond) &&
		(c.Interrupt == InterruptNone || c.Interrupt == parent.Interrupt)
}

type Execution struct {
	ProjectID  string `json:"projectId"`
	RunID      string `json:"runId"`
	AgentID    string `json:"agentId"`
	NodeID     string `json:"nodeId"`
	SessionID  string `json:"sessionId"`
	ActivityID string `json:"activityId"`
}

func (e Execution) Validate() error {
	return required("projectId", e.ProjectID, "runId", e.RunID, "agentId", e.AgentID,
		"nodeId", e.NodeID, "sessionId", e.SessionID, "activityId", e.ActivityID)
}

type SessionHandle struct {
	EngineID  string            `json:"engineId"`
	SessionID string            `json:"sessionId"`
	NativeID  string            `json:"nativeId"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (h SessionHandle) Validate() error {
	return required("engineId", h.EngineID, "sessionId", h.SessionID, "nativeId", h.NativeID)
}

type SessionConfig struct {
	EngineID     string                     `json:"engineId"`
	Cwd          string                     `json:"cwd"`
	Model        string                     `json:"model,omitempty"`
	SystemPrompt string                     `json:"systemPrompt,omitempty"`
	Options      map[string]json.RawMessage `json:"options,omitempty"`
}

func (c SessionConfig) Validate() error {
	if err := required("engineId", c.EngineID, "cwd", c.Cwd); err != nil {
		return err
	}
	for key, value := range c.Options {
		if strings.TrimSpace(key) == "" || !json.Valid(value) {
			return fmt.Errorf("invalid engine option %q", key)
		}
	}
	return nil
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Failure) Validate() error { return required("code", f.Code, "message", f.Message) }

type Admission string

const (
	Admitted Admission = "admitted"
	Accepted Admission = "accepted"
	Rejected Admission = "rejected"
	Unknown  Admission = "unknown"
)

type ExitStatus string

const (
	ExitLive          ExitStatus = "live"
	ExitUnproven      ExitStatus = "unproven"
	ExitExited        ExitStatus = "exited"
	ExitNotApplicable ExitStatus = "not_applicable"
)

type StopEvidence struct {
	Mode            InterruptMode `json:"mode"`
	Requested       bool          `json:"requested"`
	ActivityStopped bool          `json:"activityStopped"`
	SessionUsable   bool          `json:"sessionUsable"`
	Root            ExitStatus    `json:"root"`
	Workload        ExitStatus    `json:"workload"`
	Detail          string        `json:"detail,omitempty"`
}

func (s StopEvidence) Validate() error {
	if err := (Capabilities{Interrupt: s.Mode}).Validate(); err != nil {
		return err
	}
	for _, status := range []ExitStatus{s.Root, s.Workload} {
		if status != ExitLive && status != ExitUnproven && status != ExitExited && status != ExitNotApplicable {
			return fmt.Errorf("invalid exit status %q", status)
		}
	}
	if s.Mode == InterruptProcess && s.SessionUsable {
		return fmt.Errorf("process termination cannot preserve a live session")
	}
	return nil
}

// ConfirmsStop concerns the target activity and its workload, not just a signal.
func (s StopEvidence) ConfirmsStop() bool {
	settled := func(status ExitStatus) bool { return status == ExitExited || status == ExitNotApplicable }
	return s.Validate() == nil && s.ActivityStopped && settled(s.Workload) &&
		(s.Mode != InterruptProcess || settled(s.Root))
}

type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 {
		return fmt.Errorf("token usage cannot be negative")
	}
	return nil
}

type TerminalState string

const (
	Completed TerminalState = "completed"
	Failed    TerminalState = "failed"
	Cancelled TerminalState = "cancelled"
)

func (s TerminalState) valid() bool { return s == Completed || s == Failed || s == Cancelled }

func required(pairs ...string) error {
	for i := 0; i < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			return fmt.Errorf("%s is required", pairs[i])
		}
	}
	return nil
}
