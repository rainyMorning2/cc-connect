package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type sharedSettingsTestAgent struct {
	sharedTestAgent
	model, effort, mode string
	providers           []ProviderConfig
	provider            string
}

type serverDefaultsSettingsAgent struct {
	*sharedSettingsTestAgent
	defaults   AgentRuntimeSettings
	defaultErr error
}

func (a *serverDefaultsSettingsAgent) ReadDefaultSettings(context.Context) (AgentRuntimeSettings, error) {
	return a.defaults, a.defaultErr
}

func TestDefaultSettingsSummary_UnattachedReadsServerDefaultsWithoutChangingOverrides(t *testing.T) {
	env := newSharedTestEnv(t)
	a := &serverDefaultsSettingsAgent{
		sharedSettingsTestAgent: &sharedSettingsTestAgent{model: "override-model", effort: "high"},
		defaults:                AgentRuntimeSettings{Model: "server-model", ReasoningEffort: "medium"},
	}
	env.e.agent = a
	for _, command := range []string{"model", "reasoning"} {
		text := env.e.renderDefaultSettingsCard(a, "test:user", command).RenderText()
		for _, want := range []string{"No attached runtime", "Server defaults (reported by server)", "Model: server-model · Reasoning: medium", "Model: override-model · Reasoning: high"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s menu missing %q: %s", command, want, text)
			}
		}
	}
	if a.GetModel() != "override-model" || a.GetReasoningEffort() != "high" {
		t.Fatal("reading defaults changed overrides")
	}
}

func TestDefaultSettingsSummary_ServerFailureDoesNotFabricateDefaults(t *testing.T) {
	env := newSharedTestEnv(t)
	a := &serverDefaultsSettingsAgent{
		sharedSettingsTestAgent: &sharedSettingsTestAgent{model: "override-model", effort: "high"},
		defaultErr:              fmt.Errorf("service unavailable"),
	}
	env.e.agent = a
	text := env.e.defaultSettingsSummary(a, "test:user")
	if !strings.Contains(text, "Could not read server defaults: service unavailable") || strings.Contains(text, "Server defaults (reported by server)") {
		t.Fatalf("server failure hidden or defaults fabricated: %s", text)
	}
	a.defaults.Model = "reported-model"
	text = env.e.defaultSettingsSummary(a, "test:user")
	if !strings.Contains(text, "Model: reported-model · Reasoning: not reported by server") || !strings.Contains(text, "service unavailable") {
		t.Fatalf("partial defaults discarded or guessed: %s", text)
	}
}

type sharedPermissionPresetTestAgent struct{ *sharedSettingsTestAgent }

func (a *sharedPermissionPresetTestAgent) PermissionModes() []PermissionModeInfo {
	return append([]PermissionModeInfo{
		{Key: "default", NameKey: MsgPermDefault},
		{Key: "ask-for-approval", NameKey: MsgPermAsk},
		{Key: "approve-for-me", NameKey: MsgPermAutoReview},
		{Key: "full-access", NameKey: MsgPermFullAccess},
		{Key: "read-only", NameKey: MsgPermReadOnly},
	}, a.sharedSettingsTestAgent.PermissionModes()...)
}

func TestDefaultModeCard_LocalizesPermissionPresets(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	agent := &sharedPermissionPresetTestAgent{a}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		env.e.i18n.SetLang(lang)
		card := env.e.renderDefaultSettingsCard(agent, "test:user", "mode")
		found := map[string]string{}
		for _, element := range card.Elements {
			if row, ok := element.(CardSelect); ok {
				for _, option := range row.Options {
					found[option.Value] = option.Text
				}
			}
		}
		for _, mode := range agent.PermissionModes() {
			if mode.NameKey != "" {
				want := env.e.i18n.T(mode.NameKey)
				if got := found["act:/mode "+mode.Key]; got != want || want == string(mode.NameKey) {
					t.Errorf("%s mode %s label=%q, want %q", lang, mode.Key, got, want)
				}
			}
		}
	}
}

func (*sharedSettingsTestAgent) DefaultSettingsOnly() bool { return true }
func (a *sharedSettingsTestAgent) SetModel(v string)       { a.mu.Lock(); defer a.mu.Unlock(); a.model = v }
func (a *sharedSettingsTestAgent) GetModel() string        { a.mu.Lock(); defer a.mu.Unlock(); return a.model }
func (*sharedSettingsTestAgent) AvailableModels(context.Context) []ModelOption {
	return []ModelOption{{Name: "default-model", Alias: "default"}, {Name: "new-model", Alias: "new"}}
}

