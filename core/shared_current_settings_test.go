package core

import (
	"context"
	"strings"
	"testing"
)

type liveSettingsTestSession struct{ *sharedTestSession }

func (*liveSettingsTestSession) CurrentModels(context.Context) ([]ModelOption, error) {
	return []ModelOption{{Name: "server-model"}, {Name: "target-model"}}, nil
}
func (*liveSettingsTestSession) CurrentReasoningEfforts(context.Context) ([]string, error) {
	return []string{"medium", "extra high"}, nil
}
func (s *liveSettingsTestSession) SetCurrentModel(_ context.Context, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.Model = model
	return nil
}
func (s *liveSettingsTestSession) SetCurrentReasoningEffort(_ context.Context, effort string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.ReasoningEffort = effort
	return nil
}

type liveSettingsTestAgent struct{ sharedSettingsTestAgent }

func (a *liveSettingsTestAgent) AttachSession(ctx context.Context, id string) (AgentSession, error) {
	as, err := a.sharedTestAgent.AttachSession(ctx, id)
	if err != nil {
		return nil, err
	}
	s := as.(*sharedTestSession)
	s.settings = AgentRuntimeSettings{Model: "server-model", ReasoningEffort: "medium"}
	return &liveSettingsTestSession{s}, nil
}

func newLiveSettingsEnv(t *testing.T) (*sharedTestEnv, *liveSettingsTestAgent, *sharedTestSession) {
	env := newSharedTestEnv(t)
	a := &liveSettingsTestAgent{sharedSettingsTestAgent{model: "future-model", effort: "low"}}
	env.e.agent = a
	env.send("/attach first")
	env.await("Attached to session first")
	as := a.connection("first")
	env.e.sessions.GetOrCreateActive("test:user").AddHistory("user", "KEEP HISTORY")
	return env, a, as
}

func TestCurrentSettingsCardActionsChangeSessionAndPreserveDefaults(t *testing.T) {
	env, a, as := newLiveSettingsEnv(t)
	for _, action := range []string{"nav:/model", "act:/model switch 2", "nav:/reasoning", "act:/reasoning 2"} {
		card := env.e.handleCardNav(action, "test:user")
		if card == nil || !strings.Contains(card.RenderText(), "Current session") || strings.Contains(card.RenderText(), "No attached runtime") {
			t.Fatalf("current-session card action failed: %s: %+v", action, card)
		}
	}
	settings, err := as.ReadRuntimeSettings(context.Background())
	if err != nil || settings.Model != "target-model" || settings.ReasoningEffort != "extra high" {
		t.Fatalf("card action did not update attached session: %+v, %v", settings, err)
	}
	if a.GetModel() != "future-model" || a.GetReasoningEffort() != "low" {
		t.Fatal("current-session card changed creation defaults")
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestCurrentSettingsInvalidChoicesPreserveCurrentSession(t *testing.T) {
	env, _, as := newLiveSettingsEnv(t)
	for _, action := range []string{"act:/model switch 999", "act:/reasoning invalid"} {
		card := env.e.handleCardNav(action, "test:user")
		if card == nil || !strings.Contains(card.RenderText(), "Error") {
			t.Fatalf("invalid choice not rejected: %s", action)
		}
	}
	settings, _ := as.ReadRuntimeSettings(context.Background())
	if settings.Model != "server-model" || settings.ReasoningEffort != "medium" {
		t.Fatalf("invalid choice changed session: %+v", settings)
	}
	assertSharedSettingsPreserved(t, env, as)
}
