package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}
type rpc struct {
	mu, writeMu sync.Mutex
	next        int
	pending     map[int]chan rpcReply
	stdin       io.Writer
	done        chan struct{}
	tail        []string
}

func newRPC(stdin io.Writer, stdout, stderr io.Reader, onEvent func(json.RawMessage)) *rpc {
	r := &rpc{stdin: stdin, pending: map[int]chan rpcReply{}, done: make(chan struct{})}
	go func() {
		if closer, ok := stderr.(io.Closer); ok {
			defer closer.Close()
		}
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			r.mu.Lock()
			r.tail = append(r.tail, scanner.Text())
			if len(r.tail) > 8 {
				r.tail = r.tail[1:]
			}
			r.mu.Unlock()
		}
	}()
	go func() {
		defer close(r.done)
		if closer, ok := stdout.(io.Closer); ok {
			defer closer.Close()
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 8192), 4<<20)
		for scanner.Scan() {
			var message struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				rpcReply
			}
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				continue
			}
			if message.Method == "session.event" {
				onEvent(message.Params)
				continue
			}
			r.mu.Lock()
			pending := r.pending[message.ID]
			delete(r.pending, message.ID)
			r.mu.Unlock()
			if pending != nil {
				pending <- message.rpcReply
			}
		}
	}()
	return r
}
func (r *rpc) call(ctx context.Context, method string, params any, result any) error {
	r.mu.Lock()
	r.next++
	id := r.next
	reply := make(chan rpcReply, 1)
	r.pending[id] = reply
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	r.writeMu.Lock()
	_, err = r.stdin.Write(append(data, '\n'))
	r.writeMu.Unlock()
	if err != nil {
		return err
	}
	select {
	case response := <-reply:
		if response.Error != nil {
			return fmt.Errorf("%s: %s", method, response.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return fmt.Errorf("dsh stdio closed during %s", method)
	}
}
