package main

import (
	"context"
	"crypto/rand"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/coder/websocket"
)

type executionPeer struct {
	registration p.Register
	generation   uint64
	conn         *websocket.Conn
	cancel       context.CancelFunc
	writeMu      sync.Mutex
}

func (peer *executionPeer) send(ctx context.Context, message p.Message) error {
	data, err := p.Encode(rand.Text(), message)
	if err != nil {
		return err
	}
	peer.writeMu.Lock()
	defer peer.writeMu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := peer.conn.Write(callCtx, websocket.MessageText, data); err != nil {
		peer.cancel()
		peer.conn.CloseNow()
		return err
	}
	return nil
}
func (h *Hub) attachExecution(peer *executionPeer) {
	h.mu.Lock()
	id := peer.registration.NodeID
	old := h.peers[id]
	node := h.nodes[id]
	if node == nil {
		node = &Node{ID: id}
		h.nodes[id] = node
	}
	node.Name, node.Version, node.Online, node.LastSeenAt = peer.registration.Name, peer.registration.Version, true, h.now()
	node.generation++
	peer.generation = node.generation
	registration := peer.registration
	node.Registration = &registration
	h.peers[id] = peer
	h.mu.Unlock()
	if old != nil {
		old.cancel()
		old.conn.CloseNow()
	}
}
func (h *Hub) dropExecution(peer *executionPeer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := peer.registration.NodeID
	if h.peers[id] != peer {
		return false
	}
	delete(h.peers, id)
	if node := h.nodes[id]; node != nil && node.generation == peer.generation {
		node.Online = false
	}
	return true
}
func (h *Hub) peerOnline(peer *executionPeer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	node := h.nodes[peer.registration.NodeID]
	return h.peers[peer.registration.NodeID] == peer && node != nil && node.Online && h.now().Sub(node.LastSeenAt) <= offlineAfter
}
func (a *API) enrollNode(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		writeError(w, 403, "local_administration_required", "Enroll nodes from the control-plane host")
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	err = a.projects.EnrollNode(r.Context(), stringField(body, "nodeId"), stringField(body, "name"), stringField(body, "token"))
	a.reply(w, map[string]bool{"enrolled": err == nil}, err, 200)
}
func (a *API) routerMemory(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	project, err := a.projects.SetRouterMemory(r.Context(), r.PathValue("id"), stringField(body, "text"), "human:local")
	a.reply(w, project, err, 200)
}
func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	run, err := a.projects.CancelRun(r.Context(), stringField(body, "projectId"), r.PathValue("id"), stringField(body, "reason"), "human:local")
	a.reply(w, run, err, 202)
}
func (a *API) reconcileRun(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	run, err := a.projects.RequestReconciliation(r.Context(), stringField(body, "projectId"), r.PathValue("id"), "human:local")
	if err == nil {
		a.hub.mu.Lock()
		peer := a.hub.peers[run.NodeID]
		a.hub.mu.Unlock()
		if peer != nil {
			if sendErr := peer.send(r.Context(), p.ReconcileRequest{NodeID: run.NodeID, Known: []p.Cursor{{Execution: p.Execution{ProjectID: run.ProjectID, RunID: run.ID, AgentID: run.AgentID, NodeID: run.NodeID, SessionID: run.SessionID, ActivityID: run.ActivityID}, InstanceID: run.InstanceID, LastSeq: run.LastSeq}}}); sendErr != nil {
				log.Printf("Request reconciliation: %v", sendErr)
			}
		}
	}
	a.reply(w, run, err, 202)
}

func (a *API) serveExecution(w http.ResponseWriter, request *http.Request) {
	nodeID := request.Header.Get("X-Tina-Node")
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") || !a.projects.AuthenticateNode(request.Context(), nodeID, strings.TrimPrefix(authorization, "Bearer ")) {
		writeError(w, 401, "unauthorized_node", "Valid node enrollment is required")
		return
	}
	conn, err := websocket.Accept(w, request, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	firstCtx, firstCancel := context.WithTimeout(ctx, 5*time.Second)
	_, data, err := conn.Read(firstCtx)
	firstCancel()
	if err != nil {
		return
	}
	frame, err := p.Decode(data)
	if err != nil {
		return
	}
	registration, ok := frame.Message.(*p.Register)
	if !ok || registration.NodeID != nodeID {
		return
	}
	if err := a.projects.RegisterNode(ctx, *registration); err != nil {
		log.Printf("Node %s registration rejected: %v", nodeID, err)
		conn.Close(websocket.StatusPolicyViolation, "Agent binding does not match control plane")
		return
	}
	peer := &executionPeer{registration: *registration, conn: conn, cancel: cancel}
	a.hub.attachExecution(peer)
	defer func() {
		if a.hub.dropExecution(peer) {
			if err := a.projects.NodeOffline(context.Background(), nodeID); err != nil {
				log.Printf("Mark node offline: %v", err)
			}
		}
	}()
	known, err := a.projects.KnownExecutions(ctx, nodeID)
	if err != nil {
		return
	}
	if err := peer.send(ctx, p.ReconcileRequest{NodeID: nodeID, Known: known}); err != nil {
		return
	}
	go a.dispatchLoop(ctx, peer)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		frame, err := p.Decode(data)
		if err != nil {
			return
		}
		if heartbeat, ok := frame.Message.(*p.Heartbeat); ok {
			if heartbeat.NodeID != nodeID {
				return
			}
			a.hub.heartbeatGeneration(nodeID, peer.generation)
			continue
		}
		a.hub.heartbeatGeneration(nodeID, peer.generation)
		if err := a.projects.Receive(ctx, nodeID, frame); err != nil {
			log.Printf("Node %s fact rejected: %v", nodeID, err)
			conn.Close(websocket.StatusPolicyViolation, "Execution fact identity mismatch")
			return
		}
		if err := peer.send(ctx, p.Ack{NodeID: nodeID, MessageID: frame.ID}); err != nil {
			return
		}
	}
}
func (a *API) dispatchLoop(ctx context.Context, peer *executionPeer) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	sentCancels := map[string]time.Time{}
	lastReconciliation := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !a.hub.peerOnline(peer) {
			peer.cancel()
			peer.conn.CloseNow()
			return
		}
		cancellations, err := a.projects.PendingCancellations(ctx, peer.registration.NodeID)
		if err != nil {
			continue
		}
		for _, request := range cancellations {
			if time.Since(sentCancels[request.CommandID]) >= time.Second {
				if err := peer.send(ctx, request); err != nil {
					return
				}
				sentCancels[request.CommandID] = time.Now()
			}
		}
		if time.Since(lastReconciliation) >= 2*time.Second {
			known, err := a.projects.KnownExecutions(ctx, peer.registration.NodeID)
			if err == nil && len(known) > 0 {
				if err := peer.send(ctx, p.ReconcileRequest{NodeID: peer.registration.NodeID, Known: known}); err != nil {
					return
				}
			}
			lastReconciliation = time.Now()
		}
		dispatch, err := a.projects.Claim(ctx, peer.registration)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Claim execution for %s: %v", peer.registration.NodeID, err)
			}
			continue
		}
		if dispatch != nil {
			if err := peer.send(ctx, *dispatch); err != nil {
				return
			}
		}
	}
}
