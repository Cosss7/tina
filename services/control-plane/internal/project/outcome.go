package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func validatePlan(plan Plan) error {
	if len(plan.Cards) == 0 {
		return fmt.Errorf("Plan cards must be a non-empty array")
	}
	byKey := map[string]PlanCard{}
	for _, card := range plan.Cards {
		if strings.TrimSpace(card.Key) == "" || strings.TrimSpace(card.Title) == "" || len(card.Acceptance) == 0 {
			return fmt.Errorf("Plan cards require key, title and acceptance criteria")
		}
		if _, exists := byKey[card.Key]; exists {
			return fmt.Errorf("Duplicate plan key %q", card.Key)
		}
		for _, criterion := range card.Acceptance {
			if strings.TrimSpace(criterion) == "" {
				return fmt.Errorf("Empty acceptance criterion")
			}
		}
		byKey[card.Key] = card
	}
	visited, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		card, ok := byKey[key]
		if !ok {
			return fmt.Errorf("Unknown dependency %q", key)
		}
		if visiting[key] {
			return fmt.Errorf("Cyclic plan dependencies")
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dep := range card.Deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, key)
		visited[key] = true
		return nil
	}
	for key := range byKey {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

// RecordOutcome is a trusted integration boundary for an observed terminal fact.
// P2 exposes no HTTP route for it. P4 must validate message identity, evidence and
// deduplication before calling this method; request timeouts must never call it.
func (s *Service) RecordOutcome(ctx context.Context, runID, output, executionFailure string) error {
	return s.change(ctx, func(tx *sql.Tx) error {
		return recordOutcome(ctx, tx, runID, output, executionFailure)
	})
}
func recordOutcome(ctx context.Context, tx *sql.Tx, runID, output, executionFailure string, forcedState ...string) error {
	run, err := one[Run](ctx, tx, "select body from run where id=?", runID)
	if err != nil {
		return err
	}
	if terminal(run.State) {
		return nil
	}
	task, err := requireTask(ctx, tx, run.ProjectID, run.TaskID)
	if err != nil {
		return err
	}
	report := Report{ID: id("report"), RunID: runID, State: "done", Summary: "", Decisions: []string{}, Artifacts: []json.RawMessage{}, Blockers: []string{}, Usage: map[string]any{}}
	var plan Plan
	if executionFailure == "" || run.Purpose != "plan" {
		if run.Purpose == "plan" {
			err = modelObject(output, &plan)
			if err == nil {
				err = validatePlan(plan)
			}
			if err == nil {
				report.Summary = plan.Summary
			}
		} else {
			// Decode into a separate model payload: engines cannot choose record IDs
			// or claim a generated fallback report came from the control plane.
			var raw struct {
				State     string            `json:"state"`
				Summary   string            `json:"summary"`
				Decisions []string          `json:"decisions"`
				Artifacts []json.RawMessage `json:"artifacts"`
				Blockers  []string          `json:"blockers"`
				Usage     map[string]any    `json:"usage"`
			}
			err = modelObject(output, &raw)
			if err == nil && (strings.TrimSpace(raw.Summary) == "" || (raw.State != "done" && raw.State != "blocked" && raw.State != "failed")) {
				err = fmt.Errorf("Invalid worker report state or summary")
			}
			if err == nil {
				report.State, report.Summary = raw.State, raw.Summary
				if raw.Decisions != nil {
					report.Decisions = raw.Decisions
				}
				if raw.Artifacts != nil {
					report.Artifacts = raw.Artifacts
				}
				if raw.Blockers != nil {
					report.Blockers = raw.Blockers
				}
				if raw.Usage != nil {
					report.Usage = raw.Usage
				}
			}
		}
		if err != nil && executionFailure == "" {
			executionFailure = err.Error()
		}
	}
	validWorkerReport := run.Purpose != "plan" && err == nil && report.Summary != ""
	run.State = "completed"
	if executionFailure != "" {
		run.State = "failed"
		run.Error = &executionFailure
		if !validWorkerReport {
			report.State, report.Summary, report.Generated = "failed", "Run failed: "+executionFailure, true
		}
		if len(forcedState) > 0 && forcedState[0] == "cancelled" {
			run.State = "cancelled"
			if !validWorkerReport {
				report.Summary = "Run cancelled: " + executionFailure
			}
		}
	} else if run.Purpose == "plan" {
		payload := planPayload{Plan: plan}
		payload.Task.ID, payload.Task.Title = task.ID, task.Title
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		a := Approval{ID: id("approval"), ProjectID: run.ProjectID, TaskID: task.ID, Kind: "plan", State: "pending", Payload: body, RequestedAt: now()}
		if err := insertApproval(ctx, tx, a); err != nil {
			return err
		}
		if err := emit(ctx, tx, run.ProjectID, "plan.requested", run.AgentID, a); err != nil {
			return err
		}
	}
	ended := now()
	run.EndedAt = &ended
	if run.Cancellation != nil {
		run.Cancellation.State = "settled"
		if _, err := tx.ExecContext(ctx, "update cancel_request set state='settled' where run_id=?", run.ID); err != nil {
			return err
		}
	}
	if err := save(ctx, tx, "insert into report(id,run_id,body) values(?,?,?)", report, report.ID, runID); err != nil {
		return err
	}
	if err := save(ctx, tx, "update run set state=?1,body=?3 where id=?2", run, run.State, run.ID); err != nil {
		return err
	}
	job, err := one[Job](ctx, tx, "select body from execution_request where run_id=?", runID)
	if err != nil {
		return err
	}
	job.State = "settled"
	if err := save(ctx, tx, "update execution_request set state=?1,body=?3 where command_id=?2", job, job.State, job.CommandID); err != nil {
		return err
	}
	if err := emit(ctx, tx, run.ProjectID, "run."+run.State, run.AgentID, run); err != nil {
		return err
	}
	return refresh(ctx, tx, &task)
}

func prompt(task Task, purpose string) string {
	context := "\n标题：" + task.Title + "\n描述：" + task.Description + "\n"
	if purpose == "plan" {
		return "[TINA:PLAN]\n你是项目协调者。将需求拆为可执行的任务建议，不要自己动手。" + context +
			`只输出 JSON：{"summary":"计划概述","cards":[{"key":"c1","title":"任务","description":"内容","acceptance":["可验证的验收点"],"deps":[]}]}` +
			"\n每张卡必须有验收标准；deps 只能引用同一计划的 key，不能有环。建议经过控制面校验和人工审批才能执行。"
	}
	return "[TINA:REPORT]\n你是执行者。这是联调环境，无真实代码交付要求。不要执行命令或读仓库；按任务信息返回结构化报告。" + context +
		`只输出 JSON：{"state":"done","summary":"执行情况","decisions":[],"artifacts":[],"blockers":[],"usage":{}}。state 只能为 done/blocked/failed。`
}
