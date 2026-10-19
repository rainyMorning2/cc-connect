package kimi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebSubagents_LifecycleDoesNotCompleteMainTurn(t *testing.T) {
	s := &webSession{id: "session_test", ctx: context.Background(), events: make(chan core.Event, 32), epoch: "epoch", turn: "main-prompt", prompt: "main-prompt", turnKeys: map[int64]string{1: "main-prompt"}, completed: map[string]bool{}, text: map[string]string{}}
	seq := int64(0)
	frame := func(kind string, p any) {
		t.Helper()
		seq++
		raw, _ := json.Marshal(p)
		if err := s.handleFrame(webFrame{Type: kind, SessionID: s.id, Epoch: s.epoch, Seq: seq, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	frame("subagent.spawned", map[string]any{"subagentId": "child", "subagentName": "reviewer", "description": "review changes", "taskId": "task-1", "model": "child-model"})
	frame("subagent.started", map[string]any{"subagentId": "child"})
	frame("subagent.suspended", map[string]any{"subagentId": "child", "reason": "awaiting input"})
	frame("subagent.started", map[string]any{"subagentId": "child"})
	frame("subagent.completed", map[string]any{"subagentId": "child", "resultSummary": "review complete"})
	frame("subagent.failed", map[string]any{"subagentId": "other-child", "error": "tool failed"}) // Error is a string, unlike turn.ended.
	frame("subagent.cancelled", map[string]any{"subagentId": "third-child"})
	// Child turns and tokens must never overwrite main state or appear as its answer.
	frame("turn.ended", map[string]any{"agentId": "child", "turnId": 1, "reason": "completed"})
	frame("assistant.delta", map[string]any{"agentId": "child", "turnId": 1, "delta": "CHILD INTERNAL TEXT"})
	frame("agent.status.updated", map[string]any{"agentId": "child", "model": "child-model", "contextTokens": 999})
	want := []string{"queued", "running", "suspended", "running", "completed", "failed", "cancelled"}
	for _, status := range want {
		select {
		case e := <-s.events:
			if e.Type != core.EventNotice || e.Notice.Subagent == nil || e.Notice.Subagent.Status != status {
				t.Fatalf("unexpected child event: %+v", e)
			}
			if e.Notice.Subagent.ID == "child" && e.Notice.Subagent.TaskID != "task-1" {
				t.Fatal("child metadata lost")
			}
		default:
			t.Fatal("missing lifecycle notice")
		}
	}
	if len(s.events) != 0 || s.turn != "main-prompt" || s.model != "" || s.hasUsage || s.text["main-prompt"] != "" {
		t.Fatal("child lifecycle polluted main turn")
	}
	frame("assistant.delta", map[string]any{"agentId": "main", "turnId": 1, "delta": "MAIN CONTINUES"})
	if e := <-s.events; e.Type != core.EventText || e.Content != "MAIN CONTINUES" {
		t.Fatal(e)
	}
}

func TestWebSubagents_SnapshotAndReplayDoNotDuplicate(t *testing.T) {
	s := &webSession{id: "session_test", ctx: context.Background(), events: make(chan core.Event, 16), epoch: "epoch"}
	tasks := []webTask{
		{ID: "task-1", SessionID: s.id, Kind: "subagent", AgentID: "child", Description: "running review", Status: "running", Phase: "working"},
		{ID: "old-task", SessionID: s.id, Kind: "subagent", AgentID: "old-child", Status: "completed"},
		{ID: "foreign", SessionID: "other", Kind: "subagent", AgentID: "foreign", Status: "running"},
	}
	s.emitSnapshotSubagents(tasks)
	s.emitSnapshotSubagents(tasks)
	if len(s.events) != 1 {
		t.Fatalf("snapshot notices=%d", len(s.events))
	}
	<-s.events
	raw := json.RawMessage(`{"subagentId":"child","resultSummary":"done"}`)
	f := webFrame{Type: "subagent.completed", SessionID: s.id, Seq: 1, Epoch: s.epoch, Payload: raw}
	if err := s.handleFrame(f); err != nil {
		t.Fatal(err)
	}
	if err := s.handleFrame(f); err != nil {
		t.Fatal(err)
	}
	if len(s.events) != 1 {
		t.Fatalf("replayed completion notices=%d", len(s.events))
	}
	tasks[0].Phase = "failed"
	tasks[0].Status = "failed"
	s.emitSnapshotSubagents(tasks)
	if len(s.events) != 2 {
		t.Fatal("reconnect did not reconcile known child")
	}
}

func TestWebEngine_SubagentLifecycleVisibleWithoutReplacingMainAnswer(t *testing.T) {
	f := newWebFixture(t)
	p := &webWorkspacePlatform{}
	e := core.NewEngine("subagents", f.agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	e.SetAdminFrom("user")
	defer e.Stop()
	e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: "/attach session_test", ReplyCtx: "user"})
	await := func(text string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(p.getSent(), text) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", text, p.getSent())
	}
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 1, "promptId": "main-prompt"})
	f.broadcast("subagent.spawned", map[string]any{"subagentId": "child", "subagentName": "reviewer", "description": "CHECK CODE", "taskId": "task-1"})
	await("CHECK CODE")
	f.broadcast("subagent.failed", map[string]any{"subagentId": "child", "error": "CHILD TOOL FAILURE"})
	await("reviewer (child): Failed")
	await("CHILD TOOL FAILURE")
	f.broadcast("assistant.delta", map[string]any{"agentId": "main", "turnId": 1, "delta": "MAIN ANSWER"})
	f.mu.Lock()
	f.appendMessage("main-prompt", "assistant", "MAIN ANSWER")
	f.mu.Unlock()
	f.broadcast("turn.ended", map[string]any{"agentId": "main", "turnId": 1, "reason": "completed"})
	await("MAIN ANSWER")
}
