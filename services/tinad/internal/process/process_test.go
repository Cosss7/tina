package process

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessFixture(t *testing.T) {
	if os.Getenv("TINA_PROCESS_FIXTURE") != "1" {
		return
	}
	if os.Getenv("TINA_PROCESS_CHILD") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=TestProcessFixture")
	child.Env = append(os.Environ(), "TINA_PROCESS_CHILD=1")
	if err := child.Start(); err != nil {
		os.Exit(10)
	}
	data, _ := json.Marshal(map[string]int{"root": os.Getpid(), "child": child.Process.Pid})
	if err := os.WriteFile(os.Getenv("TINA_PROCESS_READY"), data, 0600); err != nil {
		os.Exit(11)
	}
	child.Wait()
	os.Exit(0)
}
func TestStopConfirmsRootAndChildWorkloadExit(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready.json")
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessFixture")
	cmd.Env = append(os.Environ(), "TINA_PROCESS_FIXTURE=1", "TINA_PROCESS_READY="+ready)
	scope, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		scope.Stop(ctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not spawn child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if scope.Evidence().ConfirmsStop() {
		t.Fatal("live workload appeared stopped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	proof, err := scope.Stop(ctx)
	if err != nil || !proof.ConfirmsStop() {
		t.Fatalf("root and child cleanup: %+v %v", proof, err)
	}
}
