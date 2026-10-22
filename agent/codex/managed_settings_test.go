package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedDefaultSettings_ReadsScopedServerDefaultsWithoutThread(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		config                map[string]any
		wantModel, wantEffort string
	}{
		{"configured", map[string]any{"model": "configured-model", "model_reasoning_effort": "high"}, "configured-model", "high"},
		{"model catalog", map[string]any{}, "server-model", "medium"},
		{"configured model effort", map[string]any{"model": "configured-model"}, "configured-model", "low"},
		{"unknown model effort", map[string]any{"model": "unknown-model"}, "unknown-model", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			socket := managedFixture(t, func(ws *websocket.Conn) {
				fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
					switch m.Method {
					case "config/read":
						var params map[string]any
						if err := json.Unmarshal(m.Params, &params); err != nil {
							t.Error(err)
						}
						if params["cwd"] != cwd || params["includeLayers"] != false {
							t.Errorf("defaults use wrong directory scope: %v", params)
						}
						return map[string]any{"config": tc.config}, true
					case "model/list":
						return map[string]any{"data": []map[string]any{
							{"id": "server-model", "model": "server-model", "isDefault": true, "defaultReasoningEffort": "medium"},
							{"id": "configured-model", "model": "configured-model", "defaultReasoningEffort": "low"},
						}}, true
					default:
						t.Errorf("default read created or modified a thread: %s", m.Method)
						return nil, false
					}
				})
			})
			a := fixtureManagedAgent(t, socket, cwd, map[string]any{"model": "local-override", "reasoning_effort": "max"})
			settings, err := a.ReadDefaultSettings(context.Background())
			if err != nil || settings.Model != tc.wantModel || settings.ReasoningEffort != tc.wantEffort {
				t.Fatalf("wrong server defaults: %+v, %v", settings, err)
			}
			if a.GetModel() != "local-override" || a.GetReasoningEffort() != "max" {
				t.Fatal("reading defaults changed local overrides")
			}
		})
	}
}

func TestManagedCurrentModelAndEffort_UpdateAttachedThreadWithoutChangingDefaults(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	model, effort := "server-model", "medium"
	var updates []map[string]any
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			mu.Lock()
			defer mu.Unlock()
			switch m.Method {
			case "thread/resume":
				snapshot := fixtureSnapshot(cwd, "target", "")
				snapshot["model"], snapshot["reasoningEffort"] = model, effort
				return snapshot, true
			case "model/list":
				return map[string]any{"data": []map[string]any{
					{"id": "server-model", "model": "server-model", "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "medium"}}},
					{"id": "target-model", "model": "target-model", "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "high"}, {"reasoningEffort": "ultra"}}},
				}}, true
			case "thread/settings/update":
				var params map[string]any
				if err := json.Unmarshal(m.Params, &params); err != nil {
					t.Error(err)
				}
				updates = append(updates, params)
				if v, ok := params["model"].(string); ok {
					model = v
				}
				if v, ok := params["effort"].(string); ok {
					effort = v
				}
				return map[string]any{}, true
			}
			t.Errorf("unexpected RPC %s", m.Method)
			return nil, false
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"model": "future-model", "reasoning_effort": "low"})
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	ms, ok := as.(core.SessionModelSwitcher)
	if !ok {
		t.Fatal("/model cannot change the attached Codex session")
	}
	es, ok := as.(core.SessionReasoningEffortSwitcher)
	if !ok {
		t.Fatal("/effort cannot change the attached Codex session")
	}
	models, err := ms.CurrentModels(context.Background())
	if err != nil || len(models) != 2 || models[1].Name != "target-model" {
		t.Fatalf("server models: %+v, %v", models, err)
	}
	if err := ms.SetCurrentModel(context.Background(), "target-model"); err != nil {
		t.Fatal(err)
	}
	efforts, err := es.CurrentReasoningEfforts(context.Background())
	if err != nil || !reflect.DeepEqual(efforts, []string{"high", "ultra"}) {
		t.Fatalf("efforts did not follow current model: %v, %v", efforts, err)
	}
	if err := es.SetCurrentReasoningEffort(context.Background(), "ultra"); err != nil {
		t.Fatal(err)
	}
	s := as.(*managedSession)
	if s.GetModel() != "target-model" || s.GetReasoningEffort() != "ultra" || !s.Alive() {
		t.Fatalf("runtime settings not refreshed: %s, %s", s.GetModel(), s.GetReasoningEffort())
	}
	if a.GetModel() != "future-model" || a.GetReasoningEffort() != "low" || as.CurrentSessionID() != "target" {
		t.Fatal("current setting update changed defaults or thread identity")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []map[string]any{{"threadId": "target", "model": "target-model"}, {"threadId": "target", "effort": "ultra"}}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates overrode unrelated settings: %+v", updates)
	}
}

func TestManagedCurrentModelAndEffort_RejectBusyAndSurfaceServerErrors(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			if m.Method == "thread/settings/update" {
				if err := ws.WriteJSON(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "model forbidden by policy"}}); err != nil {
					t.Error(err)
				}
				return nil, false
			}
			t.Errorf("unexpected RPC %s", m.Method)
			return nil, false
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	ms, ok := as.(core.SessionModelSwitcher)
	if !ok {
		t.Fatal("attached model switching unsupported")
	}
	es := as.(core.SessionReasoningEffortSwitcher)
	s := as.(*managedSession)
	s.mu.Lock()
	s.turn = "busy-turn"
	s.mu.Unlock()
	if err := ms.SetCurrentModel(context.Background(), "new"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy model update: %v", err)
	}
	if err := es.SetCurrentReasoningEffort(context.Background(), "high"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy effort update: %v", err)
	}
	s.mu.Lock()
	s.turn = ""
	s.mu.Unlock()
	if err := ms.SetCurrentModel(context.Background(), "new"); err == nil || !strings.Contains(err.Error(), "model forbidden by policy") {
		t.Fatalf("server rejection hidden: %v", err)
	}
}

