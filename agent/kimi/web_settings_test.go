package kimi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebSettings_ModelAndEffortDefaultsPreserveAttachedSession(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.runtimeStatus = map[string]any{"model": "fixture-model", "thinking_level": "high", "permission": "manual"}
	f.mu.Unlock()
	as, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	sw, ok := any(f.agent).(core.ModelSwitcher)
	if !ok {
		t.Fatal("/model cannot select new-session defaults")
	}
	models := sw.AvailableModels(context.Background())
	if len(models) != 2 || models[1].Name != "alternate-model" {
		t.Fatalf("models not read from server: %+v", models)
	}
	effort, ok := any(f.agent).(core.ReasoningEffortSwitcher)
	if !ok {
		t.Fatal("/effort cannot select new-session defaults")
	}
	sw.SetModel("alternate-model")
	if got := strings.Join(effort.AvailableReasoningEfforts(), ","); got != "off,max" {
		t.Fatalf("efforts not matched to selected model: %s", got)
	}
	effort.SetReasoningEffort("max")
	options := f.agent.WorkspaceAgentOptions()
	options["work_dir"] = t.TempDir()
	cloned, err := New(options)
	if err != nil {
		t.Fatalf("new-session defaults cannot survive config reload/workspace cloning: %v", err)
	}
	defer cloned.Stop()
	if cloned.(core.ModelSwitcher).GetModel() != "alternate-model" || cloned.(core.ReasoningEffortSwitcher).GetReasoningEffort() != "max" {
		t.Fatal("workspace cloning dropped new-session defaults")
	}
	reader, ok := as.(core.AgentRuntimeSettingsReader)
	if !ok {
		t.Fatal("/model incorrectly reports the active session as unattached")
	}
	settings, err := reader.ReadRuntimeSettings(context.Background())
	if err != nil || settings.Model != "fixture-model" || settings.ReasoningEffort != "high" {
		t.Fatalf("defaults overwrote current session settings: %+v, %v", settings, err)
	}
	f.mu.Lock()
	for _, request := range f.requests {
		if strings.Contains(request, "/profile") || request == "POST /api/v1/config" {
			f.mu.Unlock()
			t.Fatalf("defaults mutated existing/global settings: %s", request)
		}
	}
	f.mu.Unlock()
	created, err := f.agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer created.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creates) != 1 {
		t.Fatalf("creation requests: %+v", f.creates)
	}
	config, _ := f.creates[0]["agent_config"].(map[string]any)
	if config["model"] != "alternate-model" || config["thinking"] != "max" {
		t.Fatalf("new session ignored selected defaults: %+v", config)
	}
}

// The live server accepts agent_config during creation but can return an
// empty profile. Without initialization, the first prompt uses server defaults.
func TestWebStartSession_PreservesSelectedModelWhenCreateIgnoresAgentConfig(t *testing.T) {
	f := newWebFixture(t)
	f.agent.SetModel("alternate-model")
	f.agent.SetReasoningEffort("max")
	as, err := f.agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	settings, err := as.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
	if err != nil || settings.Model != "alternate-model" || settings.ReasoningEffort != "max" {
		t.Fatalf("new session lost selected settings before first prompt: %+v, %v", settings, err)
	}
	body, err := as.(*webSession).promptBody("first-prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if model, ok := body["model"]; ok && model != "alternate-model" {
		t.Fatalf("first prompt overrides selected model with server default: %v", model)
	}
}

func TestWebStartSession_RejectsUnconfirmedCreationSettings(t *testing.T) {
	for _, failure := range []string{"http", "model", "thinking"} {
		t.Run(failure, func(t *testing.T) {
			f := newWebFixture(t)
			f.agent.SetModel("alternate-model")
			f.agent.SetReasoningEffort("max")
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/profile") {
					if failure == "http" {
						http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
						return
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					record := f.snapshot.Session
					record.AgentConfig.Model = "alternate-model"
					record.AgentConfig.Thinking = "max"
					if failure == "model" {
						record.AgentConfig.Model = "fixture-model"
					} else {
						record.AgentConfig.Thinking = "low"
					}
					f.reply(w, record)
					return
				}
				f.serve(w, r)
			})
			if as, err := f.agent.StartSession(context.Background(), ""); err == nil {
				as.Close()
				t.Fatal("created session despite unconfirmed settings")
			}
		})
	}
}

