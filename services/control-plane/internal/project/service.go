package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	p "github.com/Cosss7/tina/packages/protocol"
)

func (s *Service) CreateProject(ctx context.Context, projectID, name, actor string) (Project, error) {
	p := Project{ID: projectID, Name: strings.TrimSpace(name), CreatedAt: now()}
	if p.Name == "" {
		return p, problem("invalid_argument", "Project name is required")
	}
	if p.ID == "" {
		p.ID = id("project")
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "select count(*) from project where id=?", p.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return problem("conflict", "Project already exists")
		}
		if err := save(ctx, tx, "insert into project(id,name,body) values(?,?,?)", p, p.ID, p.Name); err != nil {
			return err
		}
		for _, role := range []string{"router", "worker"} {
			a := Agent{ID: role + "-" + p.ID, ProjectID: p.ID, Name: role, Role: role, EngineID: "fake", Cwd: "."}
			if err := save(ctx, tx, "insert into agent(id,project_id,role,body) values(?,?,?,?)", a, a.ID, p.ID, role); err != nil {
				return err
			}
		}
		return emit(ctx, tx, p.ID, "project.created", actor, p)
	})
	return p, err
}

// SeedDemo is explicit control-plane initialization, independent of registration.
func (s *Service) SeedDemo(ctx context.Context) error {
	projects, err := s.Projects(ctx)
	if err != nil || len(projects) != 0 {
		return err
	}
	for _, p := range []struct{ id, name string }{{"proj-website", "网站改版"}, {"proj-mobile", "移动端"}} {
		if _, err := s.CreateProject(ctx, p.id, p.name, "system:bootstrap"); err != nil {
			return err
		}
	}
	return nil
}

type EngineSelection struct {
	Model   string
	Options map[string]json.RawMessage
}

func (s *Service) ConfigureAgent(ctx context.Context, projectID, agentID, name, engineID, nodeID, cwd, actor string, selection ...EngineSelection) (Agent, error) {
	var agent Agent
	if strings.TrimSpace(name) == "" || strings.TrimSpace(engineID) == "" || strings.TrimSpace(cwd) == "" {
		return agent, problem("invalid_argument", "name, engineId and cwd are required")
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		agent, err = one[Agent](ctx, tx, "select body from agent where id=? and project_id=?", agentID, projectID)
		if err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, "select count(*) from run where project_id=? and state in ('dispatched','running') and json_extract(body,'$.agentId')=?", projectID, agentID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return problem("conflict", "Agent has a dispatched or running request; reconcile it before changing execution configuration")
		}
		agent.Name, agent.EngineID, agent.NodeID, agent.Cwd = name, engineID, nodeID, cwd
		if len(selection) > 0 {
			agent.Model, agent.Options = selection[0].Model, selection[0].Options
		}
		if err := save(ctx, tx, "update agent set body=?2 where id=?1", agent, agent.ID); err != nil {
			return err
		}
		return emit(ctx, tx, projectID, "agent.configured", actor, agent)
	})
	return agent, err
}

func (s *Service) CreateDemand(ctx context.Context, projectID, title, description, actor string) (Task, error) {
	var task Task
	title = strings.TrimSpace(title)
	if title == "" {
		return task, problem("invalid_argument", "Title is required")
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		if _, err := one[Project](ctx, tx, "select body from project where id=?", projectID); err != nil {
			return err
		}
		task = newTask(projectID, title, description, "demand")
		if err := insertTask(ctx, tx, task); err != nil {
			return err
		}
		return emit(ctx, tx, projectID, "card.created", actor, task)
	})
	return task, err
}

func newTask(projectID, title, description, kind string) Task {
	t := now()
	return Task{ID: id("task"), ProjectID: projectID, Title: title, Description: description,
		Status: "todo", Kind: kind, AssigneeKind: "agent", Deps: []string{}, CreatedAt: t, UpdatedAt: t}
}
func insertTask(ctx context.Context, tx *sql.Tx, t Task) error {
	return save(ctx, tx, "insert into task(id,project_id,body) values(?,?,?)", t, t.ID, t.ProjectID)
}
func updateTask(ctx context.Context, tx *sql.Tx, t Task) error {
	return save(ctx, tx, "update task set body=?2 where id=?1", t, t.ID)
}

