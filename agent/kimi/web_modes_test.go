package kimi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebPermissionModes_MatchWebAndPreserveSession(t *testing.T) {
	f := newWebFixture(t)
	f.runtimeStatus = map[string]any{"model": "fixture-model", "thinking_level": "high", "permission": "manual"}
	as, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	sw := as.(core.SessionPermissionSwitcher)
	modes := sw.CurrentPermissionModes()
	if len(modes) != 3 || modes[0].Key != "manual" || modes[1].Key != "yolo" || modes[2].Key != "auto" {
		t.Fatalf("Web choices: %+v", modes)
	}
	for _, mode := range []string{"yolo", "auto", "manual"} {
		if err := sw.SetCurrentPermissionMode(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		settings, err := as.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
		if err != nil || settings.PermissionMode != mode || settings.Model != "fixture-model" || settings.ReasoningEffort != "high" {
			t.Fatalf("settings changed incorrectly: %+v, %v", settings, err)
		}
	}
	if err := sw.SetCurrentPermissionMode(context.Background(), "full-access"); err == nil {
		t.Fatal("foreign preset accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.profiles) != 3 || len(f.peers) != 1 || !as.Alive() {
		t.Fatalf("updates=%d observers=%d", len(f.profiles), len(f.peers))
	}
	for _, body := range f.profiles {
		config := body["agent_config"].(map[string]any)
		if len(body) != 1 || len(config) != 1 || config["permission_mode"] == nil {
			t.Fatalf("unrelated profile fields overwritten: %v", body)
		}
	}
}

func TestWebPermissionModes_RejectUnconfirmedOrFailedUpdates(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			f := newWebFixture(t)
			f.runtimeStatus = map[string]any{"permission": "manual"}
			original := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/profile") {
					if reject {
						fmt.Fprint(w, `{"code":40001,"msg":"rejected","data":null}`)
					} else {
						f.reply(w, f.snapshot.Session)
					}
					return
				}
				original.ServeHTTP(w, r)
			})
			as, err := f.agent.AttachSession(context.Background(), "session_test")
			if err != nil {
				t.Fatal(err)
			}
			defer as.Close()
			err = as.(core.SessionPermissionSwitcher).SetCurrentPermissionMode(context.Background(), "auto")
			if err == nil {
				t.Fatal("reported success without applied permission")
			}
			if !reject && !strings.Contains(err.Error(), "not confirmed") {
				t.Fatal(err)
			}
		})
	}
}

func TestWebEngine_PermissionCommandsAndCardsUseCurrentSession(t *testing.T) {
	f := newWebFixture(t)
	f.runtimeStatus = map[string]any{"model": "fixture-model", "permission": "manual"}
	p := &webWorkspacePlatform{}
	e := core.NewEngine("permissions", f.agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	e.SetAdminFrom("user")
	defer e.Stop()
	send := func(cmd string) string {
		before := p.getSent()
		e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", UserID: "user", Platform: "test", Content: cmd, ReplyCtx: "user"})
		return strings.TrimPrefix(p.getSent(), before)
	}
	if text := send("/mode"); !strings.Contains(text, "attach") {
		t.Fatalf("unattached mode: %s", text)
	}
	send("/attach session_test")
	text := send("/mode")
	for _, label := range []string{"Always Ask", "Ask When Needed", "Never Ask"} {
		if !strings.Contains(text, label) {
			t.Fatalf("missing %s: %s", label, text)
		}
	}
	for _, command := range []string{"/mode yolo", "/mode current auto", "/mode manual"} {
		if text := send(command); !strings.Contains(text, "updated") {
			t.Fatalf("mode command: %s", text)
		}
	}
	// Platform button callbacks use the same current-session command path.
	if text := send("/mode current yolo"); !strings.Contains(text, "yolo") {
		t.Fatal(text)
	}
	if text := send("/model"); !strings.Contains(text, "fixture-model") {
		t.Fatal(text)
	}
}

// Only creates and changes a fresh empty test session; no model requests.
func TestWebLivePermissionModes(t *testing.T) {
	addr := os.Getenv("CC_KIMI_WEB_TEST_URL")
	if addr == "" {
		t.Skip("set CC_KIMI_WEB_TEST_URL, token file and CWD")
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
	defer first.Close()
	second, err := a.(core.AgentSessionAttacher).AttachSession(context.Background(), first.CurrentSessionID())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, mode := range []string{"manual", "yolo", "auto", "manual"} {
		if err := first.(core.SessionPermissionSwitcher).SetCurrentPermissionMode(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		settings, err := second.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
		if err != nil || settings.PermissionMode != mode {
			t.Fatalf("peer permission=%s want=%s: %v", settings.PermissionMode, mode, err)
		}
	}
	t.Logf("two observers verified all permissions on fresh session %s", first.CurrentSessionID())
}
