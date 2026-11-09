package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

type Register struct {
	NodeID        string               `json:"nodeId"`
	Name          string               `json:"name"`
	Version       string               `json:"version"`
	MaxConcurrent int                  `json:"maxConcurrent"`
	Engines       []EngineRegistration `json:"engines"`
	Agents        []AgentRegistration  `json:"agents"`
}

type EngineRegistration struct {
	ID           string       `json:"id"`
	Version      string       `json:"version,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

type AgentRegistration struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Role      string `json:"role"`
	EngineID  string `json:"engineId"`
	ConfigRef string `json:"configRef,omitempty"`
}

func (m Register) Validate() error {
	if err := required("nodeId", m.NodeID, "name", m.Name, "version", m.Version); err != nil {
		return err
	}
	if m.MaxConcurrent <= 0 {
		return fmt.Errorf("maxConcurrent must be positive")
	}
	engines := map[string]bool{}
	for _, engine := range m.Engines {
		if err := required("engine.id", engine.ID); err != nil {
			return err
		}
		if engines[engine.ID] {
			return fmt.Errorf("duplicate engine %q", engine.ID)
		}
		if err := engine.Capabilities.Validate(); err != nil {
			return err
		}
		engines[engine.ID] = true
	}
	agents := map[string]bool{}
	for _, agent := range m.Agents {
		if err := required("agent.id", agent.ID, "agent.projectId", agent.ProjectID); err != nil {
			return err
		}
		if agents[agent.ID] || !engines[agent.EngineID] {
			return fmt.Errorf("duplicate agent or unregistered engine for %q", agent.ID)
		}
		if agent.Role != "router" && agent.Role != "worker" {
			return fmt.Errorf("invalid agent role %q", agent.Role)
		}
		agents[agent.ID] = true
	}
	return nil
}

type Heartbeat struct {
	NodeID string `json:"nodeId"`
}

func (m Heartbeat) Validate() error { return required("nodeId", m.NodeID) }

type Dispatch struct {
	CommandID string         `json:"commandId"`
	Execution Execution      `json:"execution"`
	Config    SessionConfig  `json:"config"`
	Input     string         `json:"input"`
	Resume    *SessionHandle `json:"resume,omitempty"`
}

func (m Dispatch) Validate() error {
	if err := required("commandId", m.CommandID, "input", m.Input); err != nil {
		return err
	}
	if err := m.Execution.Validate(); err != nil {
		return err
	}
	if err := m.Config.Validate(); err != nil {
		return err
	}
	if m.Resume != nil {
		if err := m.Resume.Validate(); err != nil {
			return err
		}
		if m.Resume.SessionID != m.Execution.SessionID || m.Resume.EngineID != m.Config.EngineID {
			return fmt.Errorf("resume handle does not match dispatch")
		}
	}
	return nil
}

type Input struct {
	CommandID  string    `json:"commandId"`
	Execution  Execution `json:"execution"`
	InstanceID string    `json:"instanceId"`
	Text       string    `json:"text"`
}

func (m Input) Validate() error {
	if err := required("commandId", m.CommandID, "instanceId", m.InstanceID, "text", m.Text); err != nil {
		return err
	}
	return m.Execution.Validate()
}

type Cancel struct {
	CommandID  string    `json:"commandId"`
	Execution  Execution `json:"execution"`
	InstanceID string    `json:"instanceId,omitempty"`
	Reason     string    `json:"reason"`
}

func (m Cancel) Validate() error {
	if err := required("commandId", m.CommandID, "reason", m.Reason); err != nil {
		return err
	}
	return m.Execution.Validate()
}

type Respond struct {
	CommandID  string          `json:"commandId"`
	Execution  Execution       `json:"execution"`
	InstanceID string          `json:"instanceId"`
	RequestID  string          `json:"requestId"`
	Answer     json.RawMessage `json:"answer"`
}

func (m Respond) Validate() error {
	if err := required("commandId", m.CommandID, "instanceId", m.InstanceID, "requestId", m.RequestID); err != nil {
		return err
	}
	if !json.Valid(m.Answer) {
		return fmt.Errorf("answer must be JSON")
	}
	return m.Execution.Validate()
}

type Receipt struct {
	CommandID  string        `json:"commandId"`
	Execution  Execution     `json:"execution"`
	InstanceID string        `json:"instanceId,omitempty"`
	Admission  Admission     `json:"admission"`
	Failure    *Failure      `json:"failure,omitempty"`
	Stop       *StopEvidence `json:"stop,omitempty"`
}

func (m Receipt) Validate() error {
	if err := required("commandId", m.CommandID); err != nil {
		return err
	}
	if err := m.Execution.Validate(); err != nil {
		return err
	}
	switch m.Admission {
	case Admitted:
	case Accepted:
		if err := required("instanceId", m.InstanceID); err != nil {
			return err
		}
	case Rejected, Unknown:
		if m.Failure == nil {
			return fmt.Errorf("%s requires a reason", m.Admission)
		}
	default:
		return fmt.Errorf("invalid admission %q", m.Admission)
	}
	if m.Failure != nil {
		if err := m.Failure.Validate(); err != nil {
			return err
		}
	}
	if m.Stop != nil {
		return m.Stop.Validate()
	}
	return nil
}

type EventKind string

const (
	SessionOpened      EventKind = "session.opened"
	ActivityStarted    EventKind = "activity.started"
	MessageText        EventKind = "message"
	ToolCall           EventKind = "tool.call"
	ToolResult         EventKind = "tool.result"
	UsageReported      EventKind = "usage"
	InteractionRequest EventKind = "interaction.request"
	StopReported       EventKind = "stop"
)

type Tool struct {
	CallID  string          `json:"callId"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	IsError bool            `json:"isError,omitempty"`
}

