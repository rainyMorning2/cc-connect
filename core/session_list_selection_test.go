package core

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func firstSessionAction(t *testing.T, card *Card) string {
	t.Helper()
	for _, element := range card.Elements {
		if row, ok := element.(CardListItem); ok && strings.HasPrefix(row.BtnValue, "act:/switch ") {
			return row.BtnValue
		}
	}
	t.Fatal("missing session switch action")
	return ""
}

func TestSessionListCardSwitch_StableIDSurvivesReorderingAndRemoval(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "reordered", true: "removed with prefix collision"}[removed], func(t *testing.T) {
			a := &stubListAgent{sessions: []AgentSessionInfo{{ID: "target", Summary: "TARGET"}, {ID: "other", Summary: "OTHER"}}}
			p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			defer e.Stop()
			e.sessions.SwitchToAgentSession("test:user", "current", a.Name(), "CURRENT")
			card, err := e.renderListCard("test:user", 1)
			if err != nil {
				t.Fatal(err)
			}
			action := firstSessionAction(t, card)
			if removed {
				a.sessions = []AgentSessionInfo{{ID: "target-other", Summary: "TARGET copy"}, {ID: "other", Summary: "OTHER"}}
			} else {
				a.sessions = []AgentSessionInfo{a.sessions[1], a.sessions[0]}
			}
			e.handleCardNav(action, "test:user")
			want := "target"
			if removed {
				want = "current"
			}
			if got := e.sessions.GetOrCreateActive("test:user").GetAgentSessionID(); got != want {
				t.Fatalf("clicked target but switched to %q, want %q (action %q)", got, want, action)
			}
		})
	}
}

type reorderedSharedListAgent struct {
	sharedTestAgent
	listMu sync.Mutex
	list   []AgentSessionInfo
}

func (a *reorderedSharedListAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	a.listMu.Lock()
	defer a.listMu.Unlock()
	return append([]AgentSessionInfo(nil), a.list...), nil
}
func runStableSharedSessionListJourney(t *testing.T) {
	a := &reorderedSharedListAgent{list: []AgentSessionInfo{{ID: "first", Summary: "First"}, {ID: "second", Summary: "Second"}}}
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("list", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	send("/attach second")
	send("/list")
	p.mu.Lock()
	cards := append([]*Card(nil), p.sentCards...)
	cards = append(cards, p.repliedCards...)
	p.mu.Unlock()
	var action string
	for _, card := range cards {
		for _, el := range card.Elements {
			if row, ok := el.(CardListItem); ok && strings.HasPrefix(row.BtnValue, "act:/switch ") {
				action = row.BtnValue
				break
			}
		}
		if action != "" {
			break
		}
	}
	if action == "" {
		t.Fatal("list did not display selectable sessions")
	}
	a.listMu.Lock()
	a.list[0], a.list[1] = a.list[1], a.list[0]
	a.listMu.Unlock()
	before := len(p.getSent())
	// Send the command encoded by the displayed row through the real entrypoint.
	send(strings.TrimPrefix(action, "act:"))
	visible := strings.Join(p.getSent()[before:], "\n")
	if !strings.Contains(visible, "Attached to session first") {
		t.Fatalf("selected first row before reorder, got: %s", visible)
	}
	send("/detach")
}
