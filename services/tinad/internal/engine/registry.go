package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/Cosss7/tina/packages/protocol"
)

type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Driver
}

func (r *Registry) Register(driver Driver) error {
	if nilValue(driver) {
		return fmt.Errorf("driver is required")
	}
	definition := driver.Definition()
	if definition.ID == "" {
		return fmt.Errorf("engine id is required")
	}
	if err := definition.Capabilities.Validate(); err != nil {
		return err
	}
	if definition.Capabilities.Resume {
		if _, ok := driver.(Resumer); !ok {
			return fmt.Errorf("engine %q declares resume without an implementation", definition.ID)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drivers == nil {
		r.drivers = make(map[string]Driver)
	}
	if _, exists := r.drivers[definition.ID]; exists {
		return fmt.Errorf("engine %q is already registered", definition.ID)
	}
	r.drivers[definition.ID] = driver
	return nil
}

func (r *Registry) Open(ctx context.Context, options OpenOptions) (Session, error) {
	driver, caps, err := r.resolve(ctx, options)
	if err != nil {
		return nil, err
	}
	session, err := driver.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	return checkedSession(session, options, caps, nil)
}

func (r *Registry) Resume(ctx context.Context, handle protocol.SessionHandle, options OpenOptions) (Session, error) {
	if err := handle.Validate(); err != nil {
		return nil, err
	}
	if handle.EngineID != options.Config.EngineID || handle.SessionID != options.SessionID {
		return nil, fmt.Errorf("%w: handle does not match requested session", ErrResumeRejected)
	}
	driver, caps, err := r.resolve(ctx, options)
	if err != nil {
		return nil, err
	}
	resumer, ok := driver.(Resumer)
	if !ok || !caps.Resume {
		return nil, ErrUnsupported
	}
	session, err := resumer.Resume(ctx, handle, options)
	if err != nil {
		return nil, err
	}
	return checkedSession(session, options, caps, &handle)
}

func (r *Registry) resolve(ctx context.Context, options OpenOptions) (Driver, protocol.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, protocol.Capabilities{}, err
	}
	if err := options.Validate(); err != nil {
		return nil, protocol.Capabilities{}, err
	}
	r.mu.RLock()
	driver := r.drivers[options.Config.EngineID]
	r.mu.RUnlock()
	if driver == nil {
		return nil, protocol.Capabilities{}, fmt.Errorf("%w: %s", ErrUnavailable, options.Config.EngineID)
	}
	probe, err := driver.Probe(ctx, options.Config)
	if err != nil {
		return nil, protocol.Capabilities{}, err
	}
	if !probe.Available {
		return nil, protocol.Capabilities{}, fmt.Errorf("%w: %s", ErrUnavailable, probe.Diagnostic)
	}
	if err := probe.Capabilities.Validate(); err != nil {
		return nil, protocol.Capabilities{}, err
	}
	if !probe.Capabilities.Narrows(driver.Definition().Capabilities) {
		return nil, protocol.Capabilities{}, fmt.Errorf("probe widened declared capabilities")
	}
	return driver, probe.Capabilities, nil
}

func checkedSession(session Session, options OpenOptions, caps protocol.Capabilities, resumed *protocol.SessionHandle) (Session, error) {
	if nilValue(session) {
		return nil, fmt.Errorf("driver returned no session")
	}
	err := validateSession(session, options, caps)
	if err == nil && resumed != nil {
		handle := session.Info().Handle
		if handle == nil || handle.NativeID != resumed.NativeID {
			err = fmt.Errorf("%w: driver replaced the native session", ErrResumeRejected)
		}
	}
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stop, closeErr := session.Close(ctx)
		if closeErr == nil && !stop.ConfirmsStop() {
			closeErr = fmt.Errorf("invalid session cleanup was not confirmed")
		}
		return nil, errors.Join(err, closeErr)
	}
	return session, nil
}

func validateSession(session Session, options OpenOptions, caps protocol.Capabilities) error {
	info := session.Info()
	if info.SessionID != options.SessionID || info.InstanceID == "" {
		return fmt.Errorf("driver returned invalid session identity")
	}
	if err := info.Capabilities.Validate(); err != nil {
		return err
	}
	if !info.Capabilities.Narrows(caps) {
		return fmt.Errorf("session widened available capabilities")
	}
	if info.Handle != nil {
		if err := info.Handle.Validate(); err != nil {
			return err
		}
		if info.Handle.SessionID != options.SessionID || info.Handle.EngineID != options.Config.EngineID {
			return fmt.Errorf("driver returned an unrelated native handle")
		}
	} else if info.Capabilities.Resume {
		return fmt.Errorf("resumable session has no native handle")
	}
	if info.Capabilities.Interrupt != protocol.InterruptNone {
		if _, ok := session.(Interrupter); !ok {
			return fmt.Errorf("session declares interrupt without an implementation")
		}
	}
	if info.Capabilities.Respond {
		if _, ok := session.(Responder); !ok {
			return fmt.Errorf("session declares respond without an implementation")
		}
	}
	if session.Updates() == nil {
		return fmt.Errorf("session has no update stream")
	}
	return nil
}

func nilValue(value any) bool {
	return value == nil || (reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil())
}
