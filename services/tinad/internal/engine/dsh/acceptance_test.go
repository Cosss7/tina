package dsh

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

// Explicitly opt in: this contacts a real model and creates native session history.
func TestRealDSHAcceptance(t *testing.T) {
	filename := os.Getenv("TINA_DSH_ACCEPTANCE_CONFIG")
	if filename == "" {
		t.Skip("set TINA_DSH_ACCEPTANCE_CONFIG for real-model acceptance")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var options Options
	if err := json.Unmarshal(data, &options); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	registry := &engine.Registry{}
	if err := registry.Register(&Driver{Options: options}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	open := engine.OpenOptions{SessionID: "acceptance-" + rand.Text(), Config: p.SessionConfig{EngineID: "dsh", Cwd: cwd}}
	session, err := registry.Open(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if session == nil {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		session.Close(c)
	})
	nonce := rand.Text()
	submit := func(text, activity string) {
		t.Helper()
		input := p.Input{CommandID: activity, Execution: p.Execution{ProjectID: "acceptance", RunID: activity, AgentID: "worker", NodeID: "local", SessionID: open.SessionID, ActivityID: activity}, InstanceID: session.Info().InstanceID, Text: text}
		receipt, err := session.Submit(ctx, input)
		if err != nil || receipt.Admission != p.Accepted {
			t.Fatalf("submit admission=%s failure=%+v error=%v", receipt.Admission, receipt.Failure, err)
		}
	}
	result := func() *p.Result {
		t.Helper()
		for {
			select {
			case update, ok := <-session.Updates():
				if !ok {
					t.Fatal("native stream closed without terminal")
				}
				if update.Result != nil {
					return update.Result
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	submit("Remember this exact verification token: "+nonce+". Return ONLY JSON with state done, summary equal to that token, decisions [], artifacts [], blockers []. Do not use tools.", "first")
	first := result()
	if first.State != p.Completed || !strings.Contains(first.Output, nonce) || first.Handle == nil {
		t.Fatalf("initial response state=%s output=%q failure=%+v", first.State, first.Output, first.Failure)
	}
	var report struct{ State, Summary string }
	if json.Unmarshal([]byte(strings.Trim(strings.TrimSpace(first.Output), "`")), &report) != nil {
		// Model code fences are accepted by the product parser, too.
		start, end := strings.Index(first.Output, "{"), strings.LastIndex(first.Output, "}")
		if start < 0 || end < start || json.Unmarshal([]byte(first.Output[start:end+1]), &report) != nil {
			t.Fatal("not a structured report")
		}
	}
	if report.State != "done" || report.Summary != nonce {
		t.Fatal("report does not preserve the requested fact")
	}
	handle, instance := *first.Handle, session.Info().InstanceID
	proof, err := session.Close(ctx)
	if err != nil || !proof.ConfirmsStop() {
		t.Fatal(proof, err)
	}
	legacy := os.Getenv("TINA_DSH_ACCEPTANCE_EXPECT_NO_RESUME") == "1"
	session, err = registry.Resume(ctx, handle, open)
	if legacy {
		if !errors.Is(err, engine.ErrUnsupported) || handle.Metadata["resume"] != "false" {
			t.Fatalf("legacy SDK claimed resume: %v %+v", err, handle)
		}
		open.SessionID += "-cancel"
		session, err = registry.Open(ctx, open)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if session.Info().InstanceID == instance || session.Info().Handle.NativeID != handle.NativeID {
			t.Fatal("resume did not retain native identity across process release")
		}
		submit("What was the verification token in our previous message? Return the exact token only. Do not use tools.", "second")
		second := result()
		if second.State != p.Completed || !strings.Contains(second.Output, nonce) {
			t.Fatalf("native history not recalled: %+v", second)
		}
	}
	marker := filepath.Join(cwd, ".tina-native-cancel-"+rand.Text())
	tools := os.Getenv("TINA_DSH_ACCEPTANCE_TOOLS") == "1"
	workloadPID := 0
	if tools {
		t.Cleanup(func() { os.Remove(marker) })
		submit("Use your terminal tool to execute exactly this foreground PowerShell command: Set-Content -LiteralPath '"+marker+"' -Value $PID; Start-Sleep -Seconds 60 . Do not background it. Do not change any other files.", "cancel")
		deadline := time.Now().Add(45 * time.Second)
		for {
			if data, err := os.ReadFile(marker); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
					live, err := acceptanceProcessAlive(pid)
					if err != nil || !live {
						t.Fatalf("native tool workload was not live before cancel: pid=%d live=%t error=%v", pid, live, err)
					}
					workloadPID = pid
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("native tool never created the workload marker")
			}
			time.Sleep(50 * time.Millisecond)
		}
	} else {
		submit("Count from 1 to 100000, one number per line, without tools or explanation.", "cancel")
	}
	for {
		select {
		case update := <-session.Updates():
			if update.Result != nil {
				t.Fatal("activity ended before cancellation acceptance")
			}
			if update.Event != nil && update.Event.Kind == p.ActivityStarted {
				goto started
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
started:
	proof, err = session.(engine.Interrupter).Interrupt(ctx, "cancel")
	if err != nil || !proof.ConfirmsStop() || proof.SessionUsable {
		t.Fatal(proof, err)
	}
	cancelled := result()
	if cancelled.State != p.Cancelled {
		t.Fatalf("unexpected terminal: %+v", cancelled)
	}
	if workloadPID != 0 {
		live, err := acceptanceProcessAlive(workloadPID)
		if err != nil || live {
			t.Fatalf("native tool process survived cancellation: pid=%d live=%t error=%v", workloadPID, live, err)
		}
	}
	t.Logf("real dsh: structured report; legacy=%t nativeTool=%t; process cancellation root=%s workload=%s", legacy, tools, proof.Root, proof.Workload)
}
