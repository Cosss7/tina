package project

import (
	"context"
	"database/sql"

	p "github.com/Cosss7/tina/packages/protocol"
)

func (s *Service) GetRun(ctx context.Context, projectID, runID string) (Run, error) {
	return one[Run](ctx, s.db, "select body from run where id=? and project_id=?", runID, projectID)
}
func (s *Service) CancelRun(ctx context.Context, projectID, runID, reason, actor string) (Run, error) {
	var run Run
	if reason == "" {
		reason = "用户请求取消"
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		run, err = one[Run](ctx, tx, "select body from run where id=? and project_id=?", runID, projectID)
		if err != nil {
			return err
		}
		if terminal(run.State) || run.Cancellation != nil {
			return nil
		}
		cancellation := &Cancellation{CommandID: id("cancel"), RequestedAt: now(), RequestedBy: actor, Reason: reason, State: "queued"}
		run.Cancellation = cancellation
		request := p.Cancel{CommandID: cancellation.CommandID, Execution: refs(run), InstanceID: run.InstanceID, Reason: reason}
		if err := save(ctx, tx, "insert into cancel_request(id,run_id,state,body) values(?,?,?,?)", request, request.CommandID, run.ID, "queued"); err != nil {
			return err
		}
		if err := updateRun(ctx, tx, run); err != nil {
			return err
		}
		if err := emit(ctx, tx, projectID, "run.cancel.requested", actor, cancellation); err != nil {
			return err
		}
		if run.State == "queued" {
			run.Stop = &p.StopEvidence{Mode: p.InterruptNone, ActivityStopped: true, Root: p.ExitNotApplicable, Workload: p.ExitNotApplicable, Detail: "withdrawn before dispatch"}
			if err := updateRun(ctx, tx, run); err != nil {
				return err
			}
			if err := recordOutcome(ctx, tx, run.ID, "", "Cancelled before dispatch", "cancelled"); err != nil {
				return err
			}
			run, err = one[Run](ctx, tx, "select body from run where id=?", runID)
			return err
		}
		return nil
	})
	return run, err
}
func (s *Service) PendingCancellations(ctx context.Context, nodeID string) ([]p.Cancel, error) {
	var requests []p.Cancel
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		requests, err = many[p.Cancel](ctx, tx, "select c.body from cancel_request c join run r on r.id=c.run_id where c.state!='settled' and json_extract(r.body,'$.nodeId')=? and r.state in ('dispatched','running') order by c.rowid", nodeID)
		if err != nil {
			return err
		}
		for _, request := range requests {
			run, err := one[Run](ctx, tx, "select body from run where id=?", request.Execution.RunID)
			if err != nil {
				return err
			}
			if run.Cancellation != nil && run.Cancellation.State == "queued" {
				run.Cancellation.State = "sent"
				if err := updateRun(ctx, tx, run); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "update cancel_request set state='sent' where id=?", request.CommandID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return requests, err
}
func (s *Service) RequestReconciliation(ctx context.Context, projectID, runID, actor string) (Run, error) {
	var run Run
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		run, err = one[Run](ctx, tx, "select body from run where id=? and project_id=?", runID, projectID)
		if err != nil {
			return err
		}
		if run.State == "queued" {
			return problem("conflict", "Run has not been dispatched")
		}
		job, err := one[Job](ctx, tx, "select body from execution_request where run_id=?", run.ID)
		if err != nil {
			return err
		}
		if job.Dispatch == nil {
			return problem("conflict", "Run was never dispatched")
		}
		run.NeedsReconciliation = !terminal(run.State) || run.Stop == nil || !run.Stop.ConfirmsStop()
		if err := updateRun(ctx, tx, run); err != nil {
			return err
		}
		return emit(ctx, tx, projectID, "run.reconciliation.requested", actor, map[string]any{"runId": run.ID})
	})
	return run, err
}