func TestWebEngine_UnattachedMessageCreatesSessionDespiteStaleWorkspaceBinding(t *testing.T) {
	f := newWebFixture(t)
	path := filepath.Join(t.TempDir(), "sessions.json")
	sm := core.NewSessionManager(path)
	sm.GetOrCreateActive("test:user").SetAgentSessionID("session_stale", "kimi")
	sm.Save()
	p := &webWorkspacePlatform{}
	e := core.NewEngine("stale-workspace", f.agent, []core.Platform{p}, path, core.LangEnglish)
	defer e.Stop()
	e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", UserID: "user", Platform: "test", Content: "hi", ReplyCtx: "user"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.getSent(), "IM answer") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ordinary message did not automatically create a session: %s", p.getSent())
}

func TestWebEngine_ModelAndEffortCommandsBeforeAndAfterAttach(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.runtimeStatus = map[string]any{"model": "fixture-model", "thinking_level": "high"}
	f.mu.Unlock()
	p := &webWorkspacePlatform{}
	e := core.NewEngine("settings", f.agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	e.SetAdminFrom("user")
	defer e.Stop()
	send := func(command string) string {
		t.Helper()
		before := p.getSent()
		e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", UserID: "user", Platform: "test", Content: command, ReplyCtx: "user"})
		return strings.TrimPrefix(p.getSent(), before)
	}
	if text := send("/model"); !strings.Contains(text, "alternate-model") {
		t.Fatalf("unattached model selection missing: %s", text)
	}
	if text := send("/model switch alternate-model"); !strings.Contains(text, "New-thread defaults updated") {
		t.Fatalf("cannot choose model without attaching: %s", text)
	}
	if text := send("/effort"); !strings.Contains(text, "max") {
		t.Fatalf("model effort choices missing: %s", text)
	}
	if text := send("/effort max"); !strings.Contains(text, "Reasoning: max") {
		t.Fatalf("effort selection missing: %s", text)
	}
	if text := send("/attach session_test"); !strings.Contains(text, "Attached to session") {
		t.Fatal(text)
	}
	if text := send("/model"); !strings.Contains(text, "Model: fixture-model · Reasoning: high") || strings.Contains(text, "No attached runtime") {
		t.Fatalf("attached model settings reported as unattached: %s", text)
	}
	if text := send("/model switch alternate-model"); !strings.Contains(text, "Current session settings updated") || !strings.Contains(text, "Model: alternate-model") {
		t.Fatalf("cannot change current model while attached: %s", text)
	}
	if text := send("/effort off"); !strings.Contains(text, "Reasoning: off") || !strings.Contains(text, "Current session settings updated") {
		t.Fatalf("cannot change current effort while attached: %s", text)
	}
	if f.agent.GetModel() != "alternate-model" || f.agent.GetReasoningEffort() != "max" {
		t.Fatal("changing current settings overwrote future-session defaults")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.profiles) != 2 || len(f.peers) != 1 {
		t.Fatalf("profile updates=%d, observers=%d", len(f.profiles), len(f.peers))
	}
	for i, field := range []string{"model", "thinking"} {
		config, _ := f.profiles[i]["agent_config"].(map[string]any)
		if len(f.profiles[i]) != 1 || len(config) != 1 || config[field] == nil {
			t.Fatalf("profile update overwrote unrelated settings: %+v", f.profiles[i])
		}
	}
}

func TestWebValidateSession_TransientFailuresRetainStoredBinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want bool
	}{{"missing", 40401, false}, {"server failure", 50001, true}, {"authentication failure", 40101, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWebFixture(t)
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"code":%d,"msg":"fixture error","data":null}`, tc.code)
			})
			if got := f.agent.ValidateSessionID(context.Background(), "session_test"); got != tc.want {
				t.Fatalf("binding validity=%v, want %v", got, tc.want)
			}
		})
	}
}

// Creates one empty session and changes only that session's profile. No prompt
// or provider request is sent, and existing user sessions are left untouched.
func TestWebLiveSessionModelAndEffortChanges(t *testing.T) {
	addr := os.Getenv("CC_KIMI_WEB_TEST_URL")
	if addr == "" {
		t.Skip("set CC_KIMI_WEB_TEST_URL, token file and CWD for live settings checks")
	}
	a, err := New(map[string]any{"backend": "web", "web_url": addr, "web_token_file": os.Getenv("CC_KIMI_WEB_TEST_TOKEN_FILE"), "work_dir": os.Getenv("CC_KIMI_WEB_TEST_CWD")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	first, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.(core.AgentSessionAttacher).AttachSession(context.Background(), first.CurrentSessionID())
	if err != nil {
		t.Fatal(err)
	}
	models, err := first.(core.SessionModelSwitcher).CurrentModels(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("configured models unavailable: %+v, %v", models, err)
	}
	if err := first.(core.SessionModelSwitcher).SetCurrentModel(context.Background(), models[0].Name); err != nil {
		t.Fatal(err)
	}
	efforts, err := first.(core.SessionReasoningEffortSwitcher).CurrentReasoningEfforts(context.Background())
	if err != nil || len(efforts) == 0 {
		t.Fatalf("model has no effort options: %v, %v", efforts, err)
	}
	if err := first.(core.SessionReasoningEffortSwitcher).SetCurrentReasoningEffort(context.Background(), efforts[0]); err != nil {
		t.Fatal(err)
	}
	settings, err := second.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
	if err != nil || settings.Model != models[0].Name || settings.ReasoningEffort != efforts[0] {
		t.Fatalf("second client did not see profile changes: %+v, %v", settings, err)
	}
	t.Logf("session=%s, peer sees model=%s, effort=%s", first.CurrentSessionID(), settings.Model, settings.ReasoningEffort)
}

func TestWebSettings_CatalogFailureDoesNotReuseStaleEfforts(t *testing.T) {
	f := newWebFixture(t)
	if got := f.agent.AvailableReasoningEfforts(); len(got) == 0 {
		t.Fatal("fixture did not populate model cache")
	}
	original := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/models" {
			http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
			return
		}
		original.ServeHTTP(w, r)
	})
	if got := f.agent.AvailableReasoningEfforts(); len(got) != 0 {
		t.Fatalf("offered stale efforts after catalog failure: %v", got)
	}
}

func TestWebSettings_ModelAndEffortRejectUnconfirmedUpdates(t *testing.T) {
	for _, setting := range []string{"model", "thinking"} {
		t.Run(setting, func(t *testing.T) {
			f := newWebFixture(t)
			f.runtimeStatus = map[string]any{"model": "fixture-model", "thinking_level": "low"}
			original := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/profile") {
					f.mu.Lock()
					defer f.mu.Unlock()
					f.reply(w, f.snapshot.Session) // Accepted without applying the requested setting.
					return
				}
				original.ServeHTTP(w, r)
			})
			as, err := f.agent.AttachSession(context.Background(), "session_test")
			if err != nil {
				t.Fatal(err)
			}
			defer as.Close()
			if setting == "model" {
				err = as.(core.SessionModelSwitcher).SetCurrentModel(context.Background(), "alternate-model")
			} else {
				err = as.(core.SessionReasoningEffortSwitcher).SetCurrentReasoningEffort(context.Background(), "high")
			}
			if err == nil || !strings.Contains(err.Error(), "not confirmed") {
				t.Fatalf("unapplied %s reported success: %v", setting, err)
			}
		})
	}
}
