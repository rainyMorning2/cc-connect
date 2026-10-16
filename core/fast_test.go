package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fastTestAgent struct {
	cujAgent
	tierMu sync.Mutex
	tier   string
}

func (a *fastTestAgent) GetServiceTier() string {
	a.tierMu.Lock()
	defer a.tierMu.Unlock()
	return a.tier
}
func (a *fastTestAgent) SetServiceTier(_ context.Context, tier string) error {
	a.tierMu.Lock()
	defer a.tierMu.Unlock()
	a.tier = tier
	return nil
}
func (a *fastTestAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	base, err := a.cujAgent.StartSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &fastTestSession{cujAgentSession: base.(*cujAgentSession), tier: a.GetServiceTier()}, nil
}

type fastTestSession struct {
	*cujAgentSession
	tier    string
	tierErr error
}

func (s *fastTestSession) GetServiceTier() string { s.mu.Lock(); defer s.mu.Unlock(); return s.tier }
func (s *fastTestSession) SetServiceTier(_ context.Context, tier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tierErr != nil {
		return s.tierErr
	}
	s.tier = tier
	return nil
}
func (s *fastTestSession) GetModel() string           { return "test-model" }
func (s *fastTestSession) GetReasoningEffort() string { return "high" }

func TestReplyFooterFast_SessionOverridesDefaultsAndAllLayouts(t *testing.T) {
	e := newLegacyFooterEngine()
	e.ctx = context.Background()
	e.i18n = NewI18n(LangEnglish)
	a := &fastTestAgent{tier: "fast"}
	s := &fastTestSession{cujAgentSession: newCUJAgentSession()}
	for _, tier := range []string{"", "default", "priority", "fast"} {
		s.tier = tier
		wantFast := tier == "priority" || tier == "fast"
		legacy := e.buildReplyFooter(a, s, "", "")
		rich := e.composeRichStatusFooter(false, time.Now(), a, s, "")
		for _, footer := range []string{legacy, rich} {
			if strings.Contains(footer, " · fast") != wantFast {
				t.Fatalf("tier %q: footer=%q", tier, footer)
			}
			if wantFast && !strings.Contains(footer, "test-model · high · fast") {
				t.Fatalf("wrong Fast position: %q", footer)
			}
		}
	}
	if replyFooterFast(nil, a) != "fast" {
		t.Fatal("missing default before session creation")
	}
	e.SetReplyFooterEnabled(false)
	if got := e.buildReplyFooter(a, s, "", ""); got != "" {
		t.Fatalf("disabled footer = %q", got)
	}
}

func TestFastCommand_ValidationUnsupportedAndRejection(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &fastTestAgent{}
	e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	msg := &Message{SessionKey: "test:user", Platform: "test", UserID: "user"}
	e.cmdFast(p, msg, []string{"invalid"})
	if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgFastUsage)) {
		t.Fatal("missing usage")
	}
	e.agent = &stubAgent{}
	e.cmdFast(p, msg, nil)
	if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgFastNotSupported)) {
		t.Fatal("missing unsupported reply")
	}
	e.agent = a
	s := &fastTestSession{cujAgentSession: newCUJAgentSession(), tierErr: errors.New("tier denied")}
	e.interactiveStates[msg.SessionKey] = &interactiveState{agentSession: s}
	e.cmdFast(p, msg, []string{"on"})
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "tier denied") || s.GetServiceTier() != "" {
		t.Fatal("rejection was not preserved")
	}
}

func TestFastCommand_ToggleStatusAndDisabled(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	a := &fastTestAgent{}
	e := NewEngine("test", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	send := func(text string) string {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", UserID: "user", Content: text})
		sent := p.getSent()
		return sent[len(sent)-1]
	}
	if text := send("/fast status"); !strings.Contains(text, e.i18n.T(MsgFastInherit)) {
		t.Fatalf("inherited status = %s", text)
	}
	if text := send("/fast"); !strings.Contains(text, e.i18n.T(MsgFastOn)) {
		t.Fatalf("toggle on = %s", text)
	}
	if text := send("/fast status"); !strings.Contains(text, e.i18n.T(MsgFastOn)) {
		t.Fatalf("status toggled tier = %s", text)
	}
	if text := send("/fast"); !strings.Contains(text, e.i18n.T(MsgFastOff)) {
		t.Fatalf("toggle off = %s", text)
	}
	e.disabledCmds = map[string]bool{"fast": true}
	if text := send("/fast on"); !strings.Contains(text, e.i18n.Tf(MsgCommandDisabled, "/fast")) {
		t.Fatalf("disabled command = %s", text)
	}
	if a.GetServiceTier() != "default" {
		t.Fatal("disabled command changed tier")
	}
}
