package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type inlineChildPlatform struct {
	observedToolOutputPlatform
	edits []inlineChildEdit
}
type inlineChildEdit struct {
	handle  any
	content string
}

func (p *inlineChildPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	p.mu.Lock()
	p.edits = append(p.edits, inlineChildEdit{handle, content})
	p.mu.Unlock()
	return p.observedToolOutputPlatform.UpdateMessage(ctx, handle, content)
}
func runSubagentInlineJourney(t *testing.T) {
	p := &inlineChildPlatform{observedToolOutputPlatform: observedToolOutputPlatform{toolOutputRichPlatform: toolOutputRichPlatform{stubRichCardSilentPlatform: stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}}
	a := &sharedTestAgent{}
	e := NewEngine("inline", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.display.CardMode, e.display.ToolMessages = "rich", true
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	await := func(text string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(strings.Join(p.getSent(), "\n"), text) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("missing %q: %v", text, p.getSent())
	}
	send("/attach first")
	await("Attached to session first")
	as := a.connection("first")
	turn := as.RuntimeState().TurnID
	as.emit(Event{Type: EventTurnStarted, TurnID: turn})
	as.emit(Event{Type: EventToolUse, TurnID: turn, ItemID: "tool", ToolName: "Bash", ToolInput: "MAIN TOOL"})
	await("MAIN TOOL")
	child := func(id, status, detail string) {
		as.emit(Event{Type: EventNotice, Notice: &AgentNotice{Kind: "subagent", Subagent: &SubagentNotice{ID: id, TurnID: turn, Name: id, Description: "TASK " + id, Status: status, Detail: detail}}})
	}
	child("one", "running", "")
	await("TASK one")
	child("two", "running", "")
	await("TASK two")
	starts, _, _, _ := p.snapshot()
	if len(starts) != 1 {
		t.Fatalf("children created extra cards: %d", len(starts))
	}
	send("/steer keep working")
	await("STEER observed: keep working")
	as.emit(Event{Type: EventResult, TurnID: turn, Content: "FIRST FINAL ANSWER", Done: true})
	await("FIRST FINAL ANSWER")
	as.mu.Lock()
	as.turn = "next-turn"
	as.mu.Unlock()
	as.emit(Event{Type: EventTurnStarted, TurnID: "next-turn"})
	as.emit(Event{Type: EventToolUse, TurnID: "next-turn", ItemID: "next-tool", ToolName: "Bash", ToolInput: "NEXT TOOL"})
	await("NEXT TOOL")
	child("one", "completed", "LATE RESULT ONE")
	await("LATE RESULT ONE")
	child("two", "failed", "LATE FAILURE TWO")
	await("LATE FAILURE TWO")
	starts, _, _, _ = p.snapshot()
	if len(starts) != 2 {
		t.Fatalf("late children created extra cards: %d", len(starts))
	}
	p.mu.Lock()
	edits := append([]inlineChildEdit(nil), p.edits...)
	p.mu.Unlock()
	lateEdits := 0
	for _, edit := range edits {
		if strings.Contains(edit.content, "LATE RESULT ONE") || strings.Contains(edit.content, "LATE FAILURE TWO") {
			lateEdits++
			if edit.handle != "handle-1" || !strings.Contains(edit.content, "FIRST FINAL ANSWER") || !strings.Contains(edit.content, "MAIN TOOL") || strings.Contains(edit.content, "NEXT TOOL") {
				t.Fatalf("late update lost original card or mixed turns: %+v", edit)
			}
		}
	}
	if lateEdits < 1 {
		t.Fatal("late child status was sent separately instead of updating the parent card")
	}
	send("/history 10")
	await("FIRST FINAL ANSWER")
	send("/detach")
	await(e.i18n.T(MsgSharedDetached))
}

func TestSubagentPanels_ChildIDsDoNotMatchToolInvocations(t *testing.T) {
	steps := []ToolStep{{ItemID: "same", Kind: ToolStepKindSubagent, Summary: "CHILD"}}
	steps = mergeLiveToolOutput(steps, Event{ItemID: "same", ToolName: "Bash", Content: "SHELL"}, 100)
	if len(steps) != 2 || steps[0].Summary != "CHILD" || steps[0].Result != "" {
		t.Fatalf("tool overwrote child: %+v", steps)
	}
}
