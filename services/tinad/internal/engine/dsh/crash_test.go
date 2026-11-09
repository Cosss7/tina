package dsh

import (
	"context"
	"os"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

func TestNativeCrashIsFailureWithExitEvidence(t *testing.T) {
	directory := t.TempDir()
	driver := &Driver{Options: Options{Command: []string{os.Args[0], "-test.run=TestSDKFixture", "--", "--sdk-fixture", directory}, Home: directory}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := driver.Create(ctx, engine.OpenOptions{SessionID: "logical", Config: p.SessionConfig{EngineID: "dsh", Cwd: directory}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(ctx)
	in := p.Input{CommandID: "command", Execution: p.Execution{ProjectID: "p", RunID: "r", AgentID: "a", NodeID: "n", SessionID: "logical", ActivityID: "activity"}, InstanceID: session.Info().InstanceID, Text: "crash"}
	if _, err := session.Submit(ctx, in); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case update := <-session.Updates():
			if update.Result != nil {
				if update.Result.State != p.Failed || update.Result.Failure.Code != "engine_exited" || update.Result.Stop.Root != p.ExitExited {
					t.Fatal(update.Result)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
