package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

// A real subprocess implementing the documented native transport. Persistence
// intentionally uses a file so Resume cannot succeed by inventing a new ID.
func TestSDKFixture(t *testing.T) {
	fixture := ""
	for i, arg := range os.Args {
		if arg == "--sdk-fixture" && i+1 < len(os.Args) {
			fixture = os.Args[i+1]
		}
	}
	if fixture == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID     string `json:"sessionId"`
				ContentBlocks []struct {
					Text string `json:"text"`
				} `json:"contentBlocks"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}
		nativePath := filepath.Join(fixture, request.Params.SessionID+".history")
		switch request.Method {
		case "initialize":
		case "session/resume":
			if _, err := os.Stat(nativePath); err != nil {
				delete(response, "result")
				response["error"] = map[string]any{"message": "history not found"}
			}
		case "session/prompt":
			os.WriteFile(nativePath, []byte("durable native history"), 0600)
			response["result"] = map[string]any{"messageId": "accepted-native-message"}
			encoder.Encode(response)
			data := func(kind string, body any) {
				encoder.Encode(map[string]any{"method": "session.event", "params": map[string]any{"sessionId": request.Params.SessionID, "event": map[string]any{"type": kind, "data": body}}})
			}
			data("turn/start", map[string]any{})
			if len(request.Params.ContentBlocks) > 0 && request.Params.ContentBlocks[0].Text == "hang" {
				continue
			}
			data("assistant/message", map[string]any{"message": map[string]any{"content": []map[string]string{{"type": "text", "text": "native answer"}}}})
			data("turn/end", map[string]any{"reason": map[string]string{"kind": "completed"}})
			continue
		}
		encoder.Encode(response)
	}
	os.Exit(0)
}
func TestNativeSessionIdentityResumeAndProcessCancel(t *testing.T) {
	directory := t.TempDir()
	driver := &Driver{Options: Options{Command: []string{os.Args[0], "-test.run=TestSDKFixture", "--", "--sdk-fixture", directory}, Home: directory}}
	var registry engine.Registry
	if err := registry.Register(driver); err != nil {
		t.Fatal(err)
	}
	options := engine.OpenOptions{SessionID: "logical-session", Config: p.SessionConfig{EngineID: "dsh", Cwd: directory}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	session, err := registry.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	in := p.Input{CommandID: "command-1", Execution: p.Execution{ProjectID: "p", RunID: "r", AgentID: "a", NodeID: "n", SessionID: options.SessionID, ActivityID: "first"}, InstanceID: session.Info().InstanceID, Text: "normal"}
	receipt, err := session.Submit(ctx, in)
	if err != nil || receipt.Admission != p.Accepted {
		t.Fatal(receipt, err)
	}
	var terminal *p.Result
	for terminal == nil {
		select {
		case update := <-session.Updates():
			if err := update.Validate(); err != nil {
				t.Fatal(err)
			}
			terminal = update.Result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if terminal.State != p.Completed || terminal.Output != "native answer" || terminal.Handle == nil {
		t.Fatal(terminal)
	}
	handle := *terminal.Handle
	if _, err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, err := registry.Resume(ctx, handle, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resumed.Close(cleanup)
	})
	if resumed.Info().InstanceID == in.InstanceID {
		t.Fatal("resume reused a runtime identity")
	}
	in.CommandID, in.InstanceID, in.Text = "command-2", resumed.Info().InstanceID, "hang"
	in.Execution.ActivityID = "second"
	if _, err := resumed.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resumed.Updates():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	proof, err := resumed.(engine.Interrupter).Interrupt(ctx, "second")
	if err != nil || !proof.ConfirmsStop() || proof.SessionUsable {
		t.Fatal(proof, err)
	}
	terminal = nil
	for terminal == nil {
		select {
		case update := <-resumed.Updates():
			terminal = update.Result
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if terminal.State != p.Cancelled {
		t.Fatalf("process cancel became %+v", terminal)
	}
	missing := handle
	missing.NativeID = "missing-history"
	if _, err := registry.Resume(ctx, missing, options); err == nil {
		t.Fatal("missing history silently became a new session")
	}
}
