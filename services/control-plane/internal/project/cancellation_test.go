package project

import (
	"path/filepath"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
)

func TestQueuedCancellationIsAtomicAndNeverDispatched(t *testing.T) {
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelRun(testCtx, "p", q.Run.ID, "withdraw", "human")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != "cancelled" || cancelled.StartedAt != nil || cancelled.Cancellation.RequestedBy != "human" {
		t.Fatal(cancelled)
	}
	again, err := s.CancelRun(testCtx, "p", q.Run.ID, "duplicate", "other")
	if err != nil || again.Cancellation.CommandID != cancelled.Cancellation.CommandID {
		t.Fatal(again, err)
	}
	jobs, _ := s.Queue(testCtx, "p")
	if len(jobs) != 0 {
		t.Fatal("cancelled request remained queued", jobs)
	}
}
func TestCancelRequestAndReceiptDoNotSettleAnExecution(t *testing.T) {
	s, _, dispatch := executionSetup(t)
	run, err := s.CancelRun(testCtx, "p", dispatch.Execution.RunID, "stop", "human")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "dispatched" || run.EndedAt != nil {
		t.Fatal("request fabricated termination", run)
	}
	receipt := p.Receipt{CommandID: run.Cancellation.CommandID, Execution: dispatch.Execution, InstanceID: "instance", Admission: p.Accepted, Stop: &p.StopEvidence{Mode: p.InterruptTurn, Requested: true, ActivityStopped: true, SessionUsable: true, Root: p.ExitNotApplicable, Workload: p.ExitNotApplicable}}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "cancel-receipt", Message: &receipt}); err != nil {
		t.Fatal(err)
	}
	run, _ = s.GetRun(testCtx, "p", run.ID)
	if run.State != "dispatched" || run.EndedAt != nil {
		t.Fatal("receipt fabricated a native terminal result", run)
	}
	result := p.Result{Execution: dispatch.Execution, CommandID: dispatch.CommandID, InstanceID: "instance", Seq: 1, OccurredAt: time.Now().UTC(), State: p.Completed, Output: goodPlan}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "native-completed", Message: &result}); err != nil {
		t.Fatal(err)
	}
	run, _ = s.GetRun(testCtx, "p", run.ID)
	if run.State != "completed" || run.Cancellation.State != "settled" {
		t.Fatal("late cancellation overwrote completion", run)
	}
}
func TestUnknownCancellationPersistsAndCannotTriggerSecondAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTest(t, path)
	task := setup(t, s, "p")
	agents, _ := s.Agents(testCtx, "p")
	if _, err := s.ConfigureAgent(testCtx, "p", agents[0].ID, "router", "fake", "node", ".", "human"); err != nil {
		t.Fatal(err)
	}
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	reg := p.Register{NodeID: "node", MaxConcurrent: 1, Agents: []p.AgentRegistration{{ID: agents[0].ID, ProjectID: "p", Role: "router", EngineID: "fake"}}}
	d, err := s.Claim(testCtx, reg)
	if err != nil || d == nil {
		t.Fatal(d, err)
	}
	cancelled, err := s.CancelRun(testCtx, "p", q.Run.ID, "stop", "human")
	if err != nil {
		t.Fatal(err)
	}
	receipt := p.Receipt{CommandID: cancelled.Cancellation.CommandID, Execution: d.Execution, Admission: p.Unknown, Failure: &p.Failure{Code: "stop_unproven", Message: "tool workload remains unproven"}}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "unknown", Message: &receipt}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, path)
	run, err := s.GetRun(testCtx, "p", q.Run.ID)
	if err != nil || !run.NeedsReconciliation || run.State != "dispatched" || run.Cancellation.Admission != p.Unknown {
		t.Fatal(run, err)
	}
	if _, err := s.RequestPlan(testCtx, "p", task.ID, "human"); code(err) != "conflict" {
		t.Fatal("uncertain attempt was repeated", err)
	}
	pending, err := s.PendingCancellations(testCtx, "node")
	if err != nil || len(pending) != 1 || pending[0].CommandID != cancelled.Cancellation.CommandID {
		t.Fatal(pending, err)
	}
}
