package project

import (
	"context"
	"database/sql"
	"errors"
)

type RunDetail struct {
	Run    Run     `json:"run"`
	Report *Report `json:"report"`
}

func (s *Service) InspectRun(ctx context.Context, projectID, runID string) (RunDetail, error) {
	var detail RunDetail
	err := s.change(ctx, func(tx *sql.Tx) error {
		run, err := one[Run](ctx, tx, "select body from run where id=? and project_id=?", runID, projectID)
		if err != nil {
			return err
		}
		detail.Run = run
		report, err := one[Report](ctx, tx, "select body from report where run_id=?", runID)
		var business *Error
		if errors.As(err, &business) && business.Code == "not_found" {
			return nil
		}
		if err != nil {
			return err
		}
		detail.Report = &report
		return nil
	})
	return detail, err
}

// Reassignment changes future execution only; historical attempts keep their Agent IDs.
func (s *Service) AssignTask(ctx context.Context, projectID, taskID, agentID, actor string) (Task, error) {
	var task Task
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		task, err = requireTask(ctx, tx, projectID, taskID)
		if err != nil {
			return err
		}
		if task.Kind != "task" {
			return problem("conflict", "Only worker tasks may be reassigned")
		}
		var active int
		if err := tx.QueryRowContext(ctx, "select count(*) from run where task_id=? and state in ('queued','dispatched','running')", taskID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return problem("conflict", "Withdraw or finish the current attempt before reassigning")
		}
		agent, err := one[Agent](ctx, tx, "select body from agent where id=? and project_id=? and role='worker'", agentID, projectID)
		if err != nil {
			return err
		}
		task.AssigneeID, task.AssigneeKind, task.UpdatedAt = &agent.ID, "agent", now()
		if err := updateTask(ctx, tx, task); err != nil {
			return err
		}
		return emit(ctx, tx, projectID, "task.assigned", actor, task)
	})
	return task, err
}
