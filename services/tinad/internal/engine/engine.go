// Package engine defines tinad's in-process adapter boundary.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Cosss7/tina/packages/protocol"
)

var (
	ErrUnsupported    = errors.New("engine capability is unsupported")
	ErrUnavailable    = errors.New("engine is unavailable")
	ErrResumeRejected = errors.New("native session resume was rejected")
	ErrClosed         = errors.New("runtime is closed")
	ErrConflict       = errors.New("command or session identity conflict")
	ErrStaleActivity  = errors.New("activity does not belong to the current execution")
)

type Definition struct {
	ID           string
	Capabilities protocol.Capabilities
}

type Probe struct {
	Available    bool
	Version      string
	Diagnostic   string
	Capabilities protocol.Capabilities
}

type OpenOptions struct {
	SessionID string
	Config    protocol.SessionConfig
}

func (o OpenOptions) Validate() error {
	if o.SessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	return o.Config.Validate()
}

// A Driver is a reusable client/factory; Create returns isolated session state.
type Driver interface {
	Definition() Definition
	Probe(context.Context, protocol.SessionConfig) (Probe, error)
	Create(context.Context, OpenOptions) (Session, error)
}

type Resumer interface {
	Resume(context.Context, protocol.SessionHandle, OpenOptions) (Session, error)
}

type SessionInfo struct {
	SessionID    string
	InstanceID   string
	Handle       *protocol.SessionHandle
	Capabilities protocol.Capabilities
}

type Session interface {
	Info() SessionInfo
	Submit(context.Context, protocol.Input) (protocol.Receipt, error)
	Updates() <-chan Update
	Close(context.Context) (protocol.StopEvidence, error)
}

type Interrupter interface {
	Interrupt(context.Context, string) (protocol.StopEvidence, error)
}

type Responder interface {
	Respond(context.Context, string, json.RawMessage) error
}

// Update carries one progress fact or terminal result, never a business mutation.
type Update struct {
	Event  *protocol.Event
	Result *protocol.Result
}

func (u Update) Validate() error {
	if (u.Event == nil) == (u.Result == nil) {
		return fmt.Errorf("update requires exactly one event or result")
	}
	if u.Event != nil {
		return u.Event.Validate()
	}
	return u.Result.Validate()
}

func (u Update) Message() (protocol.Message, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	if u.Event != nil {
		return u.Event, nil
	}
	return u.Result, nil
}
