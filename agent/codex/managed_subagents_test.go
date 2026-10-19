package codex

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func collabItem(tool, status string, states map[string]any) map[string]any {
	return map[string]any{"id": "spawn-1", "type": "collabAgentToolCall", "senderThreadId": "target", "receiverThreadIds": []string{"child"}, "tool": tool, "status": status, "agentsStates": states}
}
func subagentTestSession() *managedSession {
	return &managedSession{ctx: context.Background(), thread: "target", turn: "main-turn", events: make(chan core.Event, 32)}
}
func sendCollab(t *testing.T, s *managedSession, thread string, item map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"threadId": thread, "turnId": "main-turn", "item": item})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMessage(daemonMessage{Method: "item/completed", Params: raw}); err != nil {
		t.Fatal(err)
	}
}
func childEvent(t *testing.T, s *managedSession) core.SubagentNotice {
	t.Helper()
	select {
	case e := <-s.events:
		if e.Type != core.EventNotice || e.Notice == nil || e.Notice.Subagent == nil {
			t.Fatalf("not a child notice: %+v", e)
		}
		return *e.Notice.Subagent
	default:
		t.Fatal("missing child notice")
		return core.SubagentNotice{}
	}
}
func TestManagedSubagents_LifecyclePreservesMetadataAndMainTurn(t *testing.T) {
	s := subagentTestSession()
	spawn := collabItem("spawnAgent", "completed", nil)
	spawn["prompt"], spawn["model"] = "Review protocol", "server-model"
	sendCollab(t, s, "target", spawn)
	n := childEvent(t, s)
	if n.Status != "running" || n.Description != "Review protocol" || n.Model != "server-model" {
		t.Fatalf("spawn: %+v", n)
	}
	for _, tc := range []struct{ status, want string }{{"pendingInit", "queued"}, {"running", "running"}, {"interrupted", "suspended"}, {"running", "running"}, {"completed", "completed"}, {"shutdown", "completed"}} {
		item := collabItem("wait", "completed", map[string]any{"child": map[string]any{"status": tc.status, "message": "result " + tc.status}})
		sendCollab(t, s, "target", item)
		n = childEvent(t, s)
		if n.Status != tc.want || n.Model != "server-model" || n.Description != "Review protocol" || n.TaskID != "spawn-1" {
			t.Fatalf("%s: %+v", tc.status, n)
		}
		sendCollab(t, s, "target", item)
		if len(s.events) != 0 {
			t.Fatal("duplicate lifecycle event replayed")
		}
	}
	if s.turn != "main-turn" || len(s.completed) != 0 {
		t.Fatal("child ended main turn")
	}
}
func TestManagedSubagents_ForeignThreadsAndToolCompletionAreNotChildCompletion(t *testing.T) {
	s := subagentTestSession()
	item := collabItem("spawnAgent", "completed", nil)
	sendCollab(t, s, "foreign", item)
	item["senderThreadId"] = "foreign"
	sendCollab(t, s, "target", item)
	if len(s.events) != 0 {
		t.Fatal("foreign child leaked")
	}
	item["senderThreadId"] = "target"
	sendCollab(t, s, "target", item)
	childEvent(t, s)
	sendCollab(t, s, "target", collabItem("wait", "completed", nil))
	if len(s.events) != 0 {
		t.Fatal("wait completion changed child status")
	}
	for _, status := range []string{"errored", "notFound", "shutdown"} {
		s = subagentTestSession()
		sendCollab(t, s, "target", collabItem("wait", "completed", map[string]any{"child": map[string]any{"status": status, "message": "DETAIL"}}))
		n := childEvent(t, s)
		want := "failed"
		if status == "shutdown" {
			want = "cancelled"
		}
		if n.Status != want || n.Detail != "DETAIL" {
			t.Fatalf("%s: %+v", status, n)
		}
	}
}
func TestManagedSubagents_SnapshotReconcilesLatestWithoutHistoricalReplay(t *testing.T) {
	s := subagentTestSession()
	spawn := collabItem("spawnAgent", "completed", nil)
	spawn["prompt"] = "Original task"
	done := collabItem("wait", "completed", map[string]any{"child": map[string]any{"status": "completed", "message": "FINISHED"}})
	history := []managedTurn{{Items: []map[string]any{spawn, done}}}
	s.reconcileSubagents(history)
	if len(s.events) != 0 {
		t.Fatal("initial attach replayed finished history")
	}
	s.reconcileSubagents([]managedTurn{{Items: []map[string]any{spawn}}})
	childEvent(t, s)
	s.reconcileSubagents(history)
	n := childEvent(t, s)
	if n.Status != "completed" || n.Detail != "FINISHED" || n.Description != "Original task" {
		t.Fatalf("reconciled: %+v", n)
	}
	s.reconcileSubagents(history)
	if len(s.events) != 0 {
		t.Fatal("snapshot replayed intermediate states")
	}
}
func TestManagedSubagents_DaemonEventsReachSharedNotice(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				return nil, false
			}
			_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "")})
			for _, item := range []map[string]any{
				collabItem("spawnAgent", "completed", nil),
				collabItem("wait", "completed", map[string]any{"child": map[string]any{"status": "errored", "message": "CHILD FAILED"}}),
			} {
				_ = ws.WriteJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "target", "turnId": "turn", "item": item}})
			}
			_ = ws.WriteJSON(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "target", "turnId": "turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "MAIN ANSWER", "phase": "final_answer"}}})
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	for _, want := range []string{"running", "failed"} {
		e := awaitManagedEvent(t, as, core.EventNotice)
		if e.Notice == nil || e.Notice.Subagent == nil || e.Notice.Subagent.Status != want {
			t.Fatalf("notice: %+v", e)
		}
	}
	e := awaitManagedEvent(t, as, core.EventText)
	if e.Content != "MAIN ANSWER" {
		t.Fatalf("main answer lost: %+v", e)
	}
}

