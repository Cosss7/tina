package project

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

type ImportResult struct {
	GeneratedReports int    `json:"generatedReports"`
	InterruptedRuns  int    `json:"interruptedRuns"`
	Fingerprint      string `json:"fingerprint"`
	Backup           string `json:"backup"`
	Projects         int    `json:"projects"`
	Tasks            int    `json:"tasks"`
	Runs             int    `json:"runs"`
	Reports          int    `json:"reports"`
	Approvals        int    `json:"approvals"`
	Events           int    `json:"events"`
	AlreadyImported  bool   `json:"alreadyImported"`
}
type legacySnapshot struct {
	Tasks     []Task
	Runs      []Run
	Reports   []Report
	Approvals []Approval
	Events    []Event
}

// ImportH0 preserves source IDs and writes no source records. The operator must
// stop the old execution service first; an interrupted attempt is a migration
// failure with an explicit report, never a fabricated native cancellation.
func (s *Service) ImportH0(ctx context.Context, source, backupDirectory string, oldStopped bool) (ImportResult, error) {
	var result ImportResult
	if !oldStopped {
		return result, problem("conflict", "Stop the old H0 execution service and confirm with -h0-stopped before importing")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return result, err
	}
	sourceInfo, err := os.Stat(absolute)
	if err != nil {
		return result, err
	}
	if destinationInfo, err := os.Stat(s.filename); err == nil && os.SameFile(sourceInfo, destinationInfo) {
		return result, problem("conflict", "Source and destination must be different databases")
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}
	if len(u.Path) > 1 && u.Path[1] == ':' {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var application int
	if err := db.QueryRowContext(ctx, "pragma application_id").Scan(&application); err != nil {
		return result, err
	}
	if application != 0 {
		return result, problem("conflict", "Source is not an H0 database")
	}
	if err := os.MkdirAll(backupDirectory, 0700); err != nil {
		return result, err
	}
	backup, err := filepath.Abs(filepath.Join(backupDirectory, "h0-"+id("snapshot")+".db"))
	if err != nil {
		return result, err
	}
	file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	file.Close()
	// VACUUM INTO includes committed WAL pages, unlike a raw main-file copy.
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		os.Remove(backup)
		return result, fmt.Errorf("snapshot H0 database: %w", err)
	}
	result.Backup = backup
	backupURL := url.URL{Scheme: "file", Path: filepath.ToSlash(backup), RawQuery: "mode=ro"}
	if len(backupURL.Path) > 1 && backupURL.Path[1] == ':' {
		backupURL.Path = "/" + backupURL.Path
	}
	snapshotDB, err := sql.Open("sqlite", backupURL.String())
	if err != nil {
		return result, err
	}
	defer snapshotDB.Close()
	snapshotDB.SetMaxOpenConns(1)
	snapshot, err := readLegacy(ctx, snapshotDB)
	if err != nil {
		return result, fmt.Errorf("read H0 data (snapshot retained at %s): %w", backup, err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(encoded)
	result.Fingerprint = hex.EncodeToString(digest[:])
	if previous, err := one[ImportResult](ctx, s.db, "select body from migration where fingerprint=?", result.Fingerprint); err == nil {
		os.Remove(backup)
		previous.AlreadyImported = true
		return previous, nil
	}
	err = s.change(ctx, func(tx *sql.Tx) error {
		projects := map[string]bool{}
		taskByID := map[string]Task{}
		runByID := map[string]Run{}
		reported := map[string]bool{}
		for _, task := range snapshot.Tasks {
			projects[task.ProjectID] = true
			taskByID[task.ID] = task
		}
		for _, approval := range snapshot.Approvals {
			projects[approval.ProjectID] = true
		}
		for _, event := range snapshot.Events {
			projects[event.ProjectID] = true
		}
		for _, run := range snapshot.Runs {
			runByID[run.ID] = run
		}
		for _, report := range snapshot.Reports {
			reported[report.RunID] = true
		}
		for projectID := range projects {
			var exists int
			if err := tx.QueryRowContext(ctx, "select count(*) from project where id=?", projectID).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				return problem("conflict", "Destination project already exists; import into an empty project store")
			}
			name := projectID
			if projectID == "proj-website" {
				name = "网站改版"
			}
			if projectID == "proj-mobile" {
				name = "移动端"
			}
			project := Project{ID: projectID, Name: name, CreatedAt: now()}
			if err := save(ctx, tx, "insert into project(id,name,body) values(?,?,?)", project, project.ID, project.Name); err != nil {
				return err
			}
			for _, role := range []string{"router", "worker"} {
				agent := Agent{ID: role + "-" + projectID, ProjectID: projectID, Name: role, Role: role, EngineID: "fake", Cwd: "."}
				if err := save(ctx, tx, "insert into agent(id,project_id,role,body) values(?,?,?,?)", agent, agent.ID, projectID, role); err != nil {
					return err
				}
			}
		}
		for _, task := range snapshot.Tasks {
			for _, dependency := range task.Deps {
				if target, exists := taskByID[dependency]; !exists || target.ProjectID != task.ProjectID {
					return problem("conflict", "Legacy task dependency is missing or crosses projects")
				}
			}
			if err := insertTask(ctx, tx, task); err != nil {
				return err
			}
		}
		for _, run := range snapshot.Runs {
			task, ok := taskByID[run.TaskID]
			if !ok {
				return problem("conflict", "Legacy Run references a missing task")
			}
			run.ProjectID = task.ProjectID
			run.Purpose = "report"
			if !terminal(run.State) {
				result.InterruptedRuns++
				run.State = "failed"
				ended := now()
				run.EndedAt = &ended
				message := "Migration interrupted this attempt after the old service was stopped. Its external effects remain unknown; inspect them before retrying."
				run.Error = &message
			}
			if err := save(ctx, tx, "insert into run(id,project_id,task_id,state,body) values(?,?,?,?,?)", run, run.ID, run.ProjectID, run.TaskID, run.State); err != nil {
				return err
			}
			if !reported[run.ID] && run.Error != nil {
				result.GeneratedReports++
				report := Report{ID: id("report"), RunID: run.ID, State: "failed", Summary: *run.Error, Generated: true, Decisions: []string{}, Artifacts: []json.RawMessage{}, Blockers: []string{}, Usage: map[string]any{}}
				if err := save(ctx, tx, "insert into report(id,run_id,body) values(?,?,?)", report, report.ID, run.ID); err != nil {
					return err
				}
			}
		}
		for _, report := range snapshot.Reports {
			if _, ok := runByID[report.RunID]; !ok {
				return problem("conflict", "Legacy report references a missing Run")
			}
			if err := save(ctx, tx, "insert into report(id,run_id,body) values(?,?,?)", report, report.ID, report.RunID); err != nil {
				return err
			}
		}
		for _, approval := range snapshot.Approvals {
			task, ok := taskByID[approval.TaskID]
			if !ok || task.ProjectID != approval.ProjectID {
				return problem("conflict", "Legacy approval has no valid task association")
			}
			if err := insertApproval(ctx, tx, approval); err != nil {
				return err
			}
		}
		for _, event := range snapshot.Events {
			if err := save(ctx, tx, "insert into event(id,project_id,seq,body) values(?,?,?,?)", event, event.ID, event.ProjectID, event.Seq); err != nil {
				return err
			}
		}
		for _, original := range snapshot.Tasks {
			if original.ManualOverride {
				continue
			}
			previousStatus, previousUpdated := original.Status, original.UpdatedAt
			if err := refresh(ctx, tx, &original); err != nil {
				return err
			}
			if original.Status == previousStatus {
				original.UpdatedAt = previousUpdated
				if err := updateTask(ctx, tx, original); err != nil {
					return err
				}
			}
		}
		for projectID := range projects {
			if err := emit(ctx, tx, projectID, "migration.completed", "system:migration", map[string]any{"fingerprint": result.Fingerprint, "backup": backup}); err != nil {
				return err
			}
		}
		result.Projects, result.Tasks, result.Runs, result.Reports, result.Approvals, result.Events = len(projects), len(snapshot.Tasks), len(snapshot.Runs), len(snapshot.Reports), len(snapshot.Approvals), len(snapshot.Events)
		return save(ctx, tx, "insert into migration(fingerprint,body) values(?,?)", result, result.Fingerprint)
	})
	return result, err
}

func readLegacy(ctx context.Context, db *sql.DB) (legacySnapshot, error) {
	data := legacySnapshot{Tasks: []Task{}, Runs: []Run{}, Reports: []Report{}, Approvals: []Approval{}, Events: []Event{}}
	rows, err := db.QueryContext(ctx, "select id,project_id,title,description,status,kind,assignee_kind,assignee_id,deps_json,manual_override,plan_approved,created_at,updated_at from task order by rowid")
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var t Task
		var assignee sql.NullString
		var deps string
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Status, &t.Kind, &t.AssigneeKind, &assignee, &deps, &t.ManualOverride, &t.PlanApproved, &t.CreatedAt, &t.UpdatedAt); err != nil {
			rows.Close()
			return data, err
		}
		if assignee.Valid {
			t.AssigneeID = &assignee.String
		}
		if err := json.Unmarshal([]byte(deps), &t.Deps); err != nil {
			rows.Close()
			return data, err
		}
		data.Tasks = append(data.Tasks, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, "select id,task_id,agent_id,node_id,originator_id,state,started_at,ended_at,error from run order by rowid")
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.TaskID, &r.AgentID, &r.NodeID, &r.OriginatorID, &r.State, &r.StartedAt, &r.EndedAt, &r.Error); err != nil {
			rows.Close()
			return data, err
		}
		data.Runs = append(data.Runs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, "select id,run_id,state,summary,decisions_json,artifacts_json,blockers_json,usage_json,generated from report order by rowid")
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var r Report
		var decisions, artifacts, blockers, usage string
		if err := rows.Scan(&r.ID, &r.RunID, &r.State, &r.Summary, &decisions, &artifacts, &blockers, &usage, &r.Generated); err != nil {
			rows.Close()
			return data, err
		}
		for _, field := range []struct {
			raw    string
			target any
		}{{decisions, &r.Decisions}, {artifacts, &r.Artifacts}, {blockers, &r.Blockers}, {usage, &r.Usage}} {
			if err := json.Unmarshal([]byte(field.raw), field.target); err != nil {
				rows.Close()
				return data, err
			}
		}
		data.Reports = append(data.Reports, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, "select id,project_id,task_id,kind,state,payload_json,requested_at,decided_at,decided_by from approval order by rowid")
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var a Approval
		var taskID sql.NullString
		var payload []byte
		if err := rows.Scan(&a.ID, &a.ProjectID, &taskID, &a.Kind, &a.State, &payload, &a.RequestedAt, &a.DecidedAt, &a.DecidedBy); err != nil {
			rows.Close()
			return data, err
		}
		a.TaskID = taskID.String
		if !json.Valid(payload) {
			rows.Close()
			return data, fmt.Errorf("invalid approval payload")
		}
		a.Payload = payload
		data.Approvals = append(data.Approvals, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, "select id,project_id,seq,type,actor,payload_json,created_at from event order by project_id,seq")
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var e Event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Seq, &e.Type, &e.Actor, &payload, &e.CreatedAt); err != nil {
			rows.Close()
			return data, err
		}
		if !json.Valid(payload) {
			rows.Close()
			return data, fmt.Errorf("invalid event payload")
		}
		e.Payload = payload
		data.Events = append(data.Events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	return data, nil
}