func TestManagedCurrentModel_RequiresServerConfirmation(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				snapshot := fixtureSnapshot(cwd, "target", "")
				snapshot["model"] = "unchanged-model"
				return snapshot, true
			}
			return map[string]any{}, true // update accepted, but not applied
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	if err := as.(core.SessionModelSwitcher).SetCurrentModel(context.Background(), "requested-model"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("unconfirmed model change reported as success: %v", err)
	}
	if as.(*managedSession).GetModel() != "unchanged-model" {
		t.Fatal("footer fabricated the requested model")
	}
}

// Opt-in check creates one independent empty thread; it never sends a turn.
func TestManagedLiveCurrentModelAndEffort(t *testing.T) {
	socket := os.Getenv("CC_CODEX_MANAGED_TEST_SOCKET")
	if socket == "" {
		t.Skip("set CC_CODEX_MANAGED_TEST_SOCKET and CC_CODEX_MANAGED_TEST_CWD")
	}
	a := fixtureManagedAgent(t, socket, os.Getenv("CC_CODEX_MANAGED_TEST_CWD"), nil)
	first, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	s := first.(*managedSession)
	models, err := s.CurrentModels(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("daemon model catalog unavailable: %+v, %v", models, err)
	}
	target := models[0].Name
	for _, model := range models {
		if model.Name != s.GetModel() {
			target = model.Name
			break
		}
	}
	// Empty threads have no rollout until their first turn, so metadata-only
	// resume cannot reattach/read them yet. Verify the real update protocol and
	// cross-client notifications without creating a billable turn. Persisted
	// thread read-back is covered by the adapter regression tests above.
	if err := s.updateCurrentThreadParams(context.Background(), map[string]any{"model": target}); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.currentModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	effort := ""
	for _, model := range catalog {
		if model.Model == target || model.ID == target {
			if len(model.Efforts) > 0 {
				effort = model.Efforts[0].Effort
			}
		}
	}
	if effort == "" {
		t.Fatal("daemon did not report reasoning options for selected model")
	}
	peer, err := a.openRPC(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.close()
	if err := peer.request(context.Background(), "thread/settings/update", map[string]any{"threadId": first.CurrentSessionID(), "effort": effort}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetModel() == target && s.GetReasoningEffort() == effort {
			t.Logf("thread=%s, observer sees model=%s and peer-selected effort=%s", first.CurrentSessionID(), target, effort)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("observer did not receive settings updates: model=%s, effort=%s", s.GetModel(), s.GetReasoningEffort())
}

func TestManagedDefaults_ModelAndEffortOptionsComeFromDaemon(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			switch m.Method {
			case "config/read":
				var params map[string]any
				if err := json.Unmarshal(m.Params, &params); err != nil {
					t.Error(err)
				}
				if params["cwd"] != cwd || params["includeLayers"] != false {
					t.Errorf("config scope: %v", params)
				}
				return map[string]any{"config": map[string]any{"model": "server-default"}}, true
			case "model/list":
				var params map[string]any
				if err := json.Unmarshal(m.Params, &params); err != nil {
					t.Error(err)
				}
				if params["cursor"] == "next" {
					return map[string]any{"data": []map[string]any{
						{"id": "alternate", "model": "alternate", "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "extra high"}, {"reasoningEffort": "light"}}},
					}}, true
				}
				return map[string]any{"nextCursor": "next", "data": []map[string]any{
					{"id": "server-default", "model": "server-default", "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "server-only"}}},
					{"id": "hidden", "model": "hidden", "hidden": true},
				}}, true
			}
			t.Errorf("unexpected default-settings RPC: %s", m.Method)
			return nil, false
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	models := a.AvailableModels(context.Background())
	if len(models) != 2 || models[0].Name != "server-default" || models[1].Name != "alternate" {
		t.Fatalf("models not from daemon: %+v", models)
	}
	if got := a.AvailableReasoningEfforts(); !reflect.DeepEqual(got, []string{"server-only"}) {
		t.Fatalf("default efforts: %v", got)
	}
	a.SetReasoningEffort("server-only")
	a.SetModel("alternate")
	if a.GetReasoningEffort() != "" {
		t.Fatal("old model's effort retained")
	}
	if got := a.AvailableReasoningEfforts(); !reflect.DeepEqual(got, []string{"extra high", "light"}) {
		t.Fatalf("selected model efforts: %v", got)
	}
	a.SetReasoningEffort("extra high")
	params := a.newThreadParams()
	if params["model"] != "alternate" || params["config"].(map[string]any)["model_reasoning_effort"] != "extra high" {
		t.Fatalf("creation lost server effort: %v", params)
	}
	opts := a.WorkspaceAgentOptions()
	opts["work_dir"] = cwd
	cloned, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if cloned.(core.ReasoningEffortSwitcher).GetReasoningEffort() != "extra high" {
		t.Fatal("workspace clone normalized away server effort")
	}
}

func TestManagedDefaults_DaemonUnavailableDoesNotOfferLocalOptions(t *testing.T) {
	a := fixtureManagedAgent(t, filepath.Join(t.TempDir(), "missing.sock"), t.TempDir(), nil)
	if got := a.AvailableModels(context.Background()); len(got) != 0 {
		t.Fatalf("local models leaked: %v", got)
	}
	if got := a.AvailableReasoningEfforts(); len(got) != 0 {
		t.Fatalf("hardcoded efforts leaked: %v", got)
	}
}
