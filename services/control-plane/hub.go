package main

import (
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
)

// P2 retains H0 presence reporting, never project registration or business RPC.
// Versioned execution transport and authenticated Agent registration follow in P3.
func (h *Hub) ServeNodeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	nodeID := ""
	var generation uint64
	defer func() {
		if nodeID != "" {
			h.disconnectGeneration(nodeID, generation)
		}
	}()
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var msg struct {
			Type    string `json:"type"`
			NodeID  string `json:"nodeId"`
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "register":
			if nodeID != "" || msg.NodeID == "" {
				return
			}
			nodeID = msg.NodeID
			generation = h.Register(msg.NodeID, msg.Name, msg.Version)
		case "heartbeat":
			if nodeID != "" && msg.NodeID == nodeID {
				h.heartbeatGeneration(nodeID, generation)
			}
		}
	}
}
