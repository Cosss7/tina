package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

type Message interface {
	messageType() string
	Validate() error
}

func (Register) messageType() string          { return "register" }
func (Heartbeat) messageType() string         { return "heartbeat" }
func (Dispatch) messageType() string          { return "dispatch" }
func (Input) messageType() string             { return "input" }
func (Cancel) messageType() string            { return "cancel" }
func (Respond) messageType() string           { return "respond" }
func (Receipt) messageType() string           { return "receipt" }
func (Event) messageType() string             { return "event" }
func (Result) messageType() string            { return "result" }
func (Ack) messageType() string               { return "ack" }
func (ReconcileRequest) messageType() string  { return "reconcile.request" }
func (ReconcileResponse) messageType() string { return "reconcile.response" }

type Frame struct {
	ID      string
	Message Message
}

type envelope struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func Encode(id string, message Message) ([]byte, error) {
	if err := required("id", id); err != nil {
		return nil, err
	}
	if message == nil || (reflect.ValueOf(message).Kind() == reflect.Pointer && reflect.ValueOf(message).IsNil()) {
		return nil, fmt.Errorf("message is required")
	}
	if err := message.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", message.messageType(), err)
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Version: Version, ID: id, Type: message.messageType(), Payload: payload})
}

func Decode(data []byte) (Frame, error) {
	var wire envelope
	if err := decodeObject(data, &wire); err != nil {
		return Frame{}, err
	}
	if wire.Version != Version {
		return Frame{}, fmt.Errorf("unsupported protocol version %d", wire.Version)
	}
	if err := required("id", wire.ID); err != nil {
		return Frame{}, err
	}
	var message Message
	switch wire.Type {
	case "register":
		message = &Register{}
	case "heartbeat":
		message = &Heartbeat{}
	case "dispatch":
		message = &Dispatch{}
	case "input":
		message = &Input{}
	case "cancel":
		message = &Cancel{}
	case "respond":
		message = &Respond{}
	case "receipt":
		message = &Receipt{}
	case "event":
		message = &Event{}
	case "result":
		message = &Result{}
	case "ack":
		message = &Ack{}
	case "reconcile.request":
		message = &ReconcileRequest{}
	case "reconcile.response":
		message = &ReconcileResponse{}
	default:
		return Frame{}, fmt.Errorf("unsupported execution message %q", wire.Type)
	}
	if err := decodeObject(wire.Payload, message); err != nil {
		return Frame{}, fmt.Errorf("%s: %w", wire.Type, err)
	}
	if err := message.Validate(); err != nil {
		return Frame{}, fmt.Errorf("%s: %w", wire.Type, err)
	}
	return Frame{ID: wire.ID, Message: message}, nil
}

func decodeObject(data []byte, target any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON object")
	}
	return nil
}
