package core

import (
	"strings"
	"testing"
	"time"
)

func emitChildNotice(as *sharedTestSession, id, status, detail string) {
	as.emit(Event{Type: EventNotice, Notice: &AgentNotice{Kind: "subagent", Subagent: &SubagentNotice{ID: id, Name: id, Status: status, Description: "task " + id, Detail: detail}}})
}

func TestSharedSubagentCards_UpdateEachOriginalPanel(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	as := env.a.connection("first")
	emitChildNotice(as, "child-one", "running", "")
	env.await("task child-one")
	emitChildNotice(as, "child-two", "running", "")
	env.await("task child-two")
	p.mu.Lock()
	initialCards := append([]*Card(nil), p.sentCards...)
	p.mu.Unlock()
	if len(initialCards) != 2 {
		t.Fatalf("initial cards=%d", len(initialCards))
	}
	for _, card := range initialCards {
		if !card.RichMarkdown || card.CollapsedDetailsTitle == "" || card.PreviewText == "" {
			t.Fatalf("not a collapsed child panel: %+v", card)
		}
	}
	emitChildNotice(as, "child-one", "completed", "FIRST SUMMARY")
	first := awaitTrackedPermissionUpdate(t, p)
	if first.handle != 0 || first.card.PreviewText != "FIRST SUMMARY" || first.card.Header.Color != "green" || !strings.Contains(first.card.RenderText(), "FIRST SUMMARY") {
		t.Fatalf("wrong child card updated: %+v", first)
	}
	emitChildNotice(as, "child-two", "failed", "SECOND FAILURE")
	second := awaitTrackedPermissionUpdate(t, p)
	if second.handle != 1 || second.card.CollapsedDetailsTitle != "" || second.card.Header.Color != "red" {
		t.Fatalf("failure not expanded on second card: %+v", second)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sentCards) != 2 {
		t.Fatalf("lifecycle created duplicate cards: %d", len(p.sentCards))
	}
}

func TestSharedSubagentCards_SlowUpdateDoesNotBlockMainOutput(t *testing.T) {
	env, p := newSharedTrackedPermissionEnv(t)
	p.updateStarted, p.updateRelease = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(p.updateRelease) })
	as := env.a.connection("first")
	emitChildNotice(as, "child", "running", "")
	env.await("task child")
	emitChildNotice(as, "child", "completed", "DONE")
	select {
	case <-p.updateStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("child update did not start")
	}
	as.emit(Event{Type: EventText, Content: "MAIN OUTPUT WHILE CHILD CARD UPDATES", Metadata: map[string]any{"phase": "commentary"}})
	env.await("MAIN OUTPUT WHILE CHILD CARD UPDATES")
}

func TestSubagentPreview_CompactAndTracksLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		notice SubagentNotice
		want   string
	}{
		{"running", SubagentNotice{Status: "running", Description: "\n检查  协议\n详细任务", Detail: "旧结果"}, "检查 协议"},
		{"completed", SubagentNotice{Status: "completed", Description: "任务", Detail: "\n已修复两处问题\n更多结果"}, "已修复两处问题"},
		{"failed", SubagentNotice{Status: "failed", Detail: "连接失败\r请重试"}, "连接失败"},
		{"fallback", SubagentNotice{Status: "completed", Description: "任务", Detail: "  "}, "任务"},
		{"unicode limit", SubagentNotice{Status: "running", Description: strings.Repeat("测", 80)}, strings.Repeat("测", 47) + "…"},
		{"empty", SubagentNotice{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := subagentPreview(tc.notice); got != tc.want {
				t.Fatalf("preview=%q want=%q", got, tc.want)
			}
		})
	}
}
