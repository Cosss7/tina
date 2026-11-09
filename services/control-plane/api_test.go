package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Cosss7/tina/services/control-plane/internal/project"
	"github.com/coder/websocket"
)

func testAPI(t *testing.T) (*project.Service, *httptest.Server) {
	t.Helper()
	s, err := project.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	server := httptest.NewServer(newHandler(s, NewHub(), fstest.MapFS{"index.html": {Data: []byte("test")}}))
	t.Cleanup(server.Close)
	return s, server
}
func call(t *testing.T, server *httptest.Server, method, path, body string, status int, target any) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: status=%d wanted=%d body=%s", method, path, response.StatusCode, status, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectAPIRemainsAvailableWithoutAnyNode(t *testing.T) {
	store, server := testAPI(t)
	var p project.Project
	call(t, server, "POST", "/api/projects", `{"name":"离线项目"}`, 201, &p)
	var agents []project.Agent
	call(t, server, "GET", "/api/projects/"+p.ID+"/agents", "", 200, &agents)
	if len(agents) != 2 || agents[0].NodeID != "" {
		t.Fatal(agents)
	}
	var task project.Task
	call(t, server, "POST", "/api/cards", `{"projectId":"`+p.ID+`","title":"需求"}`, 201, &task)
	var queued project.Queued
	call(t, server, "POST", "/api/plans", `{"projectId":"`+p.ID+`","taskId":"`+task.ID+`"}`, 202, &queued)
	if queued.Run.State != "queued" || queued.Run.StartedAt != nil {
		t.Fatal(queued)
	}
	call(t, server, "POST", "/api/plans", `{"projectId":"`+p.ID+`","taskId":"`+task.ID+`"}`, 409, nil)
	var jobs []project.Job
	call(t, server, "GET", "/api/queue?project="+p.ID, "", 200, &jobs)
	if len(jobs) != 1 || jobs[0].CommandID != queued.Run.CommandID {
		t.Fatal(jobs)
	}
	// Supply an observed outcome through the trusted internal boundary. The public
	// API must have no unauthenticated endpoint that can manufacture engine results.
	plan := `{"summary":"计划","cards":[{"key":"c1","title":"任务","acceptance":["验收通过"],"deps":[]}]}`
	if err := store.RecordOutcome(context.Background(), queued.Run.ID, plan, ""); err != nil {
		t.Fatal(err)
	}
	var board project.Board
	call(t, server, "GET", "/api/board?project="+p.ID, "", 200, &board)
	var decision project.Decision
	call(t, server, "POST", "/api/plans/"+board.Approvals[0].ID+"/decide", `{"projectId":"`+p.ID+`","decision":"approved"}`, 200, &decision)
	call(t, server, "POST", "/api/tasks/"+decision.Tasks[0].ID+"/dispatch", `{"projectId":"`+p.ID+`"}`, 202, &queued)
	if err := store.RecordOutcome(context.Background(), queued.Run.ID, `{"state":"blocked","summary":"缺少 API key","blockers":["权限不足"]}`, ""); err != nil {
		t.Fatal(err)
	}
	call(t, server, "POST", "/api/tasks/"+decision.Tasks[0].ID+"/review", `{"projectId":"`+p.ID+`","decision":"approved","note":"确认交付范围"}`, 200, &task)
	if task.Status != "done" {
		t.Fatal(task)
	}
}

func TestNodeRegistrationCannotCreateOrReassignProjects(t *testing.T) {
	store, server := testAPI(t)
	if err := store.SeedDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/node", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	message := `{"type":"register","nodeId":"untrusted","name":"old node","projects":[{"id":"injected","name":"injected"}]}`
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	// The project catalog is independent even if registration is still in flight.
	var state struct {
		Projects []map[string]any `json:"projects"`
	}
	call(t, server, "GET", "/api/state", "", 200, &state)
	if len(state.Projects) != 2 {
		t.Fatal(state)
	}
	for _, p := range state.Projects {
		if p["id"] == "injected" || p["homeNodeId"] != nil {
			t.Fatal(p)
		}
	}
	call(t, server, "GET", "/api/board?project=injected", "", 404, nil)
}

func TestAPIRejectsMalformedInputAndCrossProjectMutations(t *testing.T) {
	_, server := testAPI(t)
	for _, body := range []string{"null", "[]", "{} {}", `{"name":""}`, `{"name":"p"`} {
		call(t, server, "POST", "/api/projects", body, 400, nil)
	}
	var p, q project.Project
	call(t, server, "POST", "/api/projects", `{"name":"p"}`, 201, &p)
	call(t, server, "POST", "/api/projects", `{"name":"q"}`, 201, &q)
	var task project.Task
	call(t, server, "POST", "/api/cards", `{"projectId":"`+p.ID+`","title":"需求"}`, 201, &task)
	call(t, server, "POST", "/api/plans", `{"projectId":"`+q.ID+`","taskId":"`+task.ID+`"}`, 404, nil)
	call(t, server, "POST", "/api/tasks/"+task.ID+"/dispatch", `{"projectId":"`+q.ID+`"}`, 404, nil)
	call(t, server, "POST", "/api/cards", `{"projectId":"missing","title":"需求"}`, 404, nil)
	call(t, server, "POST", "/api/projects", `{"name":"`+strings.Repeat("x", 1<<20)+`"}`, 400, nil)
}