func requireTask(ctx context.Context, tx *sql.Tx, projectID, taskID string) (Task, error) {
	return one[Task](ctx, tx, "select body from task where id=? and project_id=?", taskID, projectID)
}

func (s *Service) RequestPlan(ctx context.Context, projectID, taskID, actor string) (Queued, error) {
	var queued Queued
	err := s.change(ctx, func(tx *sql.Tx) error {
		task, err := requireTask(ctx, tx, projectID, taskID)
		if err != nil {
			return err
		}
		if task.Kind != "demand" || task.PlanApproved {
			return problem("conflict", "Only an unapproved demand can request a plan")
		}
		var pending int
		if err := tx.QueryRowContext(ctx, "select count(*) from approval where task_id=? and kind='plan' and state='pending'", task.ID).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return problem("conflict", "Card already has a pending plan")
		}
		queued, err = enqueue(ctx, tx, task, "router", "plan", actor)
		return err
	})
	return queued, err
}

func (s *Service) Dispatch(ctx context.Context, projectID, taskID, actor string, resumeFrom ...string) (Queued, error) {
	var queued Queued
	err := s.change(ctx, func(tx *sql.Tx) error {
		task, err := requireTask(ctx, tx, projectID, taskID)
		if err != nil {
			return err
		}
		if task.Kind != "task" || !task.PlanApproved {
			return problem("conflict", "Dispatch requires a task from an approved plan")
		}
		if task.Status == "done" {
			return problem("conflict", "Card has already passed final review")
		}
		previous, err := many[Run](ctx, tx, "select body from run where task_id=? order by rowid", task.ID)
		if err != nil {
			return err
		}
		for _, prior := range previous {
			if prior.InstanceID != "" && terminal(prior.State) && prior.Admission != p.Rejected && (prior.Stop == nil || !prior.Stop.ConfirmsStop()) {
				return problem("conflict", "Prior workload cleanup is not confirmed; reconcile before retrying")
			}
		}
		var resume *Run
		if len(resumeFrom) > 0 && resumeFrom[0] != "" {
			for _, prior := range previous {
				if prior.ID == resumeFrom[0] {
					copy := prior
					resume = &copy
					break
				}
			}
			if resume == nil || !terminal(resume.State) || resume.Handle == nil {
				return problem("conflict", "A terminal Run with a native handle from this task is required for resume")
			}
		}
		for _, depID := range task.Deps {
			dep, err := requireTask(ctx, tx, projectID, depID)
			if err != nil {
				return err
			}
			if dep.Status != "done" {
				return problem("conflict", "Task dependencies have not passed final review")
			}
		}
		queued, err = enqueue(ctx, tx, task, "worker", "report", actor, resume)
		return err
	})
	return queued, err
}

