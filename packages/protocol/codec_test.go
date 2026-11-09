package protocol_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
)

func execution() p.Execution {
	return p.Execution{ProjectID: "project-1", RunID: "run-1", AgentID: "worker-1",
		NodeID: "node-1", SessionID: "session-1", ActivityID: "activity-1"}
}

func result() p.Result {
	return p.Result{Execution: execution(), InstanceID: "instance-1", CommandID: "command-1",
		Seq: 2, OccurredAt: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), State: p.Completed, Output: "done"}
}

func TestMessageRoundTrips(t *testing.T) {
	r := result()
	messages := []p.Message{
		p.Register{NodeID: "node-1", Name: "laptop", Version: "dev", MaxConcurrent: 2,
			Engines: []p.EngineRegistration{{ID: "fake", Capabilities: p.Capabilities{Interrupt: p.InterruptNone}}},
			Agents: []p.AgentRegistration{{ID: "router-1", ProjectID: "project-1", Role: "router", EngineID: "fake"},
				{ID: "worker-1", ProjectID: "project-1", Role: "worker", EngineID: "fake"}}},
		p.Heartbeat{NodeID: "node-1"},
		p.Dispatch{CommandID: "command-1", Execution: execution(), Config: p.SessionConfig{EngineID: "fake", Cwd: "/work"}, Input: "work"},
		p.Input{CommandID: "command-2", Execution: execution(), InstanceID: "instance-1", Text: "continue"},
		p.Cancel{CommandID: "cancel-1", Execution: execution(), Reason: "user requested"},
		p.Respond{CommandID: "respond-1", Execution: execution(), InstanceID: "instance-1", RequestID: "question-1", Answer: json.RawMessage(`{"allow":true}`)},
		p.Receipt{CommandID: "command-1", Execution: execution(), Admission: p.Unknown, Failure: &p.Failure{Code: "transport_lost", Message: "acceptance is unproven"}},
		p.Event{Execution: execution(), InstanceID: "instance-1", CommandID: "command-1", Seq: 1, OccurredAt: r.OccurredAt,
			Kind: p.ToolCall, Tool: &p.Tool{CallID: "tool-1", Name: "read", Input: json.RawMessage(`{"path":"a.txt"}`)}},
		r,
		p.Ack{NodeID: "node-1", MessageID: "result-1"},
		p.ReconcileRequest{NodeID: "node-1", Known: []p.Cursor{{Execution: execution(), InstanceID: "instance-1", LastSeq: 1}}},
		p.ReconcileResponse{NodeID: "node-1", Observations: []p.Observation{{Cursor: p.Cursor{Execution: execution(), InstanceID: "instance-1", LastSeq: 2}, State: "completed", Result: &r}}},
	}
	for _, message := range messages {
		t.Run(reflect.TypeOf(message).Name(), func(t *testing.T) {
			encoded, err := p.Encode("frame-1", message)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := p.Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ID != "frame-1" {
				t.Fatalf("lost frame identity: %s", frame.ID)
			}
			again, err := p.Encode(frame.ID, frame.Message)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(encoded) {
				t.Fatalf("round trip changed message:\n%s\n%s", encoded, again)
			}
		})
	}
}

func TestLanguageNeutralFlowFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/flow.json")
	if err != nil {
		t.Fatal(err)
	}
	var frames []json.RawMessage
	if err := json.Unmarshal(data, &frames); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 {
		t.Fatalf("expected registration and two dispatch/result pairs, got %d", len(frames))
	}
	for _, data := range frames {
		frame, err := p.Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if _, err := p.Encode(frame.ID, frame.Message); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecodeRejectsMalformedOrUnsupportedFrames(t *testing.T) {
	valid := `{"version":1,"id":"f","type":"heartbeat","payload":{"nodeId":"n"}}`
	cases := map[string]string{
		"version":                `{"version":2,"id":"f","type":"heartbeat","payload":{"nodeId":"n"}}`,
		"business RPC":           `{"version":1,"id":"f","type":"task.create","payload":{}}`,
		"unknown envelope field": `{"version":1,"id":"f","type":"heartbeat","payload":{"nodeId":"n"},"secret":true}`,
		"unknown payload field":  `{"version":1,"id":"f","type":"heartbeat","payload":{"nodeId":"n","homeNodeId":"n"}}`,
		"null payload":           `{"version":1,"id":"f","type":"heartbeat","payload":null}`,
		"missing payload":        `{"version":1,"id":"f","type":"heartbeat"}`,
		"missing identity":       `{"version":1,"type":"heartbeat","payload":{"nodeId":"n"}}`,
		"trailing value":         valid + `{}`,
		"array":                  `[]`,
		"empty":                  ``,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Decode([]byte(data)); err == nil {
				t.Fatal("accepted invalid frame")
			}
		})
	}
	var typedNil *p.Heartbeat
	if _, err := p.Encode("f", typedNil); err == nil {
		t.Fatal("accepted typed nil")
	}
	if _, err := p.Encode("", p.Heartbeat{NodeID: "n"}); err == nil {
		t.Fatal("accepted empty identity")
	}
}

