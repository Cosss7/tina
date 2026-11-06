package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// nodeConn 是一条节点连接，同时是控制面向节点发请求的唯一通道。
//
// coder/websocket 不允许并发写，所以写操作串在 writeMu 后面；请求和响应按 id 配对。
type nodeConn struct {
	id      string
	conn    *websocket.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan response
	nextID  int
	closed  bool
}

type response struct {
	result json.RawMessage
	err    *rpcError
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// call 发一条请求并等响应。超时或连接断开都会返回错误。
func (n *nodeConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, errors.New("节点连接已关闭")
	}
	n.nextID++
	id := fmt.Sprintf("req-%d", n.nextID)
	ch := make(chan response, 1)
	n.pending[id] = ch
	n.mu.Unlock()

	defer func() {
		n.mu.Lock()
		delete(n.pending, id)
		n.mu.Unlock()
	}()

	frame := map[string]any{"type": "request", "id": id, "method": method, "params": params}
	payload, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	n.writeMu.Lock()
	writeErr := n.conn.Write(ctx, websocket.MessageText, payload)
	n.writeMu.Unlock()
	if writeErr != nil {
		return nil, writeErr
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("等节点响应超时：%w", ctx.Err())
	case got := <-ch:
		if got.err != nil {
			return nil, got.err
		}
		return got.result, nil
	}
}

func (n *nodeConn) deliver(id string, res response) {
	n.mu.Lock()
	ch, ok := n.pending[id]
	delete(n.pending, id)
	n.mu.Unlock()
	if ok {
		ch <- res
	}
}

func (n *nodeConn) shutdown() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	for id := range n.pending {
		delete(n.pending, id)
	}
	n.mu.Unlock()
}

// wsError 是给 REST 层的错误：带上 HTTP 状态码，让 curl 一眼看出是哪一类问题。
type wsError struct {
	status int
	code   string
	msg    string
}

func (e *wsError) Error() string { return e.msg }

func rpcStatus(err error) (int, string) {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case "not_found":
			return 404, rpc.Code
		case "conflict":
			return 409, rpc.Code
		case "invalid_argument":
			return 400, rpc.Code
		case "method_not_found":
			return 501, rpc.Code
		}
		return 500, rpc.Code
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return 504, "timeout"
	}
	return 502, "node_unavailable"
}

// callNode 找到托管该项目且在线的节点，把请求转过去。
func (h *Hub) callNode(ctx context.Context, projectID, method string, params any) (json.RawMessage, error) {
	conn := h.connectionFor(projectID)
	if conn == nil {
		return nil, &wsError{status: 503, code: "node_offline", msg: "没有在线的节点托管这个项目"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.call(ctx, method, params)
}