func enqueue(ctx context.Context, tx *sql.Tx, task Task, role, purpose, actor string, resume ...*Run) (Queued, error) {
	var active int
	if err := tx.QueryRowContext(ctx, "select count(*) from run where task_id=? and state in ('queued','dispatched','running')", task.ID).Scan(&active); err != nil {
		return Queued{}, err
	}
	if active != 0 {
		return Queued{}, problem("conflict", "Card already has an active run")
	}
	query := "select body from agent where project_id=? and role=? order by rowid limit 1"
	args := []any{task.ProjectID, role}
	if role == "worker" && task.AssigneeID != nil {
		query = "select body from agent where project_id=? and role=? and id=?"
		args = append(args, *task.AssigneeID)
	}
	agent, err := one[Agent](ctx, tx, query, args...)
	if err != nil {
		return Queued{}, err
	}
	run := Run{ID: id("run"), ProjectID: task.ProjectID, TaskID: task.ID, AgentID: agent.ID, NodeID: agent.NodeID,
		OriginatorID: actor, Purpose: purpose, State: "queued", CommandID: id("command"), SessionID: id("session"), ActivityID: id("activity")}
	var handle *p.SessionHandle
	if len(resume) > 0 && resume[0] != nil {
		prior := resume[0]
		if prior.AgentID != agent.ID || prior.Handle.EngineID != agent.EngineID {
			return Queued{}, problem("conflict", "Native session does not match the selected Agent/engine")
		}
		run.SessionID = prior.SessionID
		handle = prior.Handle
	}
	if err := save(ctx, tx, "insert into run(id,project_id,task_id,state,body) values(?,?,?,?,?)", run, run.ID, run.ProjectID, task.ID, run.State); err != nil {
		return Queued{}, err
	}
	job := Job{CommandID: run.CommandID, RunID: run.ID, ProjectID: run.ProjectID, AgentID: agent.ID, State: "queued", Prompt: prompt(task, purpose)}
	job.Resume = handle
	if err := save(ctx, tx, "insert into execution_request(command_id,run_id,project_id,state,body) values(?,?,?,?,?)", job, job.CommandID, run.ID, run.ProjectID, job.State); err != nil {
		return Queued{}, err
	}
	if err := emit(ctx, tx, task.ProjectID, "run.queued", actor, run); err != nil {
		return Queued{}, err
	}
	if err := refresh(ctx, tx, &task); err != nil {
		return Queued{}, err
	}
	return Queued{Run: run, Task: task}, nil
}

func (s *Service) DecidePlan(ctx context.Context, projectID, approvalID, decision, actor string) (Decision, error) {
	result := Decision{Tasks: []Task{}}
	if decision != "approved" && decision != "rejected" {
		return result, problem("invalid_argument", "decision must be approved or rejected")
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		a, err := one[Approval](ctx, tx, "select body from approval where id=? and project_id=?", approvalID, projectID)
		if err != nil {
			return err
		}
		if a.Kind != "plan" || a.State != "pending" {
			return problem("conflict", "Plan is not pending")
		}
		var plan planPayload
		if err := json.Unmarshal(a.Payload, &plan); err != nil {
			return err
		}
		if err := validatePlan(plan.Plan); err != nil {
			return problem("invalid_argument", err.Error())
		}
		t := now()
		a.State, a.DecidedAt, a.DecidedBy = decision, &t, &actor
		if err := save(ctx, tx, "update approval set state=?1,body=?3 where id=?2", a, a.State, a.ID); err != nil {
			return err
		}
		result.Approval = a
		if err := emit(ctx, tx, projectID, "plan."+decision, actor, a); err != nil {
			return err
		}
		if decision == "rejected" {
			return nil
		}
		worker, err := one[Agent](ctx, tx, "select body from agent where project_id=? and role='worker' order by rowid limit 1", projectID)
		if err != nil {
			return err
		}
		keys := map[string]string{}
		for _, card := range plan.Cards {
			keys[card.Key] = id("task")
		}
		for _, card := range plan.Cards {
			task := newTask(projectID, card.Title, strings.TrimSpace(card.Description+"\n\n验收标准：\n- "+strings.Join(card.Acceptance, "\n- ")), "task")
			task.ID, task.PlanApproved, task.AssigneeID = keys[card.Key], true, &worker.ID
			for _, key := range card.Deps {
				task.Deps = append(task.Deps, keys[key])
			}
			if err := insertTask(ctx, tx, task); err != nil {
				return err
			}
			if err := emit(ctx, tx, projectID, "card.created", "router", task); err != nil {
				return err
			}
			result.Tasks = append(result.Tasks, task)
		}
		demand, err := requireTask(ctx, tx, projectID, a.TaskID)
		if err != nil {
			return err
		}
		demand.PlanApproved = true
		return refresh(ctx, tx, &demand)
	})
	return result, err
}

