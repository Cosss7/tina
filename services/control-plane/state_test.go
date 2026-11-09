package main

import (
	"testing"
	"time"
)

func newTestHub(now time.Time) *Hub {
	hub := NewHub()
	hub.now = func() time.Time { return now }
	return hub
}

func TestRegisterMarksNodeOnline(t *testing.T) {
	hub := newTestHub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hub.Register("node-1", "本机", "0.1.0-dev")

	snap := hub.Snapshot()
	if len(snap.Nodes) != 1 || !snap.Nodes[0].Online {
		t.Fatalf("节点应当在线: %+v", snap.Nodes)
	}
}

func TestSweepMarksOfflineAfterThreshold(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := newTestHub(now)
	hub.Register("node-1", "本机", "0")

	now = now.Add(2 * time.Second)
	hub.now = func() time.Time { return now }
	if changed := hub.Sweep(); changed != 0 {
		t.Fatalf("两秒不该判离线")
	}

	now = now.Add(2 * time.Second)
	if changed := hub.Sweep(); changed != 1 {
		t.Fatalf("超过三秒应判离线")
	}
	if hub.Snapshot().Nodes[0].Online {
		t.Fatalf("节点应离线")
	}
}

func TestHeartbeatKeepsNodeOnline(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := newTestHub(now)
	hub.Register("node-1", "本机", "0")

	now = now.Add(2 * time.Second)
	hub.now = func() time.Time { return now }
	hub.Heartbeat("node-1")
	now = now.Add(2 * time.Second)
	if changed := hub.Sweep(); changed != 0 {
		t.Fatalf("有心跳不该判离线")
	}
}

func TestDisconnectIsImmediate(t *testing.T) {
	hub := newTestHub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hub.Register("node-1", "本机", "0")
	hub.Disconnect("node-1")
	if hub.Snapshot().Nodes[0].Online {
		t.Fatalf("断开应当立刻离线")
	}
}

func TestOldConnectionCannotDisconnectOrRefreshReplacement(t *testing.T) {
	hub := NewHub()
	old := hub.Register("n", "old", "0")
	hub.Register("n", "new", "0")
	hub.disconnectGeneration("n", old)
	if !hub.Snapshot().Nodes[0].Online {
		t.Fatal("old connection disconnected new one")
	}
	hub.Disconnect("n")
	hub.heartbeatGeneration("n", old)
	if hub.Snapshot().Nodes[0].Online {
		t.Fatal("old connection revived node")
	}
}

func TestUnknownHeartbeatIsIgnored(t *testing.T) {
	hub := newTestHub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	hub.Heartbeat("nobody")
	if len(hub.Snapshot().Nodes) != 0 {
		t.Fatalf("没注册过的心跳不该建节点")
	}
}