func TestTerminalCancellationRequiresWorkloadProof(t *testing.T) {
	for _, mode := range []p.InterruptMode{p.InterruptTurn, p.InterruptProcess} {
		for _, workload := range []p.ExitStatus{p.ExitLive, p.ExitUnproven, p.ExitExited, p.ExitNotApplicable} {
			for _, root := range []p.ExitStatus{p.ExitLive, p.ExitUnproven, p.ExitExited, p.ExitNotApplicable} {
				r := result()
				r.State = p.Cancelled
				r.Stop = &p.StopEvidence{Mode: mode, Requested: true, ActivityStopped: true, Root: root, Workload: workload}
				want := (workload == p.ExitExited || workload == p.ExitNotApplicable) &&
					(mode == p.InterruptTurn || root == p.ExitExited || root == p.ExitNotApplicable)
				if got := r.Validate() == nil; got != want {
					t.Fatalf("mode=%s root=%s workload=%s: valid=%v, want=%v", mode, root, workload, got, want)
				}
			}
		}
	}
	r := result()
	r.State = p.Cancelled
	if r.Validate() == nil {
		t.Fatal("cancellation without proof was accepted")
	}
	// Cleanup uncertainty is a separate fact and must not erase a native completion.
	r.State = p.Completed
	r.Stop = &p.StopEvidence{Mode: p.InterruptProcess, Root: p.ExitUnproven, Workload: p.ExitUnproven}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCrossIdentityAndStateValidation(t *testing.T) {
	r := result()
	cases := map[string]p.Message{
		"accepted without instance": p.Receipt{CommandID: "c", Execution: execution(), Admission: p.Accepted},
		"unknown without reason":    p.Receipt{CommandID: "c", Execution: execution(), Admission: p.Unknown},
		"resume another session": p.Dispatch{CommandID: "c", Execution: execution(), Config: p.SessionConfig{EngineID: "fake", Cwd: "/work"}, Input: "work",
			Resume: &p.SessionHandle{EngineID: "fake", SessionID: "another", NativeID: "native"}},
		"snapshot disagrees with result": p.ReconcileResponse{NodeID: "node-1", Observations: []p.Observation{{Cursor: p.Cursor{Execution: execution(), InstanceID: "instance-1", LastSeq: 2}, State: "cancelled", Result: &r}}},
		"snapshot from another node":     p.ReconcileRequest{NodeID: "another", Known: []p.Cursor{{Execution: execution()}}},
		"running without instance":       p.ReconcileResponse{NodeID: "node-1", Observations: []p.Observation{{Cursor: p.Cursor{Execution: execution()}, State: "running"}}},
		"unproven terminal snapshot":     p.ReconcileResponse{NodeID: "node-1", Observations: []p.Observation{{Cursor: p.Cursor{Execution: execution()}, State: "cancelled"}}},
		"event with mixed payloads":      p.Event{Execution: execution(), InstanceID: "i", CommandID: "c", Seq: 1, OccurredAt: r.OccurredAt, Kind: p.MessageText, Text: "hi", Usage: &p.Usage{}},
		"unknown registered engine":      p.Register{NodeID: "n", Name: "n", Version: "dev", MaxConcurrent: 1, Agents: []p.AgentRegistration{{ID: "a", ProjectID: "p", Role: "worker", EngineID: "missing"}}},
	}
	for name, message := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Encode("f", message); err == nil {
				t.Fatal("accepted invalid message")
			}
		})
	}
	unknown := p.ReconcileResponse{NodeID: "node-1", Observations: []p.Observation{{Cursor: p.Cursor{Execution: execution()}, State: "unknown"}}}
	if err := unknown.Validate(); err != nil {
		t.Fatal(err)
	}
}