type resetEffortSettingsAgent struct{ *sharedSettingsTestAgent }

func (a *resetEffortSettingsAgent) SetModel(model string) {
	a.sharedSettingsTestAgent.SetModel(model)
	a.SetReasoningEffort("")
}

func TestCreationDefaults_ResetModelAndReasoningIndependently(t *testing.T) {
	env, base, as := newSharedSettingsEnv(t)
	a := &resetEffortSettingsAgent{base}
	env.e.agent = a
	a.SetProviders([]ProviderConfig{{Name: "default-provider", Model: "provider-model"}})
	var savedModel, savedProvider, savedEffort []string
	env.e.SetModelSaveFunc(func(v string) error { savedModel = append(savedModel, v); return nil })
	env.e.providerModelSaveFunc = func(name, v string) error {
		savedProvider = append(savedProvider, name+":"+v)
		return nil
	}
	env.e.SetReasoningSaveFunc(func(v string) error { savedEffort = append(savedEffort, v); return nil })
	card := env.e.handleCardNav("act:/model reset-default", "test:user")
	if card == nil || !strings.Contains(card.RenderText(), "New-thread defaults updated") {
		t.Fatalf("reset failed: %+v", card)
	}
	if a.GetModel() != "" || a.GetActiveProvider().Model != "" || a.GetReasoningEffort() != "low" {
		t.Fatal("model reset did not clear effective override or changed effort")
	}
	if len(savedModel) != 1 || savedModel[0] != "" || len(savedProvider) != 1 || savedProvider[0] != "default-provider:" || len(savedEffort) != 0 {
		t.Fatalf("incorrect model persistence: %v %v %v", savedModel, savedProvider, savedEffort)
	}
	a.SetModel("new-model")
	a.SetReasoningEffort("high")
	env.send("/reasoning reset-default")
	if a.GetModel() != "new-model" || a.GetReasoningEffort() != "" || a.GetMode() != "suggest" {
		t.Fatal("reasoning reset changed model or permission mode")
	}
	if len(savedEffort) != 1 || savedEffort[0] != "" {
		t.Fatalf("reasoning reset not persisted: %v", savedEffort)
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestCreationDefaults_ResetSaveFailurePreservesSettings(t *testing.T) {
	for _, command := range []string{"model", "reasoning"} {
		t.Run(command, func(t *testing.T) {
			env, a, as := newSharedSettingsEnv(t)
			fail := func(string) error { return fmt.Errorf("cannot save default") }
			env.e.SetModelSaveFunc(fail)
			env.e.SetReasoningSaveFunc(fail)
			card := env.e.handleCardNav("act:/"+command+" reset-default", "test:user")
			if card == nil || !strings.Contains(card.RenderText(), "cannot save default") {
				t.Fatalf("save failure hidden: %+v", card)
			}
			if a.GetModel() != "default-model" || a.GetReasoningEffort() != "low" {
				t.Fatal("failed save changed defaults")
			}
			assertSharedSettingsPreserved(t, env, as)
		})
	}
}

func TestCreationDefaults_ServerDefaultMenuOptionAndSelection(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	a.SetModel("")
	a.SetReasoningEffort("")
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		env.e.i18n.SetLang(lang)
		for _, command := range []string{"model", "reasoning"} {
			card := env.e.renderCreationSettingsCard(a, "test:user", command)
			found := false
			for _, el := range card.Elements {
				if sel, ok := el.(CardSelect); ok {
					if sel.InitValue != "act:/"+command+" reset-default" {
						t.Fatalf("reset is not selected: %+v", sel)
					}
					for _, opt := range sel.Options {
						if opt.Value == "act:/"+command+" reset-default" && opt.Text == env.e.i18n.T(MsgFollowServerDefault) {
							found = true
						}
					}
				}
			}
			if !found || env.e.i18n.T(MsgFollowServerDefault) == string(MsgFollowServerDefault) {
				t.Fatalf("%s %s lacks translated reset option", lang, command)
			}
		}
	}
}
func (a *sharedSettingsTestAgent) SetReasoningEffort(v string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.effort = v
}
func (a *sharedSettingsTestAgent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}
func (*sharedSettingsTestAgent) AvailableReasoningEfforts() []string { return []string{"low", "high"} }
func (a *sharedSettingsTestAgent) SetMode(v string)                  { a.mu.Lock(); defer a.mu.Unlock(); a.mode = v }
func (a *sharedSettingsTestAgent) GetMode() string                   { a.mu.Lock(); defer a.mu.Unlock(); return a.mode }
func (*sharedSettingsTestAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "suggest", Name: "Suggest"}, {Key: "yolo", Name: "Yolo"}}
}
func (a *sharedSettingsTestAgent) ListProviders() []ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ProviderConfig(nil), a.providers...)
}
func (a *sharedSettingsTestAgent) SetProviders(v []ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = append([]ProviderConfig(nil), v...)
}
func (a *sharedSettingsTestAgent) SetActiveProvider(v string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.providers {
		if p.Name == v {
			a.provider = v
			return true
		}
	}
	if v == "" {
		a.provider = v
		return true
	}
	return false
}
func (a *sharedSettingsTestAgent) GetActiveProvider() *ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.providers {
		if p.Name == a.provider {
			copy := p
			return &copy
		}
	}
	return nil
}

