package project

import (
	p "github.com/Cosss7/tina/packages/protocol"
	"testing"
	"time"
)

func TestRebindingWaitsForCleanupSoOldNodeCanStillReconcile(t *testing.T) {
	s, _, dispatch := executionSetup(t)
	result := p.Result{Execution: dispatch.Execution, CommandID: dispatch.CommandID, InstanceID: "instance", Seq: 1, OccurredAt: time.Now().UTC(), State: p.Completed, Output: goodPlan}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "completed", Message: &result}); err != nil {
		t.Fatal(err)
	}
	configure := func() error {
		_, err := s.ConfigureAgent(testCtx, "p", dispatch.Execution.AgentID, "router", "fake", "new-node", ".", "human")
		return err
	}
	if err := configure(); code(err) != "conflict" {
		t.Fatal("unproven old execution lost its Agent binding", err)
	}
	stop := p.Event{Execution: dispatch.Execution, CommandID: dispatch.CommandID, InstanceID: "instance", Seq: 2, OccurredAt: time.Now().UTC(), Kind: p.StopReported, Stop: &p.StopEvidence{Mode: p.InterruptProcess, ActivityStopped: true, Root: p.ExitExited, Workload: p.ExitExited}}
	if err := s.Receive(testCtx, "node", p.Frame{ID: "cleanup", Message: &stop}); err != nil {
		t.Fatal(err)
	}
	if err := configure(); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalReportsAndReassignmentRespectProjectAndActiveRun(t *testing.T) {
	s := openTest(t, ":memory:")
	task := planned(t, s, "p")
	setup(t, s, "other")
	if _, err := s.AssignTask(testCtx, "p", task.ID, "worker-other", "human"); code(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := s.AssignTask(testCtx, "p", task.ID, "router-p", "human"); code(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := s.AssignTask(testCtx, "p", task.ID, "worker-p", "human"); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Dispatch(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignTask(testCtx, "p", task.ID, "worker-p", "human"); code(err) != "conflict" {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, queued.Run.ID, goodReport, ""); err != nil {
		t.Fatal(err)
	}
	first, err := s.InspectRun(testCtx, "p", queued.Run.ID)
	if err != nil || first.Report == nil || first.Report.Generated {
		t.Fatal(first, err)
	}
	second, err := s.Dispatch(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.InspectRun(testCtx, "p", second.Run.ID)
	if err != nil || pending.Report != nil {
		t.Fatal(pending, err)
	}
	historical, err := s.InspectRun(testCtx, "p", queued.Run.ID)
	if err != nil || historical.Report.ID != first.Report.ID {
		t.Fatal(historical, err)
	}
	if _, err := s.InspectRun(testCtx, "other", queued.Run.ID); code(err) != "not_found" {
		t.Fatal(err)
	}
}