type Interaction struct {
	RequestID string `json:"requestId"`
	Kind      string `json:"kind"`
	Prompt    string `json:"prompt"`
}

type Event struct {
	Execution   Execution      `json:"execution"`
	InstanceID  string         `json:"instanceId"`
	CommandID   string         `json:"commandId"`
	Seq         uint64         `json:"seq"`
	OccurredAt  time.Time      `json:"occurredAt"`
	Kind        EventKind      `json:"kind"`
	Text        string         `json:"text,omitempty"`
	Tool        *Tool          `json:"tool,omitempty"`
	Usage       *Usage         `json:"usage,omitempty"`
	Handle      *SessionHandle `json:"handle,omitempty"`
	Interaction *Interaction   `json:"interaction,omitempty"`
	Stop        *StopEvidence  `json:"stop,omitempty"`
}

func (m Event) Validate() error {
	if err := validateFact(m.Execution, m.InstanceID, m.CommandID, m.Seq, m.OccurredAt); err != nil {
		return err
	}
	count := 0
	for _, present := range []bool{m.Text != "", m.Tool != nil, m.Usage != nil, m.Handle != nil, m.Interaction != nil, m.Stop != nil} {
		if present {
			count++
		}
	}
	if m.Kind == ActivityStarted && count == 0 {
		return nil
	}
	if m.Kind == SessionOpened && count == 0 {
		return nil // Engines without durable history need not invent a native ID.
	}
	if count != 1 {
		return fmt.Errorf("event %q requires exactly its own payload", m.Kind)
	}
	switch m.Kind {
	case SessionOpened:
		if m.Handle != nil && m.Handle.SessionID == m.Execution.SessionID {
			return m.Handle.Validate()
		}
	case MessageText:
		if m.Text != "" {
			return nil
		}
	case ToolCall, ToolResult:
		if m.Tool != nil {
			if err := required("callId", m.Tool.CallID); err != nil {
				return err
			}
			if m.Kind == ToolCall {
				if err := required("tool.name", m.Tool.Name); err != nil {
					return err
				}
			}
			if len(m.Tool.Input) != 0 && !json.Valid(m.Tool.Input) {
				return fmt.Errorf("tool input must be JSON")
			}
			return nil
		}
	case UsageReported:
		if m.Usage != nil {
			return m.Usage.Validate()
		}
	case InteractionRequest:
		if m.Interaction != nil && (m.Interaction.Kind == "approval" || m.Interaction.Kind == "question") {
			return required("requestId", m.Interaction.RequestID, "prompt", m.Interaction.Prompt)
		}
	case StopReported:
		if m.Stop != nil {
			return m.Stop.Validate()
		}
	}
	return fmt.Errorf("invalid payload for event kind %q", m.Kind)
}

