package runtime

import (
	"context"
	"encoding/json"
	"testing"

	p "github.com/Cosss7/tina/packages/protocol"
)

func TestJournalOwnershipAndAuthorizationReloadDoNotRestartWork(t *testing.T) {
	config := testConfig(t)
	r := testRuntime(t, config)
	if duplicate, err := openJournal(config.Database); err == nil {
		duplicate.close()
		t.Fatal("two daemons own the same ledger")
	}
	dispatch := testDispatch(config)
	dispatch.Config.Options = map[string]json.RawMessage{"delayMs": json.RawMessage(`-1`)}
	if err := r.Handle(context.Background(), &dispatch); err != nil {
		t.Fatal(err)
	}
	before := waitRecord(t, r, func(rec record) bool { return rec.Started })
	// Removing a grant affects future inputs, not an already authorized native activity.
	if err := r.UpdateAuthorization(config.Roots, nil); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	current, _, err := r.journal.get(dispatch.CommandID)
	r.mu.Unlock()
	if err != nil || current.Result != nil || current.Receipt.InstanceID != before.Receipt.InstanceID {
		t.Fatal(current, err)
	}
	next := dispatch
	next.CommandID = "unauthorized"
	next.Execution.RunID = "next"
	next.Execution.ActivityID = "next"
	next.Execution.SessionID = "next"
	if err := r.Handle(context.Background(), &next); err != nil {
		t.Fatal(err)
	}
	rejected := waitCommand(t, r, next.CommandID, func(rec record) bool { return rec.Receipt.Admission == p.Rejected })
	if rejected.Receipt.Failure.Code != "local_policy" {
		t.Fatal(rejected)
	}
	if err := r.Quiesce(); err != nil {
		t.Fatal(err)
	}
	stopped := waitRecord(t, r, func(rec record) bool { return rec.Result != nil })
	if stopped.Result.State != p.Cancelled || !stopped.Result.Stop.ConfirmsStop() {
		t.Fatal(stopped)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := openJournal(config.Database)
	if err != nil {
		t.Fatal(err)
	}
	journal.close()
}
