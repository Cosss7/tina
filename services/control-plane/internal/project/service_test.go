package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

var testCtx = context.Background()

const goodPlan = `{"summary":"交付功能","cards":[{"key":"c1","title":"实现","description":"实现功能","acceptance":["验证通过"],"deps":[]}]}`
const goodReport = `{"state":"done","summary":"实现完成","decisions":["复用已有接口"],"artifacts":[{"path":"src/main.go"}],"blockers":[],"usage":{"inputTokens":12}}`

func openTest(t *testing.T, path string) *Service {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func setup(t *testing.T, s *Service, projectID string) Task {
	t.Helper()
	if _, err := s.CreateProject(testCtx, projectID, "项目", "human"); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateDemand(testCtx, projectID, "需求", "描述", "human")
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func planned(t *testing.T, s *Service, projectID string) Task {
	t.Helper()
	demand := setup(t, s, projectID)
	q, err := s.RequestPlan(testCtx, projectID, demand.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, q.Run.ID, goodPlan, ""); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(testCtx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.DecidePlan(testCtx, projectID, b.Approvals[0].ID, "approved", "human")
	if err != nil {
		t.Fatal(err)
	}
	return d.Tasks[0]
}
func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestProjectQueueAndApprovalsSurviveRestartWithoutNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目 with space #", "control.db")
	t.Logf("database path: %s", path)
	s := openTest(t, path)
	demand := setup(t, s, "p")
	q, err := s.RequestPlan(testCtx, "p", demand.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if q.Run.State != "queued" || q.Run.StartedAt != nil || q.Run.NodeID != "" {
		t.Fatalf("unstarted request lied about execution: %+v", q.Run)
	}
	before, err := s.Queue(testCtx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	after, err := s.Queue(testCtx, "p")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("queue changed across restart: %+v %v", after, err)
	}
	if err := s.RecordOutcome(testCtx, q.Run.ID, "```json\n"+goodPlan+"\n```", ""); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(testCtx, "p")
	if err != nil || len(b.Approvals) != 1 {
		t.Fatalf("plan missing: %+v %v", b, err)
	}
	approvalID := b.Approvals[0].ID
	s.Close()
	s = openTest(t, path)
	d, err := s.DecidePlan(testCtx, "p", approvalID, "approved", "human")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.Dispatch(testCtx, "p", d.Tasks[0].ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if worker.Run.State != "queued" || worker.Task.Status != "doing" || worker.Run.StartedAt != nil {
		t.Fatal(worker)
	}
	if err := s.RecordOutcome(testCtx, worker.Run.ID, goodReport, ""); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, path)
	board, err := s.Board(testCtx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if board.Cards[1].Status != "review" || board.Cards[1].LatestReport.Summary != "实现完成" {
		t.Fatal(board)
	}
	final, err := s.Review(testCtx, "p", worker.Task.ID, "approved", "验收通过", "human")
	if err != nil || final.Status != "done" {
		t.Fatalf("review: %+v %v", final, err)
	}
	events, err := s.Events(testCtx, "p")
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatal("event sequence has gaps", events)
		}
	}
	if jobs, err := s.Queue(testCtx, "p"); err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
}

func TestBusinessAndEventsRollbackTogether(t *testing.T) {
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	_, err := s.db.Exec(`create trigger reject_queue_event before insert on event when json_extract(new.body,'$.type')='run.queued' begin select raise(abort,'injected event failure'); end`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPlan(testCtx, "p", task.ID, "human"); err == nil {
		t.Fatal("ignored event write failure")
	}
	jobs, err := s.Queue(testCtx, "p")
	if err != nil || len(jobs) != 0 {
		t.Fatal("committed job without event", jobs, err)
	}
	b, err := s.Board(testCtx, "p")
	if err != nil || len(b.Cards[0].Runs) != 0 {
		t.Fatal("committed run without event", b, err)
	}
	if _, err := s.db.Exec("drop trigger reject_queue_event"); err != nil {
		t.Fatal(err)
	}
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, q.Run.ID, goodPlan, ""); err != nil {
		t.Fatal(err)
	}
	b, _ = s.Board(testCtx, "p")
	_, err = s.db.Exec(`create trigger reject_card_event before insert on event when json_extract(new.body,'$.type')='card.created' begin select raise(abort,'injected event failure'); end`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePlan(testCtx, "p", b.Approvals[0].ID, "approved", "human"); err == nil {
		t.Fatal("ignored child creation failure")
	}
	b, err = s.Board(testCtx, "p")
	if err != nil || len(b.Cards) != 1 || len(b.Approvals) != 1 || b.Approvals[0].State != "pending" {
		t.Fatal("partial plan approval committed", b, err)
	}
}

func TestConcurrentRequestsProduceOneDurableJob(t *testing.T) {
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RequestPlan(testCtx, "p", task.ID, "human"); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if code(err) != "conflict" {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d concurrent commands", success)
	}
	jobs, err := s.Queue(testCtx, "p")
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
}

func TestForwardDependenciesArePreservedAndEnforced(t *testing.T) {
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	plan := `{"summary":"先做后面的基础卡","cards":[{"key":"dependent","title":"联调","acceptance":["接口通过"],"deps":["base"]},{"key":"base","title":"基础","acceptance":["单元验证"],"deps":[]}]}`
	if err := s.RecordOutcome(testCtx, q.Run.ID, plan, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Board(testCtx, "p")
	d, err := s.DecidePlan(testCtx, "p", b.Approvals[0].ID, "approved", "human")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Tasks[0].Deps, []string{d.Tasks[1].ID}) {
		t.Fatal("forward dependency dropped", d.Tasks)
	}
	if _, err := s.Dispatch(testCtx, "p", d.Tasks[0].ID, "human"); code(err) != "conflict" {
		t.Fatal("unmet dependency dispatched", err)
	}
	base, err := s.Dispatch(testCtx, "p", d.Tasks[1].ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, base.Run.ID, goodReport, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch(testCtx, "p", d.Tasks[0].ID, "human"); code(err) != "conflict" {
		t.Fatal("unreviewed dependency dispatched", err)
	}
	if _, err := s.Review(testCtx, "p", base.Task.ID, "approved", "", "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch(testCtx, "p", d.Tasks[0].ID, "human"); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidPlanAndWorkerOutputStayVisible(t *testing.T) {
	for name, output := range map[string]string{"missing dependency": `{"cards":[{"key":"a","title":"a","acceptance":["yes"],"deps":["missing"]}]}`, "cycle": `{"cards":[{"key":"a","title":"a","acceptance":["yes"],"deps":["a"]}]}`, "invalid JSON": "not JSON"} {
		t.Run(name, func(t *testing.T) {
			s := openTest(t, ":memory:")
			task := setup(t, s, "p")
			q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOutcome(testCtx, q.Run.ID, output, ""); err != nil {
				t.Fatal(err)
			}
			b, _ := s.Board(testCtx, "p")
			if len(b.Approvals) != 0 || b.Cards[0].Runs[0].State != "failed" || !b.Cards[0].LatestReport.Generated {
				t.Fatal(b)
			}
		})
	}
	s := openTest(t, ":memory:")
	task := planned(t, s, "p")
	q, err := s.Dispatch(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, q.Run.ID, "broken report", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Board(testCtx, "p")
	if b.Cards[1].Status != "review" || !b.Cards[1].LatestReport.Generated {
		t.Fatal(b)
	}
	// A late result cannot overwrite a prior settled business attempt.
	if err := s.RecordOutcome(testCtx, q.Run.ID, goodReport, ""); err != nil {
		t.Fatal(err)
	}
	b, _ = s.Board(testCtx, "p")
	if !b.Cards[1].LatestReport.Generated {
		t.Fatal("late completion overwrote failure")
	}
}

func TestCrossProjectOperationsAndActiveReviewAreRejected(t *testing.T) {
	s := openTest(t, ":memory:")
	task := planned(t, s, "p")
	setup(t, s, "other")
	if _, err := s.Dispatch(testCtx, "other", task.ID, "human"); code(err) != "not_found" {
		t.Fatal(err)
	}
	q, err := s.Dispatch(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(testCtx, "p", task.ID, "approved", "", "human"); code(err) != "conflict" {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(testCtx, q.Run.ID, goodReport, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(testCtx, "other", task.ID, "approved", "", "human"); code(err) != "not_found" {
		t.Fatal(err)
	}
	if _, err := s.Review(testCtx, "p", task.ID, "invalid", "", "human"); code(err) != "invalid_argument" {
		t.Fatal(err)
	}
}

func TestAgentConfigurationAndProjectBootstrapPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTest(t, path)
	if err := s.SeedDemo(testCtx); err != nil {
		t.Fatal(err)
	}
	agents, err := s.Agents(testCtx, "proj-website")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.ConfigureAgent(testCtx, "proj-website", agents[0].ID, "协调者", "dsh", "node-remote", "C:/work", "human")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, path)
	if err := s.SeedDemo(testCtx); err != nil {
		t.Fatal(err)
	}
	agents, err = s.Agents(testCtx, "proj-website")
	if err != nil || !reflect.DeepEqual(agents[0], a) {
		t.Fatal(agents, err)
	}
	projects, _ := s.Projects(testCtx)
	if len(projects) != 2 {
		t.Fatal("bootstrap duplicated projects", projects)
	}
}

func TestUnsentQueueCanBindAnAgentButRunningConfigurationIsFrozen(t *testing.T) {
	s := openTest(t, ":memory:")
	task := setup(t, s, "p")
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigureAgent(testCtx, "p", q.Run.AgentID, "Router", "dsh", "node-new", "C:/work", "human"); err != nil {
		t.Fatal("unbound queued work could never be assigned", err)
	}
	err = s.change(testCtx, func(tx *sql.Tx) error {
		run := q.Run
		run.State = "running"
		return save(testCtx, tx, "update run set state=?1,body=?3 where id=?2", run, run.State, run.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigureAgent(testCtx, "p", q.Run.AgentID, "Router", "dsh", "other-node", "C:/work", "human"); code(err) != "conflict" {
		t.Fatal("running work changed execution binding", err)
	}
}

func TestExistingH0DatabaseIsRejectedWithoutChangingTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h0.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table task (id text primary key); insert into task values ('historical')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("silently reused H0 database")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id string
	if err := db.QueryRow("select id from task").Scan(&id); err != nil || id != "historical" {
		t.Fatal(id, err)
	}
	// Another application's schema version 1 is not a Tina database either.
	if _, err := db.Exec("pragma user_version=1"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("accepted a foreign database with a coincident schema version")
	}
}

func TestRestartDoesNotTurnRunningWorkIntoFreshQueuedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s := openTest(t, path)
	task := setup(t, s, "p")
	q, err := s.RequestPlan(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	err = s.change(testCtx, func(tx *sql.Tx) error {
		run := q.Run
		run.State = "running"
		if err := save(testCtx, tx, "update run set state=?1,body=?3 where id=?2", run, run.State, run.ID); err != nil {
			return err
		}
		job, err := one[Job](testCtx, tx, "select body from execution_request where run_id=?", run.ID)
		if err != nil {
			return err
		}
		job.State = "awaiting_reconciliation"
		return save(testCtx, tx, "update execution_request set state=?1,body=?3 where command_id=?2", job, job.State, job.CommandID)
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openTest(t, path)
	b, _ := s.Board(testCtx, "p")
	jobs, _ := s.Queue(testCtx, "p")
	if b.Cards[0].Runs[0].State != "running" || len(jobs) != 0 {
		t.Fatal("restart guessed a new execution", b, jobs)
	}
	if _, err := s.RequestPlan(testCtx, "p", task.ID, "human"); code(err) != "conflict" {
		t.Fatal(err)
	}
}

func TestReportIdentityCannotBeProvidedByModel(t *testing.T) {
	s := openTest(t, ":memory:")
	task := planned(t, s, "p")
	q, err := s.Dispatch(testCtx, "p", task.ID, "human")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(goodReport), &raw); err != nil {
		t.Fatal(err)
	}
	raw["id"], raw["runId"], raw["generated"] = "attacker", "other-run", true
	output, _ := json.Marshal(raw)
	if err := s.RecordOutcome(testCtx, q.Run.ID, string(output), ""); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Board(testCtx, "p")
	r := b.Cards[1].LatestReport
	if r.ID == "attacker" || r.RunID != q.Run.ID || r.Generated {
		t.Fatal("model controlled authoritative metadata", r)
	}
}