func newSharedSettingsEnv(t *testing.T) (*sharedTestEnv, *sharedSettingsTestAgent, *sharedTestSession) {
	env := newSharedTestEnv(t)
	a := &sharedSettingsTestAgent{model: "default-model", effort: "low", mode: "suggest", providers: []ProviderConfig{{Name: "default-provider"}, {Name: "new-provider"}}, provider: "default-provider"}
	env.e.agent = a
	env.send("/attach first")
	env.await("Attached to session first")
	as := a.connection("first")
	as.mu.Lock()
	as.settings = AgentRuntimeSettings{Model: "server-model", ReasoningEffort: "medium", Provider: "server-provider", ApprovalPolicy: "on-request", Sandbox: "read-only"}
	as.mu.Unlock()
	env.e.sessions.GetOrCreateActive("test:user").AddHistory("user", "KEEP HISTORY")
	return env, a, as
}

func assertSharedSettingsPreserved(t *testing.T, env *sharedTestEnv, as *sharedTestSession) {
	t.Helper()
	session := env.e.sessions.GetOrCreateActive("test:user")
	if session.GetAgentSessionID() != "first" {
		t.Fatal("settings command cleared attached thread ID")
	}
	if history := session.GetHistory(0); len(history) != 1 || history[0].Content != "KEEP HISTORY" {
		t.Fatalf("settings command cleared history: %v", history)
	}
	state := as.RuntimeState()
	if !state.Connected || state.TurnID != "active-first" {
		t.Fatalf("settings command changed attached runtime: %+v", state)
	}
	key := env.e.interactiveKeyForSessionKey("test:user")
	env.e.interactiveMu.Lock()
	live := env.e.interactiveStates[key]
	env.e.interactiveMu.Unlock()
	if live == nil {
		t.Fatal("settings command removed event reader")
	}
}

func TestSharedSettingsTextCommandsPreserveObserverHistoryAndThread(t *testing.T) {
	for _, command := range []string{"/model switch new", "/reasoning high", "/mode yolo", "/provider switch new-provider", "/provider clear"} {
		t.Run(command, func(t *testing.T) {
			env, agent, as := newSharedSettingsEnv(t)
			env.send(command)
			env.await("New-thread defaults updated")
			env.await("Model: server-model · Reasoning: medium · Provider: server-provider")
			assertSharedSettingsPreserved(t, env, as)
			switch command {
			case "/model switch new":
				if agent.GetModel() != "new-model" {
					t.Fatal("model default did not change")
				}
			case "/reasoning high":
				if agent.GetReasoningEffort() != "high" {
					t.Fatal("reasoning default did not change")
				}
			case "/mode yolo":
				if agent.GetMode() != "yolo" {
					t.Fatal("mode default did not change")
				}
			case "/provider switch new-provider":
				if agent.GetActiveProvider().Name != "new-provider" {
					t.Fatal("provider default did not change")
				}
			case "/provider clear":
				if agent.GetActiveProvider() != nil {
					t.Fatal("provider default not cleared")
				}
			}
			as.emit(Event{Type: EventText, Content: "LIVE AFTER " + command, Metadata: map[string]any{"phase": "commentary"}})
			env.await("LIVE AFTER " + command)
		})
	}
}