type Result struct {
	Execution  Execution      `json:"execution"`
	InstanceID string         `json:"instanceId"`
	CommandID  string         `json:"commandId"`
	Seq        uint64         `json:"seq"`
	OccurredAt time.Time      `json:"occurredAt"`
	State      TerminalState  `json:"state"`
	Output     string         `json:"output,omitempty"`
	Failure    *Failure       `json:"failure,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Handle     *SessionHandle `json:"handle,omitempty"`
	Stop       *StopEvidence  `json:"stop,omitempty"`
}

func (m Result) Validate() error {
	if err := validateFact(m.Execution, m.InstanceID, m.CommandID, m.Seq, m.OccurredAt); err != nil {
		return err
	}
	if !m.State.valid() {
		return fmt.Errorf("invalid terminal state %q", m.State)
	}
	if m.State == Failed && m.Failure == nil {
		return fmt.Errorf("failed result requires a reason")
	}
	if m.State == Completed && m.Failure != nil {
		return fmt.Errorf("completed result cannot carry execution failure")
	}
	if m.Failure != nil {
		if err := m.Failure.Validate(); err != nil {
			return err
		}
	}
	if m.Usage != nil {
		if err := m.Usage.Validate(); err != nil {
			return err
		}
	}
	if m.Handle != nil {
		if err := m.Handle.Validate(); err != nil {
			return err
		}
		if m.Handle.SessionID != m.Execution.SessionID {
			return fmt.Errorf("result handle refers to another session")
		}
	}
	if m.Stop != nil {
		if err := m.Stop.Validate(); err != nil {
			return err
		}
	}
	if m.State == Cancelled && (m.Stop == nil || !m.Stop.ConfirmsStop()) {
		return fmt.Errorf("cancelled result requires confirmed activity and workload stop")
	}
	return nil
}

type Ack struct {
	NodeID    string `json:"nodeId"`
	MessageID string `json:"messageId"`
}

func (m Ack) Validate() error { return required("nodeId", m.NodeID, "messageId", m.MessageID) }

type Cursor struct {
	Execution  Execution `json:"execution"`
	InstanceID string    `json:"instanceId,omitempty"`
	LastSeq    uint64    `json:"lastSeq"`
}

type ReconcileRequest struct {
	NodeID string   `json:"nodeId"`
	Known  []Cursor `json:"known"`
}

func (m ReconcileRequest) Validate() error {
	if err := required("nodeId", m.NodeID); err != nil {
		return err
	}
	for _, cursor := range m.Known {
		if err := cursor.Execution.Validate(); err != nil {
			return err
		}
		if cursor.Execution.NodeID != m.NodeID {
			return fmt.Errorf("reconciliation cursor belongs to another node")
		}
		if cursor.LastSeq != 0 && cursor.InstanceID == "" {
			return fmt.Errorf("event cursor requires an instanceId")
		}
	}
	return nil
}

type Observation struct {
	Cursor
	State  string  `json:"state"`
	Result *Result `json:"result,omitempty"`
}

type ReconcileResponse struct {
	NodeID       string        `json:"nodeId"`
	Observations []Observation `json:"observations"`
}

func (m ReconcileResponse) Validate() error {
	cursors := make([]Cursor, 0, len(m.Observations))
	for _, observation := range m.Observations {
		cursors = append(cursors, observation.Cursor)
		if observation.Result != nil {
			if err := observation.Result.Validate(); err != nil {
				return err
			}
			result := observation.Result
			if result.Execution != observation.Execution || result.InstanceID != observation.InstanceID ||
				string(result.State) != observation.State || result.Seq != observation.LastSeq {
				return fmt.Errorf("reconciled result does not match observation")
			}
		} else if observation.State != "unknown" && observation.State != "queued" && observation.State != "running" {
			return fmt.Errorf("terminal observation requires its result")
		}
		if observation.State == "running" && observation.InstanceID == "" {
			return fmt.Errorf("running observation requires an instanceId")
		}
	}
	return (ReconcileRequest{NodeID: m.NodeID, Known: cursors}).Validate()
}

func validateFact(execution Execution, instanceID, commandID string, seq uint64, at time.Time) error {
	if err := execution.Validate(); err != nil {
		return err
	}
	if err := required("instanceId", instanceID, "commandId", commandID); err != nil {
		return err
	}
	if seq == 0 || at.IsZero() {
		return fmt.Errorf("execution fact requires positive seq and occurredAt")
	}
	return nil
}
