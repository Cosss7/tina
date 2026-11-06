package main

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// incoming 是节点推上来的一条消息。H0 只有注册和心跳两种。
type incoming struct {
	Type     string       `json:"type"`
	NodeID   string       `json:"nodeId"`
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Projects []ProjectRef `json:"projects"`
}

// ServeNodeWS 接受一条节点连接：读注册、读心跳，断开时立刻把节点标离线。
func (h *Hub) ServeNodeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	nodeID := ""
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			break
		}
		var msg incoming
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "register":
			nodeID = msg.NodeID
			h.Register(msg.NodeID, msg.Name, msg.Version, msg.Projects)
		case "heartbeat":
			h.Heartbeat(msg.NodeID)
		}
	}
	if nodeID != "" {
		h.Disconnect(nodeID)
	}
}