func TestSharedSettingsCardActionsPreserveObserverHistoryAndThread(t *testing.T) {
	for _, action := range []string{"act:/model switch 2", "act:/reasoning 2", "act:/mode yolo", "act:/provider new-provider", "act:/provider clear"} {
		t.Run(action, func(t *testing.T) {
			env, _, as := newSharedSettingsEnv(t)
			card := env.e.handleCardNav(action, "test:user")
			if card == nil || !strings.Contains(card.RenderText(), "New-thread defaults updated") || !strings.Contains(card.RenderText(), "server-model") {
				t.Fatalf("ambiguous card feedback: %+v", card)
			}
			assertSharedSettingsPreserved(t, env, as)
		})
	}
}

func TestSharedSettingsInvalidInputAndPersistenceFailureLeaveDefaultsUnchanged(t *testing.T) {
	env, a, as := newSharedSettingsEnv(t)
	for _, command := range []string{"/reasoning invalid", "/mode invalid", "/provider switch missing"} {
		env.send(command)
	}
	env.e.providerSaveFunc = func(string) error { return fmt.Errorf("simulated save failure") }
	env.send("/provider switch new-provider")
	env.await("simulated save failure")
	if a.GetModel() != "default-model" || a.GetReasoningEffort() != "low" || a.GetMode() != "suggest" || a.GetActiveProvider().Name != "default-provider" {
		t.Fatal("invalid input changed defaults")
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsReadFailureDoesNotPresentDefaultsAsRuntime(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	as.mu.Lock()
	as.settingsErr = fmt.Errorf("simulated read failure")
	as.mu.Unlock()
	env.send("/model")
	env.await("Could not read the attached thread settings")
	if strings.Contains(env.visible(), "Attached thread (reported by server)") {
		t.Fatal("read failure pretended defaults were runtime settings")
	}
	env.await("New-thread defaults (CC Connect)")
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsDoNotRestoreOldSessionProviderOverNewDefaults(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	session := env.e.sessions.GetOrCreateActive("test:user")
	session.SetActiveProvider("default-provider")
	a.SetActiveProvider("new-provider")
	restoreActiveProviderFromSession(a, session)
	if a.GetActiveProvider().Name != "new-provider" {
		t.Fatal("stored provider for attached thread replaced new-thread default")
	}
}

func TestSharedSettingsPlainTextShowsRuntimeAndNumberedCreationDefaults(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	p := &stubPlatformEngine{n: "plain"}
	msg := &Message{SessionKey: "test:user", ReplyCtx: "reply"}
	env.e.cmdModel(p, msg, nil)
	visible := strings.Join(p.getSent(), "\n")
	for _, want := range []string{"Attached thread (reported by server)", "server-model", "New-thread defaults (CC Connect)", "1. default-model", "2. new-model"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("plain query missing %q: %s", want, visible)
		}
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedSettingsCardValidationErrorDoesNotChangeDefaults(t *testing.T) {
	env, a, as := newSharedSettingsEnv(t)
	for _, action := range []string{"act:/reasoning invalid", "act:/mode invalid", "act:/provider missing"} {
		card := env.e.handleCardNav(action, "test:user")
		if card == nil || !strings.Contains(card.RenderText(), "Error") {
			t.Fatalf("invalid card action did not show error: %v", card)
		}
	}
	if a.GetReasoningEffort() != "low" || a.GetMode() != "suggest" || a.GetActiveProvider().Name != "default-provider" {
		t.Fatal("invalid card action changed defaults")
	}
	assertSharedSettingsPreserved(t, env, as)
}

func TestDefaultSettingsSelectors_BindValuesNotIndices(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	for command, want := range map[string]string{"model": "act:/model switch new-model", "reasoning": "act:/reasoning high"} {
		card := env.e.renderDefaultSettingsCard(a, "test:user", command)
		found := false
		for _, el := range card.Elements {
			if sel, ok := el.(CardSelect); ok {
				for _, option := range sel.Options {
					if option.Value == want {
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("%s selector does not bind stable value %q: %+v", command, want, card)
		}
	}
}