func TestManagedSubagents_ReconnectRecoversChildCompletionAfterParentFinished(t *testing.T) {
	cwd := t.TempDir()
	var connections atomic.Int32
	disconnect := make(chan struct{})
	socket := managedFixture(t, func(ws *websocket.Conn) {
		first := connections.Add(1) == 1
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				return nil, false
			}
			snapshot := fixtureSnapshot(cwd, "target", "")
			items := []map[string]any{collabItem("spawnAgent", "completed", nil)}
			if !first {
				items = append(items, collabItem("wait", "completed", map[string]any{"child": map[string]any{"status": "completed", "message": "RECOVERED RESULT"}}))
			}
			snapshot["thread"].(map[string]any)["turns"] = []any{map[string]any{"id": "old-turn", "status": "completed", "items": items}}
			if err := ws.WriteJSON(map[string]any{"id": m.ID, "result": snapshot}); err != nil {
				t.Error(err)
			}
			if first {
				<-disconnect
				_ = ws.Close()
			}
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 2}).AttachSession(context.Background(), "target")
	if err != nil {
		close(disconnect)
		t.Fatal(err)
	}
	defer as.Close()
	e := awaitManagedEvent(t, as, core.EventNotice)
	close(disconnect)
	if e.Notice == nil || e.Notice.Subagent == nil || e.Notice.Subagent.Status != "running" {
		t.Fatalf("initial snapshot: %+v", e)
	}
	e = awaitManagedEvent(t, as, core.EventNotice)
	if e.Notice == nil || e.Notice.Subagent == nil || e.Notice.Subagent.Status != "completed" || e.Notice.Subagent.Detail != "RECOVERED RESULT" {
		t.Fatalf("reconnect snapshot: %+v", e)
	}
}

func TestManagedSubagents_SpawnWithoutStatesAdvancesFromQueued(t *testing.T) {
	s := subagentTestSession()
	sendCollab(t, s, "target", collabItem("spawnAgent", "inProgress", nil))
	if n := childEvent(t, s); n.Status != "queued" {
		t.Fatalf("spawn start: %+v", n)
	}
	sendCollab(t, s, "target", collabItem("spawnAgent", "completed", nil))
	if n := childEvent(t, s); n.Status != "running" {
		t.Fatalf("spawn completion: %+v", n)
	}
}
