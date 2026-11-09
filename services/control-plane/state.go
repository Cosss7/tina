package main

import (
	"sort"
	"sync"
	"time"
)

const offlineAfter = 3 * time.Second

type Node struct {
	ID, Name, Version string
	LastSeenAt        time.Time
	Online            bool
	generation        uint64
}
type Hub struct {
	mu    sync.Mutex
	nodes map[string]*Node
	now   func() time.Time
}

func NewHub() *Hub { return &Hub{nodes: map[string]*Node{}, now: time.Now} }
func (h *Hub) Register(id, name, version string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	node := h.nodes[id]
	if node == nil {
		node = &Node{ID: id}
		h.nodes[id] = node
	}
	node.Name, node.Version, node.LastSeenAt, node.Online = name, version, h.now(), true
	node.generation++
	return node.generation
}
func (h *Hub) Heartbeat(id string) { h.heartbeatGeneration(id, 0) }
func (h *Hub) heartbeatGeneration(id string, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if node := h.nodes[id]; node != nil && (generation == 0 || generation == node.generation) {
		node.LastSeenAt, node.Online = h.now(), true
	}
}
func (h *Hub) Disconnect(id string) { h.disconnectGeneration(id, 0) }
func (h *Hub) disconnectGeneration(id string, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if node := h.nodes[id]; node != nil && (generation == 0 || generation == node.generation) {
		node.Online = false
	}
}
func (h *Hub) Sweep() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := 0
	for _, node := range h.nodes {
		if node.Online && h.now().Sub(node.LastSeenAt) > offlineAfter {
			node.Online = false
			changed++
		}
	}
	return changed
}

type Snapshot struct {
	Version string     `json:"version"`
	Now     time.Time  `json:"now"`
	Nodes   []NodeView `json:"nodes"`
}
type NodeView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Online     bool      `json:"online"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Snapshot{Version: version, Now: h.now(), Nodes: []NodeView{}}
	for _, n := range h.nodes {
		s.Nodes = append(s.Nodes, NodeView{ID: n.ID, Name: n.Name, Version: n.Version, Online: n.Online, LastSeenAt: n.LastSeenAt})
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	return s
}