func (s *Service) Review(ctx context.Context, projectID, taskID, decision, note, actor string) (Task, error) {
	var task Task
	if decision != "approved" && decision != "rejected" {
		return task, problem("invalid_argument", "decision must be approved or rejected")
	}
	err := s.change(ctx, func(tx *sql.Tx) error {
		var err error
		task, err = requireTask(ctx, tx, projectID, taskID)
		if err != nil {
			return err
		}
		if task.Kind != "task" || task.Status == "done" {
			return problem("conflict", "Card is not available for final review")
		}
		runs, err := many[Run](ctx, tx, "select body from run where task_id=? order by rowid", task.ID)
		if err != nil {
			return err
		}
		if len(runs) == 0 || !terminal(runs[len(runs)-1].State) {
			return problem("conflict", "No terminal run available for review")
		}
		report, err := one[Report](ctx, tx, "select body from report where run_id=?", runs[len(runs)-1].ID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"reportId": report.ID, "summary": report.Summary, "note": note})
		if err != nil {
			return err
		}
		t := now()
		a := Approval{ID: id("approval"), ProjectID: projectID, TaskID: task.ID, Kind: "final", State: decision, Payload: payload, RequestedAt: t, DecidedAt: &t, DecidedBy: &actor}
		if err := insertApproval(ctx, tx, a); err != nil {
			return err
		}
		if err := emit(ctx, tx, projectID, "review."+decision, actor, a); err != nil {
			return err
		}
		return refresh(ctx, tx, &task)
	})
	return task, err
}

func insertApproval(ctx context.Context, tx *sql.Tx, a Approval) error {
	return save(ctx, tx, "insert into approval(id,project_id,task_id,kind,state,body) values(?,?,?,?,?,?)", a, a.ID, a.ProjectID, a.TaskID, a.Kind, a.State)
}
func terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func refresh(ctx context.Context, tx *sql.Tx, task *Task) error {
	if !task.ManualOverride {
		task.Status = "todo"
		if task.Kind == "demand" {
			if task.PlanApproved {
				task.Status = "done"
			}
		} else {
			var approved, active, reports int
			if err := tx.QueryRowContext(ctx, "select count(*) from approval where task_id=? and kind='final' and state='approved'", task.ID).Scan(&approved); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "select count(*) from run where task_id=? and state in ('queued','dispatched','running')", task.ID).Scan(&active); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "select count(*) from report join run on report.run_id=run.id where run.task_id=? and run.state in ('completed','failed','cancelled')", task.ID).Scan(&reports); err != nil {
				return err
			}
			switch {
			case approved > 0:
				task.Status = "done"
			case active > 0:
				task.Status = "doing"
			case reports > 0:
				task.Status = "review"
			}
		}
	}
	task.UpdatedAt = now()
	return updateTask(ctx, tx, *task)
}

func (s *Service) Board(ctx context.Context, projectID string) (Board, error) {
	b := Board{ProjectID: projectID, Cards: []CardView{}, Approvals: []Approval{}}
	// A read transaction gives all columns and approvals one coherent snapshot.
	err := s.change(ctx, func(tx *sql.Tx) error {
		if _, err := one[Project](ctx, tx, "select body from project where id=?", projectID); err != nil {
			return err
		}
		tasks, err := many[Task](ctx, tx, "select body from task where project_id=? order by rowid", projectID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			runs, err := many[Run](ctx, tx, "select body from run where task_id=? order by rowid", task.ID)
			if err != nil {
				return err
			}
			card := CardView{Task: task, Runs: runs}
			if len(runs) > 0 {
				reports, err := many[Report](ctx, tx, "select body from report where run_id=?", runs[len(runs)-1].ID)
				if err != nil {
					return err
				}
				if len(reports) > 0 {
					card.LatestReport = &reports[0]
				}
			}
			b.Cards = append(b.Cards, card)
		}
		b.Approvals, err = many[Approval](ctx, tx, "select body from approval where project_id=? and state='pending' order by rowid", projectID)
		return err
	})
	return b, err
}
