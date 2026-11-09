package fake_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
	"github.com/Cosss7/tina/services/tinad/internal/engine/fake"
)

var ctx = context.Background()

func open(t *testing.T, d *fake.Driver, sessionID string) *fake.Session {
	t.Helper()
	var registry engine.Registry
	if err := registry.Register(d); err != nil {
		t.Fatal(err)
	}
	s, err := registry.Open(ctx, options(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s.(*fake.Session)
}

func options(id string) engine.OpenOptions {
	return engine.OpenOptions{SessionID: id, Config: p.SessionConfig{EngineID: "fake", Cwd: "/work"}}
}
func input(s *fake.Session, id string) p.Input {
	return p.Input{CommandID: "command-" + id, InstanceID: s.Info().InstanceID, Text: "prompt-" + id,
		Execution: p.Execution{ProjectID: "project-1", RunID: "run-" + id, AgentID: "agent-" + s.Info().SessionID,
			NodeID: "node-1", SessionID: s.Info().SessionID, ActivityID: "activity-" + id}}
}
func submit(t *testing.T, s *fake.Session, in p.Input) p.Receipt {
	t.Helper()
	r, err := s.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}
func update(t *testing.T, s *fake.Session) engine.Update {
	t.Helper()
	select {
	case u := <-s.Updates():
		if err := u.Validate(); err != nil {
			t.Fatal(err)
		}
		return u
	case <-time.After(time.Second):
		t.Fatal("missing execution update")
		return engine.Update{}
	}
}
func empty(t *testing.T, s *fake.Session) {
	t.Helper()
	select {
	case u := <-s.Updates():
		t.Fatalf("unexpected extra update: %+v", u)
	default:
	}
}

func TestLostReceiptReplayDoesNotExecuteAgain(t *testing.T) {
	d := fake.New()
	s := open(t, d, "session-1")
	in := input(s, "1")
	first := submit(t, s, in) // Pretend this receipt was lost in transit.
	if again := submit(t, s, in); !reflect.DeepEqual(first, again) {
		t.Fatal("duplicate command changed receipt")
	}
	if first.Admission != p.Accepted || update(t, s).Event.Kind != p.ActivityStarted {
		t.Fatal("input did not start")
	}
	empty(t, s)
	if err := s.Complete(in.Execution.ActivityID, "report"); err != nil {
		t.Fatal(err)
	}
	if update(t, s).Result.State != p.Completed {
		t.Fatal("missing completion")
	}
	submit(t, s, in)
	empty(t, s)
	if got := d.History(s.Info().Handle.NativeID); !reflect.DeepEqual(got, []string{in.Text, "report"}) {
		t.Fatal(got)
	}
	in.Text = "different work with same command ID"
	if _, err := s.Submit(ctx, in); !errors.Is(err, engine.ErrConflict) {
		t.Fatal(err)
	}
}

func TestSessionsAndLateCancellationAreIsolated(t *testing.T) {
	d := fake.New()
	a, b := open(t, d, "a"), open(t, d, "b")
	x, y := input(a, "1"), input(b, "2")
	submit(t, a, x)
	submit(t, b, y)
	update(t, a)
	update(t, b)
	if a.Info().Handle.NativeID == b.Info().Handle.NativeID {
		t.Fatal("sessions share native history")
	}
	stop, err := a.Interrupt(ctx, x.Execution.ActivityID)
	if err != nil || !stop.ConfirmsStop() || !stop.SessionUsable {
		t.Fatalf("interrupt: %+v %v", stop, err)
	}
	if update(t, a).Result.State != p.Cancelled {
		t.Fatal("target was not cancelled")
	}
	empty(t, b)
	next := input(a, "3")
	submit(t, a, next)
	update(t, a)
	if _, err := a.Interrupt(ctx, x.Execution.ActivityID); err != nil {
		t.Fatal(err)
	}
	empty(t, a)
	if err := a.Complete(next.Execution.ActivityID, "next result"); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete(y.Execution.ActivityID, "independent result"); err != nil {
		t.Fatal(err)
	}
	if update(t, a).Result.Execution != next.Execution || update(t, b).Result.Execution != y.Execution {
		t.Fatal("execution identities crossed")
	}
}

func TestBusyRejectionAndStaleInstance(t *testing.T) {
	s := open(t, fake.New(), "s")
	in := input(s, "1")
	submit(t, s, in)
	update(t, s)
	busy := input(s, "2")
	if submit(t, s, busy).Admission != p.Rejected {
		t.Fatal("concurrent activity was accepted")
	}
	if err := s.Complete(in.Execution.ActivityID, "done"); err != nil {
		t.Fatal(err)
	}
	update(t, s)
	if submit(t, s, busy).Admission != p.Rejected {
		t.Fatal("replaying rejected command started new execution")
	}
	stale := input(s, "3")
	stale.InstanceID = "old-process"
	if _, err := s.Submit(ctx, stale); !errors.Is(err, engine.ErrConflict) {
		t.Fatal(err)
	}
	empty(t, s)
}

func TestCloseEndsOnlyItsRuntimeAndDoesNotDeleteHistory(t *testing.T) {
	d := fake.New()
	a, b := open(t, d, "a"), open(t, d, "b")
	x, y := input(a, "1"), input(b, "2")
	submit(t, a, x)
	submit(t, b, y)
	update(t, a)
	update(t, b)
	stop, err := a.Close(ctx)
	if err != nil || !stop.ConfirmsStop() || stop.SessionUsable {
		t.Fatalf("close: %+v %v", stop, err)
	}
	r := update(t, a).Result
	if r.State != p.Cancelled || r.Stop.Mode != p.InterruptProcess {
		t.Fatalf("missing runtime close evidence: %+v", r)
	}
	if _, open := <-a.Updates(); open {
		t.Fatal("closed runtime kept its update stream open")
	}
	if _, err := a.Submit(ctx, input(a, "3")); !errors.Is(err, engine.ErrClosed) {
		t.Fatal(err)
	}
	if got := d.History(a.Info().Handle.NativeID); !reflect.DeepEqual(got, []string{x.Text}) {
		t.Fatal("runtime close deleted history", got)
	}
	if err := b.Complete(y.Execution.ActivityID, "unaffected"); err != nil {
		t.Fatal(err)
	}
	if update(t, b).Result.State != p.Completed {
		t.Fatal("another runtime was affected")
	}
}

func TestSubmitContextDoesNotOwnAcceptedActivity(t *testing.T) {
	s := open(t, fake.New(), "s")
	in := input(s, "1")
	requestCtx, cancel := context.WithCancel(ctx)
	r, err := s.Submit(requestCtx, in)
	if err != nil || r.Admission != p.Accepted {
		t.Fatalf("submit: %+v %v", r, err)
	}
	cancel()
	update(t, s)
	empty(t, s)
	if err := s.Complete(in.Execution.ActivityID, "accepted work continues"); err != nil {
		t.Fatal(err)
	}
	if update(t, s).Result.State != p.Completed {
		t.Fatal("request cancellation fabricated an activity cancellation")
	}
}

func TestClosePreservesHistoryAndResumeRejectsMissingHistory(t *testing.T) {
	d := fake.New()
	s := open(t, d, "s")
	in := input(s, "1")
	submit(t, s, in)
	update(t, s)
	if err := s.Complete(in.Execution.ActivityID, "remember me"); err != nil {
		t.Fatal(err)
	}
	update(t, s)
	handle, oldInstance := *s.Info().Handle, s.Info().InstanceID
	if stop, err := s.Close(ctx); err != nil || !stop.ConfirmsStop() || stop.SessionUsable {
		t.Fatalf("close: %+v %v", stop, err)
	}
	var registry engine.Registry
	if err := registry.Register(d); err != nil {
		t.Fatal(err)
	}
	resumed, err := registry.Resume(ctx, handle, options("s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resumed.Close(ctx) })
	if resumed.Info().InstanceID == oldInstance || resumed.Info().Handle.NativeID != handle.NativeID {
		t.Fatal("resume identity is incorrect")
	}
	if got := d.History(handle.NativeID); !reflect.DeepEqual(got, []string{in.Text, "remember me"}) {
		t.Fatal(got)
	}
	next := input(resumed.(*fake.Session), "2")
	submit(t, resumed.(*fake.Session), next)
	update(t, resumed.(*fake.Session))
	if _, err := resumed.(engine.Interrupter).Interrupt(ctx, next.Execution.ActivityID); err != nil {
		t.Fatal(err)
	}
	update(t, resumed.(*fake.Session))
	missing := handle
	missing.NativeID = "missing"
	if _, err := registry.Resume(ctx, missing, options("s")); !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatal(err)
	}
	var restarted engine.Registry
	if err := restarted.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(ctx, handle, options("s")); !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatal("new fake driver invented history", err)
	}
}

func TestCompletionCancelRaceHasOneTerminalResult(t *testing.T) {
	s := open(t, fake.New(), "s")
	for i := 0; i < 100; i++ {
		in := input(s, fmt.Sprint(i))
		submit(t, s, in)
		update(t, s)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := s.Complete(in.Execution.ActivityID, "completed"); err != nil && !errors.Is(err, engine.ErrStaleActivity) {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.Interrupt(ctx, in.Execution.ActivityID); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		r := update(t, s).Result
		if r == nil || (r.State != p.Completed && r.State != p.Cancelled) || r.Seq != uint64(2*i+2) {
			t.Fatalf("bad terminal result: %+v", r)
		}
		empty(t, s)
	}
}

func TestDispatchAndResultCrossWireBoundary(t *testing.T) {
	for _, role := range []string{"router", "worker"} {
		t.Run(role, func(t *testing.T) {
			d := fake.New()
			refs := p.Execution{ProjectID: "p", RunID: "run-" + role, AgentID: role, NodeID: "n", SessionID: "session-" + role, ActivityID: "activity-" + role}
			dispatch := p.Dispatch{CommandID: "command-" + role, Execution: refs, Config: options(refs.SessionID).Config, Input: "execute approved request"}
			data, err := p.Encode("dispatch-"+role, dispatch)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := p.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			request := frame.Message.(*p.Dispatch)
			s := open(t, d, request.Execution.SessionID)
			receipt := submit(t, s, p.Input{CommandID: request.CommandID, Execution: request.Execution, InstanceID: s.Info().InstanceID, Text: request.Input})
			if _, err := p.Encode("receipt-"+role, receipt); err != nil {
				t.Fatal(err)
			}
			update(t, s)
			output := `{"summary":"structured model output for control-plane validation"}`
			if err := s.Complete(refs.ActivityID, output); err != nil {
				t.Fatal(err)
			}
			message, err := update(t, s).Message()
			if err != nil {
				t.Fatal(err)
			}
			data, err = p.Encode("result-"+role, message)
			if err != nil {
				t.Fatal(err)
			}
			frame, err = p.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			r := frame.Message.(*p.Result)
			if r.Execution != refs || r.Output != output {
				t.Fatal("wire lost execution data")
			}
		})
	}
}
