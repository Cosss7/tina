package main

import (
	"sort"
	"sync"
	"time"
)

// offlineAfter 是判定节点掉线的阈值。节点每 1 秒发一次心跳，连接断开时还会
// 立刻标记，所以这个值只兜住"连接还在但人没了"的情况。
const offlineAfter = 3 * time.Second

// Node 是控制面记住的一个节点。
type Node struct {
	ID         string
	Name       string
	Version    string
	Projects   []string
	LastSeenAt time.Time
	Online     bool
}

// Project 是看板上的一个项目。H0 的任务列建在 M0.4，这里先留空。
type Project struct {
	ID         string
	Name       string
	HomeNodeID string
}

// Hub 是控制面的全部内存状态。H0 是单账号、单进程，M0.3 还不落库。
type Hub struct {
	mu       sync.Mutex
	nodes    map[string]*Node
	projects map[string]*Project
	order    []string
	conns    map[string]*nodeConn
	now      func() time.Time
}

func NewHub() *Hub {
	return &Hub{
		nodes:    make(map[string]*Node),
		projects: make(map[string]*Project),
		conns:    make(map[string]*nodeConn),
		now:      time.Now,
	}
}

// Register 记录一次节点注册，并把节点声明的项目挂到它名下。
func (h *Hub) Register(id, name, version string, projects []ProjectRef) {
	h.mu.Lock()
	defer h.mu.Unlock()

	node, ok := h.nodes[id]
	if !ok {
		node = &Node{ID: id}
		h.nodes[id] = node
	}
	node.Name = name
	node.Version = version
	node.LastSeenAt = h.now()
	node.Online = true
	node.Projects = node.Projects[:0]
	for _, ref := range projects {
		node.Projects = append(node.Projects, ref.ID)
		if _, exists := h.projects[ref.ID]; !exists {
			h.projects[ref.ID] = &Project{ID: ref.ID, Name: ref.Name}
			h.order = append(h.order, ref.ID)
		}
		h.projects[ref.ID].HomeNodeID = id
	}
}

// Heartbeat 只刷新存活时间；没注册过的心跳直接丢弃。
func (h *Hub) Heartbeat(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	node, ok := h.nodes[id]
	if !ok {
		return
	}
	node.LastSeenAt = h.now()
	node.Online = true
}

// Disconnect 在连接断开时立刻把节点标成离线，不等超时。
func (h *Hub) Disconnect(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if node, ok := h.nodes[id]; ok {
		node.Online = false
	}
}

// Sweep 把超时没心跳的节点标成离线，返回这次新掉线的节点数。
func (h *Hub) Sweep() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := 0
	now := h.now()
	for _, node := range h.nodes {
		if node.Online && now.Sub(node.LastSeenAt) > offlineAfter {
			node.Online = false
			changed++
		}
	}
	return changed
}

// ProjectRef 是节点注册时报上来的项目。
type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Snapshot 是给浏览器看的只读视图。
type Snapshot struct {
	Version  string        `json:"version"`
	Now      time.Time     `json:"now"`
	Nodes    []NodeView    `json:"nodes"`
	Projects []ProjectView `json:"projects"`
}

type NodeView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Online     bool      `json:"online"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	Projects   []string  `json:"projects"`
}

type ProjectView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	HomeNodeID string `json:"homeNodeId"`
	Online     bool   `json:"online"`
}

func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap := Snapshot{Version: version, Now: h.now(), Nodes: []NodeView{}, Projects: []ProjectView{}}
	for _, node := range h.nodes {
		projects := node.Projects
		if projects == nil {
			projects = []string{}
		}
		snap.Nodes = append(snap.Nodes, NodeView{
			ID: node.ID, Name: node.Name, Version: node.Version,
			Online: node.Online, LastSeenAt: node.LastSeenAt, Projects: projects,
		})
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].ID < snap.Nodes[j].ID })

	for _, id := range h.order {
		project := h.projects[id]
		online := false
		if node, ok := h.nodes[project.HomeNodeID]; ok {
			online = node.Online
		}
		snap.Projects = append(snap.Projects, ProjectView{
			ID: project.ID, Name: project.Name, HomeNodeID: project.HomeNodeID, Online: online,
		})
	}
	return snap
}
