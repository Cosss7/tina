package main

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// incoming 是节点推上来的一条消息：注册、心跳、或者某个请求的响应。
type incoming struct {
	Type     string          `json:"type"`
	NodeID   string          `json:"nodeId"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Projects []ProjectRef    `json:"projects"`
	ID       string          `json:"id"`
	Result   json.RawMessage `json:"result"`
	Error    *rpcError       `json:"error"`
}

// ServeNodeWS 接受一条节点连接：注册、心跳、响应请求，断开时立刻把节点标离线。
func (h *Hub) ServeNodeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	node := &nodeConn{conn: conn, pending: make(map[string]chan response)}
	nodeID := ""

	defer func() {
		h.dropConnection(node)
		if nodeID != "" {
			h.Disconnect(nodeID)
		}
	}()

	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var msg incoming
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "register":
			nodeID = msg.NodeID
			node.id = msg.NodeID
			h.attachConnection(node)
			h.Register(msg.NodeID, msg.Name, msg.Version, msg.Projects)
		case "heartbeat":
			h.Heartbeat(msg.NodeID)
		case "response":
			res := response{result: msg.Result}
			if msg.Error != nil {
				res.err = msg.Error
			}
			node.deliver(msg.ID, res)
		}
	}
}

// attachConnection 记住这条连接，后来的 REST 请求要用它转发。
func (h *Hub) attachConnection(node *nodeConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[node.id] = node
}

func (h *Hub) dropConnection(node *nodeConn) {
	node.shutdown()
	h.mu.Lock()
	defer h.mu.Unlock()
	if node.id != "" && h.conns[node.id] == node {
		delete(h.conns, node.id)
	}
}

// connectionFor 找托管这个项目、且还连着的节点。
func (h *Hub) connectionFor(projectID string) *nodeConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	project, ok := h.projects[projectID]
	if !ok {
		return nil
	}
	node, ok := h.conns[project.HomeNodeID]
	if !ok {
		return nil
	}
	return node
}
