package project

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	p "github.com/Cosss7/tina/packages/protocol"
)

func refs(run Run) p.Execution {
	return p.Execution{ProjectID: run.ProjectID, RunID: run.ID, AgentID: run.AgentID, NodeID: run.NodeID, SessionID: run.SessionID, ActivityID: run.ActivityID}
}
func updateRun(ctx context.Context, tx *sql.Tx, run Run) error {
	return save(ctx, tx, "update run set state=?1,body=?3 where id=?2", run, run.State, run.ID)
}

// Claim records the exact dispatch before it can leave the process. A claimed
// command is never turned back into queued merely because a connection drops.
func (s *Service) Claim(ctx context.Context, registration p.Register) (*p.Dispatch, error) {
	var dispatch *p.Dispatch
	err := s.change(ctx, func(tx *sql.Tx) error {
		owned, err := many[Run](ctx, tx, "select body from run where state!='queued' and json_extract(body,'$.nodeId')=?", registration.NodeID)
		if err != nil {
			return err
		}
		active := 0
		for _, run := range owned {
			if !terminal(run.State) || (run.InstanceID != "" && run.Admission != p.Rejected && (run.Stop == nil || !run.Stop.ConfirmsStop())) {
				active++
			}
		}
		if active >= registration.MaxConcurrent {
			return nil
		}
		jobs, err := many[Job](ctx, tx, "select body from execution_request where state='queued' order by rowid")
		if err != nil {
			return err
		}
		for _, job := range jobs {
			agent, err := one[Agent](ctx, tx, "select body from agent where id=?", job.AgentID)
			if err != nil {
				return err
			}
			if agent.NodeID != registration.NodeID {
				continue
			}
			allowed := false
			for _, local := range registration.Agents {
				if local.ID == agent.ID && local.ProjectID == agent.ProjectID && local.EngineID == agent.EngineID && local.Role == agent.Role {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
			run, err := one[Run](ctx, tx, "select body from run where id=?", job.RunID)
			if err != nil {
				return err
			}
			if run.State != "queued" {
				return problem("conflict", "Queued job does not match its Run")
			}
			run.NodeID, run.State, run.NeedsReconciliation = registration.NodeID, "dispatched", true
			input := job.Prompt
			if run.Purpose == "plan" {
				project, err := one[Project](ctx, tx, "select body from project where id=?", run.ProjectID)
				if err != nil {
					return err
				}
				tasks, err := many[Task](ctx, tx, "select body from task where project_id=? order by rowid", run.ProjectID)
				if err != nil {
					return err
				}
				input += "\n项目：" + project.Name + "\n显式记忆：" + project.RouterMemory + "\n已有卡片："
				for _, task := range tasks {
					input += fmt.Sprintf("\n- %s [%s] %s", task.ID, task.Status, task.Title)
				}
			}
			d := p.Dispatch{CommandID: run.CommandID, Execution: refs(run), Config: p.SessionConfig{EngineID: agent.EngineID, Cwd: agent.Cwd, Model: agent.Model, Options: agent.Options}, Input: input}
			d.Resume = job.Resume
			if err := d.Validate(); err != nil {
				return err
			}
			job.State, job.Dispatch = "dispatched", &d
			if err := updateRun(ctx, tx, run); err != nil {
				return err
			}
			if err := save(ctx, tx, "update execution_request set state=?1,body=?3 where command_id=?2", job, job.State, job.CommandID); err != nil {
				return err
			}
			if err := emit(ctx, tx, run.ProjectID, "run.dispatched", "control-plane", run); err != nil {
				return err
			}
			dispatch = &d
			return nil
		}
		return nil
	})
	return dispatch, err
}
func (s *Service) KnownExecutions(ctx context.Context, nodeID string) ([]p.Cursor, error) {
	runs, err := many[Run](ctx, s.db, "select body from run where (state in ('dispatched','running') or json_extract(body,'$.needsReconciliation')=1) and json_extract(body,'$.nodeId')=? order by rowid", nodeID)
	if err != nil {
		return nil, err
	}
	known := []p.Cursor{}
	for _, run := range runs {
		known = append(known, p.Cursor{Execution: refs(run), InstanceID: run.InstanceID, LastSeq: run.LastSeq})
	}
	return known, nil
}
func (s *Service) NodeOffline(ctx context.Context, nodeID string) error {
	return s.change(ctx, func(tx *sql.Tx) error {
		runs, err := many[Run](ctx, tx, "select body from run where state in ('dispatched','running') and json_extract(body,'$.nodeId')=?", nodeID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			run.NeedsReconciliation = true
			if err := updateRun(ctx, tx, run); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Service) Receive(ctx context.Context, nodeID string, frame p.Frame) error {
	return s.change(ctx, func(tx *sql.Tx) error { return receive(ctx, tx, nodeID, frame) })
}
func receive(ctx context.Context, tx *sql.Tx, nodeID string, frame p.Frame) error {
	encoded, err := p.Encode(frame.ID, frame.Message)
	if err != nil {
		return err
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, "select frame from execution_fact where node_id=? and message_id=?", nodeID, frame.ID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(encoded, existing) {
			return problem("conflict", "Execution message identity conflict")
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if reconciliation, ok := frame.Message.(*p.ReconcileResponse); ok {
		if reconciliation.NodeID != nodeID {
			return problem("conflict", "Reconciliation belongs to another node")
		}
		for _, observation := range reconciliation.Observations {
			if observation.Result != nil {
				if err := receive(ctx, tx, nodeID, p.Frame{ID: frame.ID + "/" + observation.Execution.RunID, Message: observation.Result}); err != nil {
					return err
				}
				continue
			}
			run, err := one[Run](ctx, tx, "select body from run where id=?", observation.Execution.RunID)
			if err != nil {
				return err
			}
			if refs(run) != observation.Execution || run.NodeID != nodeID {
				return problem("conflict", "Reconciliation execution identity mismatch")
			}
			if terminal(run.State) {
				continue
			}
			if run.InstanceID != "" && observation.InstanceID != "" && run.InstanceID != observation.InstanceID {
				return problem("conflict", "Reconciliation instance identity mismatch")
			}
			run.NeedsReconciliation = observation.State != "running" || (run.Cancellation != nil && run.Cancellation.Admission == p.Unknown)
			if observation.State == "unknown" {
				run.Diagnostic = "执行状态未能确认；请核查本机进程及外部副作用后重新对账"
			} else if !run.NeedsReconciliation {
				run.Diagnostic = ""
			}
			if err := updateRun(ctx, tx, run); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "insert into execution_fact(node_id,message_id,seq,frame) values(?,?,0,?)", nodeID, frame.ID, encoded)
		return err
	}
	var execution p.Execution
	var commandID, instance string
	var seq uint64
	switch message := frame.Message.(type) {
	case *p.Receipt:
		execution, commandID, instance = message.Execution, message.CommandID, message.InstanceID
	case *p.Event:
		execution, commandID, instance, seq = message.Execution, message.CommandID, message.InstanceID, message.Seq
	case *p.Result:
		execution, commandID, instance, seq = message.Execution, message.CommandID, message.InstanceID, message.Seq
	default:
		return problem("invalid_argument", "Unsupported execution fact")
	}
	run, err := one[Run](ctx, tx, "select body from run where id=?", execution.RunID)
	if err != nil {
		return err
	}
	if run.NodeID != nodeID || refs(run) != execution {
		return problem("conflict", "Execution fact does not match a dispatched command")
	}
	job, err := one[Job](ctx, tx, "select body from execution_request where run_id=?", run.ID)
	if err != nil {
		return err
	}
	if job.Dispatch == nil {
		return problem("conflict", "No execution command was issued for this Run")
	}
	if receipt, ok := frame.Message.(*p.Receipt); ok && run.Cancellation != nil && commandID == run.Cancellation.CommandID {
		if run.InstanceID != "" && instance != "" && run.InstanceID != instance {
			return problem("conflict", "Cancellation receipt targets another runtime instance")
		}
		run.Cancellation.Admission, run.Cancellation.Failure, run.Cancellation.Stop = receipt.Admission, receipt.Failure, receipt.Stop
		if !terminal(run.State) {
			run.Cancellation.State = string(receipt.Admission)
			if receipt.Admission == p.Unknown {
				run.NeedsReconciliation = true
				run.Diagnostic = receipt.Failure.Message
			}
		}
		if err := updateRun(ctx, tx, run); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update cancel_request set state=? where id=?", run.Cancellation.State, commandID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "insert into execution_fact(node_id,message_id,run_id,instance_id,seq,frame) values(?,?,?,?,0,?)", nodeID, frame.ID, run.ID, instance, encoded)
		return err
	}
	if run.CommandID != commandID {
		return problem("conflict", "Unknown execution command identity")
	}
	if run.InstanceID != "" && instance != "" && run.InstanceID != instance {
		return problem("conflict", "Execution instance identity mismatch")
	}
	if seq > 0 {
		var old []byte
		err := tx.QueryRowContext(ctx, "select frame from execution_fact where node_id=? and instance_id=? and seq=?", nodeID, instance, seq).Scan(&old)
		if err == nil {
			previous, err := p.Decode(old)
			if err != nil {
				return err
			}
			previousBytes, _ := p.Encode("fact", previous.Message)
			currentBytes, _ := p.Encode("fact", frame.Message)
			if !bytes.Equal(previousBytes, currentBytes) {
				return problem("conflict", "Execution sequence payload conflict")
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	if instance != "" {
		run.InstanceID = instance
	}
	previousSeq := run.LastSeq
	if seq > run.LastSeq {
		run.LastSeq = seq
	}
	wasTerminal := terminal(run.State)
	switch message := frame.Message.(type) {
	case *p.Receipt:
		if !wasTerminal {
			if run.Admission != p.Accepted || message.Admission != p.Admitted {
				run.Admission = message.Admission
			}
			run.NeedsReconciliation = message.Admission == p.Unknown
			if message.Admission == p.Unknown {
				run.Diagnostic = message.Failure.Message
			}
			if message.Admission == p.Rejected {
				if err := updateRun(ctx, tx, run); err != nil {
					return err
				}
				if err := recordOutcome(ctx, tx, run.ID, "", message.Failure.Message); err != nil {
					return err
				}
				run, err = one[Run](ctx, tx, "select body from run where id=?", run.ID)
				if err != nil {
					return err
				}
			}
		}
	case *p.Event:
		if message.Kind == p.StopReported {
			if seq >= previousSeq || run.Stop == nil {
				run.Stop = message.Stop
			}
			if terminal(run.State) {
				run.NeedsReconciliation = !run.Stop.ConfirmsStop()
				if run.NeedsReconciliation {
					run.Diagnostic = run.Stop.Detail
					if run.Diagnostic == "" {
						run.Diagnostic = "引擎或工具进程退出尚未确认"
					}
				} else {
					run.Diagnostic = ""
				}
			}
		}
		if message.Kind == p.SessionOpened {
			run.Handle = message.Handle
		}
		if message.Kind == p.ActivityStarted && !wasTerminal && run.StartedAt == nil {
			started := message.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			run.StartedAt = &started
			run.State = "running"
			run.Admission = p.Accepted
			run.NeedsReconciliation = run.Cancellation != nil && run.Cancellation.Admission == p.Unknown
			if err := emit(ctx, tx, run.ProjectID, "run.started", run.AgentID, run); err != nil {
				return err
			}
		}
	case *p.Result:
		if !wasTerminal {
			run.NeedsReconciliation = false
			run.Diagnostic = ""
			run.Handle = message.Handle
			if message.Stop != nil || run.Stop == nil {
				run.Stop = message.Stop
			}
			if err := updateRun(ctx, tx, run); err != nil {
				return err
			}
			failure := ""
			if message.Failure != nil {
				failure = message.Failure.Message
			}
			if message.State == p.Cancelled {
				failure = "Execution was cancelled after confirmed stop"
			}
			if err := recordOutcome(ctx, tx, run.ID, message.Output, failure, string(message.State)); err != nil {
				return err
			}
			run, err = one[Run](ctx, tx, "select body from run where id=?", run.ID)
			if err != nil {
				return err
			}
			if message.Usage != nil {
				report, err := one[Report](ctx, tx, "select body from report where run_id=?", run.ID)
				if err != nil {
					return err
				}
				if report.Usage == nil {
					report.Usage = map[string]any{}
				}
				report.Usage["engineUsage"] = message.Usage
				if err := save(ctx, tx, "update report set body=?2 where id=?1", report, report.ID); err != nil {
					return err
				}
			}
		}
	}
	if err := updateRun(ctx, tx, run); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into execution_fact(node_id,message_id,run_id,instance_id,seq,frame) values(?,?,?,?,?,?)", nodeID, frame.ID, run.ID, instance, seq, encoded)
	return err
}
