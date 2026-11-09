package engine_test

import (
	"context"
	"errors"
	"testing"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

type stubDriver struct {
	definition engine.Definition
	probe      engine.Probe
	session    engine.Session
}

func (d *stubDriver) Definition() engine.Definition { return d.definition }
func (d *stubDriver) Probe(context.Context, p.SessionConfig) (engine.Probe, error) {
	return d.probe, nil
}
func (d *stubDriver) Create(context.Context, engine.OpenOptions) (engine.Session, error) {
	return d.session, nil
}

type resumeDriver struct{ *stubDriver }

func (d resumeDriver) Resume(context.Context, p.SessionHandle, engine.OpenOptions) (engine.Session, error) {
	return d.session, nil
}

type stubSession struct {
	info    engine.SessionInfo
	updates chan engine.Update
	closed  bool
}

func (s *stubSession) Info() engine.SessionInfo { return s.info }
func (*stubSession) Submit(context.Context, p.Input) (p.Receipt, error) {
	return p.Receipt{}, engine.ErrUnsupported
}
func (s *stubSession) Updates() <-chan engine.Update { return s.updates }
func (s *stubSession) Close(context.Context) (p.StopEvidence, error) {
	s.closed = true
	return p.StopEvidence{Mode: p.InterruptProcess, ActivityStopped: true, Root: p.ExitNotApplicable, Workload: p.ExitNotApplicable}, nil
}

func stub() (*stubDriver, *stubSession, engine.OpenOptions) {
	caps := p.Capabilities{Interrupt: p.InterruptNone}
	s := &stubSession{info: engine.SessionInfo{SessionID: "s", InstanceID: "i", Capabilities: caps}, updates: make(chan engine.Update)}
	d := &stubDriver{definition: engine.Definition{ID: "stub", Capabilities: caps}, probe: engine.Probe{Available: true, Capabilities: caps}, session: s}
	return d, s, engine.OpenOptions{SessionID: "s", Config: p.SessionConfig{EngineID: "stub", Cwd: "/work"}}
}

func TestRegistrationRejectsUnsupportedDeclarations(t *testing.T) {
	var registry engine.Registry
	var typedNil *stubDriver
	if registry.Register(typedNil) == nil {
		t.Fatal("registered typed nil")
	}
	d, _, _ := stub()
	d.definition.Capabilities.Resume = true
	if registry.Register(d) == nil {
		t.Fatal("registered resume without a resumer")
	}
	d.definition.Capabilities.Resume = false
	if err := registry.Register(d); err != nil {
		t.Fatal(err)
	}
	if registry.Register(d) == nil {
		t.Fatal("registered duplicate engine")
	}
}

func TestLiveCapabilitiesCannotWidenOrInventImplementations(t *testing.T) {
	for _, scenario := range []string{"probe widens", "session widens", "missing interrupt", "missing responder", "missing handle", "missing stream", "wrong session"} {
		t.Run(scenario, func(t *testing.T) {
			d, s, options := stub()
			var driver engine.Driver = d
			switch scenario {
			case "probe widens":
				d.probe.Capabilities.Respond = true
			case "session widens":
				s.info.Capabilities.Respond = true
			case "missing interrupt":
				d.definition.Capabilities.Interrupt = p.InterruptTurn
				d.probe.Capabilities.Interrupt = p.InterruptTurn
				s.info.Capabilities.Interrupt = p.InterruptTurn
			case "missing responder":
				d.definition.Capabilities.Respond = true
				d.probe.Capabilities.Respond = true
				s.info.Capabilities.Respond = true
			case "missing handle":
				d.definition.Capabilities.Resume = true
				d.probe.Capabilities.Resume = true
				s.info.Capabilities.Resume = true
				driver = resumeDriver{d}
			case "missing stream":
				s.updates = nil
			case "wrong session":
				s.info.SessionID = "another"
			}
			var registry engine.Registry
			if err := registry.Register(driver); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Open(context.Background(), options); err == nil {
				t.Fatal("opened invalid session")
			}
			if scenario != "probe widens" && !s.closed {
				t.Fatal("rejected session leaked its runtime")
			}
		})
	}
}

func TestResumeNeverFallsBackToAnotherNativeSession(t *testing.T) {
	d, s, options := stub()
	d.definition.Capabilities.Resume = true
	d.probe.Capabilities.Resume = true
	s.info.Capabilities.Resume = true
	s.info.Handle = &p.SessionHandle{EngineID: "stub", SessionID: "s", NativeID: "new-history"}
	var registry engine.Registry
	if err := registry.Register(resumeDriver{d}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Resume(context.Background(), p.SessionHandle{EngineID: "stub", SessionID: "s", NativeID: "original-history"}, options)
	if !errors.Is(err, engine.ErrResumeRejected) || !s.closed {
		t.Fatalf("resume=%v, cleaned=%v", err, s.closed)
	}
}

func TestUnavailableAndUnsupportedAreExplicit(t *testing.T) {
	d, _, options := stub()
	var registry engine.Registry
	if _, err := registry.Open(context.Background(), options); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatal(err)
	}
	if err := registry.Register(d); err != nil {
		t.Fatal(err)
	}
	handle := p.SessionHandle{EngineID: "stub", SessionID: "s", NativeID: "native"}
	if _, err := registry.Resume(context.Background(), handle, options); !errors.Is(err, engine.ErrUnsupported) {
		t.Fatal(err)
	}
	d.probe.Available = false
	if _, err := registry.Open(context.Background(), options); !errors.Is(err, engine.ErrUnavailable) {
		t.Fatal(err)
	}
}
