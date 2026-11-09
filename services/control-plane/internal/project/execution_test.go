package project

import (
	"strings"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
)

func executionSetup(t *testing.T) (*Service, p.Register, p.Dispatch) {
	t.Helper()
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	agents, err := s.Agents(testCtx, "p")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.ConfigureAgent(testCtx, "p", agents[0].ID, "Router", "fake", "node", ".", "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollNode(testCtx, "node", "test-node", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	reg := p.Register{NodeID: "node", Name: "test-node", Version: "test", MaxConcurrent: 1, Engines: []p.EngineRegistration{{ID: "fake", Capabilities: p.Capabilities{Resume: true, Interrupt: p.InterruptTurn}}}, Agents: []p.AgentRegistration{{ID: agent.ID, ProjectID: "p", Role: "router", EngineID: "fake"}}}
	if err := s.RegisterNode(testCtx, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRouterMemory(testCtx, "p", "保留接口兼容性", "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPlan(testCtx, "p", task.ID, "human"); err != nil {
		t.Fatal(err)
	}
	dispatch, err := s.Claim(testCtx, reg)
	if err != nil || dispatch == nil {
		t.Fatal(dispatch, err)
	}
	return s, reg, *dispatch
}
func TestDispatchRequiresBindingAndFactsDriveBusinessState(t *testing.T) {
	s, reg, d := executionSetup(t)
	if !strings.Contains(d.Input, "保留接口兼容性") || !strings.Contains(d.Input, "项目：项目") {
		t.Fatal("router context not composed", d.Input)
	}
	b, _ := s.Board(testCtx, "p")
	run := b.Cards[0].Runs[0]
	if run.State != "dispatched" || run.StartedAt != nil {
		t.Fatal("claim falsely started execution", run)
	}
	receipt := p.Receipt{CommandID: d.CommandID, Execution: d.Execution, Admission: p.Accepted, InstanceID: "instance"}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "receipt", Message: &receipt}); err != nil {
		t.Fatal(err)
	}
	b, _ = s.Board(testCtx, "p")
	if b.Cards[0].Runs[0].StartedAt != nil {
		t.Fatal("accepted receipt became a start")
	}
	started := p.Event{Execution: d.Execution, InstanceID: "instance", CommandID: d.CommandID, Seq: 1, OccurredAt: time.Now().UTC(), Kind: p.ActivityStarted}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "start", Message: &started}); err != nil {
		t.Fatal(err)
	}
	result := p.Result{Execution: d.Execution, InstanceID: "instance", CommandID: d.CommandID, Seq: 2, OccurredAt: time.Now().UTC(), State: p.Completed, Output: goodPlan}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "result", Message: &result}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Events(testCtx, "p")
	for _, frameID := range []string{"result", "reconcile-result-alias"} {
		if err := s.Receive(testCtx, "node", p.Frame{ID: frameID, Message: &result}); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.Events(testCtx, "p")
	if len(after) != len(before) {
		t.Fatal("duplicate result created duplicate business events")
	}
	b, _ = s.Board(testCtx, "p")
	if len(b.Approvals) != 1 || b.Cards[0].Runs[0].State != "completed" {
		t.Fatal(b)
	}
	cleanup := started
	cleanup.Kind = p.StopReported
	cleanup.Seq = 3
	cleanup.Stop = &p.StopEvidence{Mode: p.InterruptProcess, ActivityStopped: true, Root: p.ExitExited, Workload: p.ExitExited}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "cleanup", Message: &cleanup}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.Claim(testCtx, reg); err != nil || next != nil {
		t.Fatal(next, err)
	}
	conflict := result
	conflict.Output = "changed"
	if err := s.Receive(testCtx, "node", p.Frame{ID: "result", Message: &conflict}); code(err) != "conflict" {
		t.Fatal("same message ID overwrote native result", err)
	}
}
func TestOfflineAndUnknownReconciliationDoNotResendOrInventTerminal(t *testing.T) {
	s, reg, d := executionSetup(t)
	if err := s.NodeOffline(testCtx, "node"); err != nil {
		t.Fatal(err)
	}
	response := p.ReconcileResponse{NodeID: "node", Observations: []p.Observation{{Cursor: p.Cursor{Execution: d.Execution}, State: "unknown"}}}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "reconcile", Message: &response}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Board(testCtx, "p")
	run := b.Cards[0].Runs[0]
	if run.State != "dispatched" || !run.NeedsReconciliation || run.EndedAt != nil || b.Cards[0].LatestReport != nil {
		t.Fatal("offline fabricated an execution outcome", run)
	}
	if next, err := s.Claim(testCtx, reg); err != nil || next != nil {
		t.Fatal("uncertain command was sent again", next, err)
	}
}
func TestEnrollmentAndRegistrationCannotChangeProjectIdentity(t *testing.T) {
	s, reg, _ := executionSetup(t)
	if s.AuthenticateNode(testCtx, "node", "wrong") || s.AuthenticateNode(testCtx, "other", strings.Repeat("a", 32)) {
		t.Fatal("invalid enrollment authenticated")
	}
	if !s.AuthenticateNode(testCtx, "node", strings.Repeat("a", 32)) {
		t.Fatal("valid enrollment failed")
	}
	if err := s.EnrollNode(testCtx, "node", "renamed", strings.Repeat("b", 32)); code(err) != "conflict" {
		t.Fatal(err)
	}
	reg.Agents[0].ProjectID = "injected"
	if err := s.RegisterNode(testCtx, reg); err == nil {
		t.Fatal("registration created a project")
	}
	projects, _ := s.Projects(testCtx)
	if len(projects) != 1 {
		t.Fatal(projects)
	}
}
