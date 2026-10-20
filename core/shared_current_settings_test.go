package core

import (
	"context"
	"fmt"
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

func TestCurrentSettingsMenusOnlyOfferServerChoices(t *testing.T) {
	for _, command := range []string{"model", "reasoning"} {
		t.Run(command, func(t *testing.T) {
			env, _, as := newLiveSettingsEnv(t)
			menu := env.e.handleCardNav("nav:/"+command, "test:user")
			var values []string
			for _, el := range menu.Elements {
				if row, ok := el.(CardActions); ok {
					for _, button := range row.Buttons {
						if strings.Contains(button.Value, "reset-default") {
							t.Fatal("attached menu offers a creation-default reset")
						}
					}
				}
				if sel, ok := el.(CardSelect); ok {
					for _, option := range sel.Options {
						values = append(values, option.Value)
					}
				}
			}
			want := []string{"act:/model switch server-model", "act:/model switch target-model"}
			if command == "reasoning" {
				want = []string{"act:/reasoning medium", "act:/reasoning extra high"}
			}
			if strings.Join(values, "|") != strings.Join(want, "|") {
				t.Fatalf("menu choices differ from server list: %v, want %v", values, want)
			}
			assertSharedSettingsPreserved(t, env, as)
		})
	}
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

type changingSettingsSession struct {
	*liveSettingsTestSession
	models  []ModelOption
	efforts []string
}

func (s *changingSettingsSession) CurrentModels(context.Context) ([]ModelOption, error) {
	return s.models, nil
}
func (s *changingSettingsSession) CurrentReasoningEfforts(context.Context) ([]string, error) {
	return s.efforts, nil
}

func TestCurrentSettings_StaleSelectorsPreserveSelectedValue(t *testing.T) {
	for _, command := range []string{"model", "reasoning"} {
		for _, removed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/removed=%v", command, removed), func(t *testing.T) {
				env, _, as := newLiveSettingsEnv(t)
				s := &changingSettingsSession{liveSettingsTestSession: &liveSettingsTestSession{as}, models: []ModelOption{{Name: "server-model"}, {Name: "target-model"}}, efforts: []string{"medium", "extra high"}}
				env.e.interactiveMu.Lock()
				state := env.e.interactiveStates[env.e.interactiveKeyForSessionKey("test:user")]
				env.e.interactiveMu.Unlock()
				state.mu.Lock()
				state.agentSession = s
				state.mu.Unlock()
				card := env.e.handleCardNav("nav:/"+command, "test:user")
				target := "extra high"
				if command == "model" {
					target = "target-model"
				}
				action := ""
				for _, el := range card.Elements {
					if sel, ok := el.(CardSelect); ok {
						for _, option := range sel.Options {
							if strings.Contains(option.Text, target) {
								action = option.Value
							}
						}
					}
				}
				if action == "" {
					t.Fatal("no selector action")
				}
				// Another client changes the model/catalog between display and click.
				s.models = []ModelOption{{Name: "target-model"}, {Name: "server-model"}}
				s.efforts = []string{"extra high", "medium"}
				if removed {
					s.models = s.models[1:]
					s.efforts = s.efforts[1:]
				}
				result := env.e.handleCardNav(action, "test:user").RenderText()
				settings, _ := as.ReadRuntimeSettings(context.Background())
				actual := settings.ReasoningEffort
				if command == "model" {
					actual = settings.Model
				}
				if removed {
					if !strings.Contains(result, "Error") || actual == target {
						t.Fatalf("obsolete choice not rejected: %s, %+v", result, settings)
					}
				} else if actual != target {
					t.Fatalf("clicked %s (%s), applied %s", target, action, actual)
				}
			})
		}
	}
}
