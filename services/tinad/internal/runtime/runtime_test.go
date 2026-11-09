package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
	"github.com/Cosss7/tina/services/tinad/internal/engine/fake"
	"github.com/coder/websocket"
)

func testConfig(t *testing.T) Config {
	directory := t.TempDir()
	return Config{NodeID: "node", Name: "local", Database: filepath.Join(directory, "journal.db"), MaxConcurrent: 2, Roots: []string{directory}, Agents: []p.AgentRegistration{{ID: "agent", ProjectID: "project", Role: "worker", EngineID: "fake"}}}
}
func testRuntime(t *testing.T, config Config) *Runtime {
	t.Helper()
	registry := &engine.Registry{}
	driver := fake.NewScripted()
	if err := registry.Register(driver); err != nil {
		t.Fatal(err)
	}
	r, err := New(config, registry, []p.EngineRegistration{{ID: "fake", Capabilities: driver.Definition().Capabilities}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func testDispatch(config Config) p.Dispatch {
	return p.Dispatch{CommandID: "command", Execution: p.Execution{ProjectID: "project", RunID: "run", AgentID: "agent", NodeID: config.NodeID, SessionID: "session", ActivityID: "activity"}, Config: p.SessionConfig{EngineID: "fake", Cwd: config.Roots[0]}, Input: "[TINA:REPORT]"}
}
func waitRecord(t *testing.T, r *Runtime, predicate func(record) bool) record {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		r.mu.Lock()
		rec, exists, err := r.journal.get("command")
		r.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if exists && predicate(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution did not reach expected state: %+v", rec)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDurableReplayAndRequestContextDoNotRepeatExecution(t *testing.T) {
	config := testConfig(t)
	r := testRuntime(t, config)
	d := testDispatch(config)
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Handle(ctx, &d); err != nil {
		t.Fatal(err)
	}
	cancel()
	rec := waitRecord(t, r, func(r record) bool { return r.Result != nil })
	if rec.Result.State != p.Completed {
		t.Fatal(rec)
	}
	if err := r.Handle(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = testRuntime(t, config)
	defer r.Close()
	if err := r.Handle(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	stored, _, err := r.journal.get(d.CommandID)
	if err != nil || stored.Result == nil || stored.Result.InstanceID != rec.Result.InstanceID {
		t.Fatal("replay created another execution", stored, err)
	}
	d.Input = "different work"
	if err := r.Handle(context.Background(), &d); err == nil {
		t.Fatal("accepted command identity conflict")
	}
	frames, err := r.journal.pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range frames {
		frame, err := p.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Handle(context.Background(), &p.Ack{NodeID: config.NodeID, MessageID: frame.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if frames, err := r.journal.pending(); err != nil || len(frames) != 0 {
		t.Fatal(frames, err)
	}
}
func TestInterruptedAdmissionBecomesUnknownInsteadOfBeingExecuted(t *testing.T) {
	config := testConfig(t)
	r := testRuntime(t, config)
	d := testDispatch(config)
	encoded, _ := p.Encode("command", d)
	receipt := p.Receipt{CommandID: d.CommandID, Execution: d.Execution, Admission: p.Admitted}
	if err := r.journal.put(record{CommandID: d.CommandID, Execution: d.Execution, Fingerprint: string(encoded), Receipt: receipt}, receipt); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r = testRuntime(t, config)
	defer r.Close()
	if err := r.Handle(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	rec, _, err := r.journal.get(d.CommandID)
	if err != nil || rec.Receipt.Admission != p.Unknown || rec.Result != nil {
		t.Fatal("restart fabricated an outcome", rec, err)
	}
	if len(r.sessions) != 0 {
		t.Fatal("uncertain command executed again")
	}
}
func TestLocalPolicyRejectsWrongAgentAndEscapingDirectory(t *testing.T) {
	for _, kind := range []string{"agent", "directory"} {
		t.Run(kind, func(t *testing.T) {
			config := testConfig(t)
			r := testRuntime(t, config)
			defer r.Close()
			d := testDispatch(config)
			if kind == "agent" {
				d.Execution.AgentID = "not-granted"
			} else {
				d.Config.Cwd = t.TempDir()
			}
			if err := r.Handle(context.Background(), &d); err != nil {
				t.Fatal(err)
			}
			rec := waitRecord(t, r, func(r record) bool { return r.Receipt.Admission == p.Rejected })
			if rec.Receipt.Failure.Code != "local_policy" || len(r.sessions) != 0 {
				t.Fatal(rec)
			}
		})
	}
}
func TestUnacknowledgedResultIsReplayedAfterDisconnect(t *testing.T) {
	config := testConfig(t)
	var connections atomic.Int32
	resultIDs := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, err := websocket.Accept(w, request, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		generation := connections.Add(1)
		_, data, err := conn.Read(request.Context())
		if err != nil {
			return
		}
		frame, err := p.Decode(data)
		if err != nil {
			return
		}
		if _, ok := frame.Message.(*p.Register); !ok {
			return
		}
		if generation == 1 {
			dispatch := testDispatch(config)
			data, _ = p.Encode("dispatch", dispatch)
			if conn.Write(request.Context(), websocket.MessageText, data) != nil {
				return
			}
		}
		for {
			_, data, err = conn.Read(request.Context())
			if err != nil {
				return
			}
			frame, err = p.Decode(data)
			if err != nil {
				return
			}
			if _, ok := frame.Message.(*p.Result); ok {
				resultIDs <- frame.ID
				if generation == 1 {
					return
				}
			}
			if _, ok := frame.Message.(*p.Heartbeat); ok {
				continue
			}
			ack, _ := p.Encode("ack", p.Ack{NodeID: config.NodeID, MessageID: frame.ID})
			if conn.Write(request.Context(), websocket.MessageText, ack) != nil {
				return
			}
		}
	}))
	defer server.Close()
	config.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	config.Token = "secret"
	r := testRuntime(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() { cancel(); <-done; r.Close() }()
	var first, second string
	select {
	case first = <-resultIDs:
	case <-time.After(5 * time.Second):
		t.Fatal("missing first result")
	}
	select {
	case second = <-resultIDs:
	case <-time.After(5 * time.Second):
		t.Fatal("missing replay")
	}
	if first != second || connections.Load() < 2 {
		t.Fatal("result identity changed on reconnect", first, second)
	}
}
