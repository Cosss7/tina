// Package project owns project records and deterministic business rules.
// It queues execution requests but never starts an engine or model loop.
package project

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Error struct{ Code, Message string }

func (e *Error) Error() string           { return e.Message }
func problem(code, message string) error { return &Error{code, message} }

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type Agent struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	EngineID  string `json:"engineId"`
	NodeID    string `json:"nodeId"`
	Cwd       string `json:"cwd"`
}

type Task struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"projectId"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Status         string   `json:"status"`
	Kind           string   `json:"kind"`
	AssigneeKind   string   `json:"assigneeKind"`
	AssigneeID     *string  `json:"assigneeId"`
	Deps           []string `json:"deps"`
	ManualOverride bool     `json:"manualOverride"`
	PlanApproved   bool     `json:"planApproved"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

type Run struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"projectId"`
	TaskID       string  `json:"taskId"`
	AgentID      string  `json:"agentId"`
	NodeID       string  `json:"nodeId"`
	OriginatorID string  `json:"originatorId"`
	Purpose      string  `json:"purpose"`
	State        string  `json:"state"`
	CommandID    string  `json:"commandId"`
	SessionID    string  `json:"sessionId"`
	ActivityID   string  `json:"activityId"`
	StartedAt    *string `json:"startedAt"`
	EndedAt      *string `json:"endedAt"`
	Error        *string `json:"error"`
}

type Report struct {
	ID        string            `json:"id"`
	RunID     string            `json:"runId"`
	State     string            `json:"state"`
	Summary   string            `json:"summary"`
	Decisions []string          `json:"decisions"`
	Artifacts []json.RawMessage `json:"artifacts"`
	Blockers  []string          `json:"blockers"`
	Usage     map[string]any    `json:"usage"`
	Generated bool              `json:"generated"`
}

type Approval struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"projectId"`
	TaskID      string          `json:"taskId"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Payload     json.RawMessage `json:"payload"`
	RequestedAt string          `json:"requestedAt"`
	DecidedAt   *string         `json:"decidedAt"`
	DecidedBy   *string         `json:"decidedBy"`
}

type Event struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"projectId"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt"`
}

// Job is a durable request awaiting execution. An unbound Agent may have no node
// yet; a versioned dispatch can only be built once an authorized node is chosen.
type Job struct {
	CommandID string `json:"commandId"`
	RunID     string `json:"runId"`
	ProjectID string `json:"projectId"`
	AgentID   string `json:"agentId"`
	State     string `json:"state"`
	Prompt    string `json:"prompt"`
}

type CardView struct {
	Task
	Runs         []Run   `json:"runs"`
	LatestReport *Report `json:"latestReport"`
}
type Board struct {
	ProjectID string     `json:"projectId"`
	Cards     []CardView `json:"cards"`
	Approvals []Approval `json:"approvals"`
}
type Queued struct {
	Run  Run  `json:"run"`
	Task Task `json:"task"`
}
type Decision struct {
	Approval Approval `json:"approval"`
	Tasks    []Task   `json:"tasks"`
}

type PlanCard struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Acceptance  []string `json:"acceptance"`
	Deps        []string `json:"deps"`
}
type Plan struct {
	Summary string     `json:"summary"`
	Cards   []PlanCard `json:"cards"`
}
type planPayload struct {
	Plan
	Task struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"task"`
}

// Extract the first valid JSON object, accepting model prose and code fences.
func modelObject(text string, target any) error {
	for start, ch := range text {
		if ch != '{' {
			continue
		}
		var raw json.RawMessage
		decoder := json.NewDecoder(strings.NewReader(text[start:]))
		if err := decoder.Decode(&raw); err == nil {
			if err := json.Unmarshal(raw, target); err != nil {
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("model output contains no valid JSON object")
}
