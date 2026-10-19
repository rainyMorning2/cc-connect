package kimi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebDirSwitch_ListsAndStartsInSelectedWorkspace(t *testing.T) {
	f := newWebFixture(t)
	switcher, ok := any(f.agent).(core.WorkDirSwitcher)
	if !ok {
		t.Fatal("Kimi Web does not support /dir")
	}
	oldDir := switcher.GetWorkDir()
	session, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	newDir := t.TempDir()
	switcher.SetWorkDir(newDir)
	list, err := f.agent.ListSessions(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("old workspace leaked into list: %+v, %v", list, err)
	}
	// An existing observer keeps its original workspace across reconnects.
	if _, err := session.(*webSession).snapshot(); err != nil {
		t.Fatalf("directory switch invalidated existing observer: %v", err)
	}
	if _, err := f.agent.AttachSession(context.Background(), "session_test"); err == nil || !strings.Contains(err.Error(), oldDir) || !strings.Contains(err.Error(), newDir) {
		t.Fatalf("wrong-workspace error must identify both directories: %v", err)
	}
	f.mu.Lock()
	f.snapshot.Session.Metadata.Cwd = newDir
	f.mu.Unlock()
	list, err = f.agent.ListSessions(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("selected workspace not listed: %+v, %v", list, err)
	}
	created, err := f.agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("start in selected workspace: %v", err)
	}
	defer created.Close()
}

func TestWebListSessions_LegacyZeroCountUsesHistory(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.appendMessage("legacy-1", "user", "Legacy conversation title")
	f.appendMessage("legacy-1", "assistant", "Legacy reply")
	f.mu.Unlock()
	list, err := f.agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].MessageCount != 2 || list[0].Summary != "Legacy conversation title" {
		t.Fatalf("legacy conversation shown as empty: %+v", list)
	}
}

func TestWebSession_FooterReportsServerModelAndUsage(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.snapshot.Session.AgentConfig.Model = "server-model"
	f.mu.Unlock()
	session, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	model, ok := session.(interface{ GetModel() string })
	if !ok || model.GetModel() != "server-model" {
		t.Fatal("footer model missing from attached server session")
	}
	reporter, ok := session.(core.ContextUsageReporter)
	if !ok {
		t.Fatal("footer usage missing from attached server session")
	}
	f.mu.Lock()
	f.broadcastLocked("agent.status.updated", map[string]any{
		"agentId": "subagent", "model": "wrong-model", "contextTokens": 999,
	}, false)
	f.broadcastLocked("agent.status.updated", map[string]any{
		"agentId": "main", "model": "changed-model", "thinkingEffort": "high",
		"contextTokens": 2500, "maxContextTokens": 10000,
		"usage": map[string]any{"currentTurn": map[string]int{"inputOther": 100, "output": 200, "inputCacheRead": 300, "inputCacheCreation": 400}},
	}, false)
	f.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		u := reporter.GetContextUsage()
		if u != nil && u.OutputTokens == 200 {
			if model.GetModel() != "changed-model" || u.UsedTokens != 2500 || u.ContextWindow != 10000 || u.InputTokens != 100 || u.CachedInputTokens != 300 || u.CacheCreationInputTokens != 400 {
				t.Fatalf("incorrect footer status: %s, %+v", model.GetModel(), u)
			}
			u.OutputTokens = 999 // callers must receive a copy
			if reporter.GetContextUsage().OutputTokens != 200 {
				t.Fatal("usage getter leaked mutable state")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server status event did not update footer usage")
}

type webWorkspacePlatform struct {
	mu   sync.Mutex
	sent []string
}

func (*webWorkspacePlatform) Name() string                    { return "test" }
func (*webWorkspacePlatform) Start(core.MessageHandler) error { return nil }
func (*webWorkspacePlatform) Stop() error                     { return nil }
func (p *webWorkspacePlatform) Reply(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	p.sent = append(p.sent, text)
	p.mu.Unlock()
	return nil
}
func (p *webWorkspacePlatform) Send(ctx context.Context, replyCtx any, text string) error {
	return p.Reply(ctx, replyCtx, text)
}
func (p *webWorkspacePlatform) getSent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.sent, "\n")
}

// Exercise the actual adapter and engine together; core tests cannot import
// this package because adapters depend on core. Only HTTP/WS and platform I/O
// are mocked. The persisted session belongs to a different workspace at startup.
func TestWebEngine_DirListAttachChatWithPersistedWorkspaceMismatch(t *testing.T) {
	f := newWebFixture(t)
	targetDir := f.agent.GetWorkDir()
	f.agent.SetWorkDir(t.TempDir())
	f.mu.Lock()
	f.appendMessage("legacy", "user", "Previous workspace conversation")
	f.appendMessage("legacy", "assistant", "Previous workspace reply")
	f.status = map[string]any{
		"agentId": "main", "model": "server-model", "thinkingEffort": "high",
		"contextTokens": 2500, "maxContextTokens": 10000,
		"usage": map[string]any{"currentTurn": map[string]int{"inputOther": 100, "output": 200, "inputCacheRead": 300, "inputCacheCreation": 400}},
	}
	f.snapshot.Session.AgentConfig.Model = "server-model"
	f.mu.Unlock()
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	manager := core.NewSessionManager(storePath)
	manager.GetOrCreateActive("test:user").SetAgentSessionID("session_test", "kimi")
	manager.Save()
	p := &webWorkspacePlatform{}
	e := core.NewEngine("workspace-test", f.agent, []core.Platform{p}, storePath, core.LangEnglish)
	e.SetAdminFrom("user")
	e.SetReplyFooterEnabled(true)
	defer e.Stop()
	send := func(text string) {
		t.Helper()
		e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", Platform: "test", UserID: "user", MessageID: text, Content: text, ReplyCtx: "user"})
	}
	await := func(text string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(p.getSent(), text) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q in platform output: %s", text, p.getSent())
	}
	send("/dir " + targetDir)
	await(targetDir)
	send("/list")
	await("Previous workspace conversation")
	send("/attach session_test")
	await("session_test")
	send("hi")
	await("IM answer")
	await("server-model")
	await("high")
	await("out 200")
	await("in 100 cw 400 cr 300")
	await("ctx 25%")
	send("/history")
	await("Previous workspace reply")
}

// Read-only check against a user-selected existing session. No prompt is sent.
func TestWebLiveWorkspaceSelection(t *testing.T) {
	id := os.Getenv("CC_KIMI_WEB_TEST_SESSION_ID")
	if id == "" {
		t.Skip("set CC_KIMI_WEB_TEST_SESSION_ID and the live web test URL/token/CWD")
	}
	agent, err := New(map[string]any{
		"backend": "web", "web_url": os.Getenv("CC_KIMI_WEB_TEST_URL"),
		"web_token_file": os.Getenv("CC_KIMI_WEB_TEST_TOKEN_FILE"),
		"work_dir":       t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Stop()
	if _, err := agent.StartSession(context.Background(), id); err == nil {
		t.Fatal("wrong workspace was accepted")
	}
	agent.(core.WorkDirSwitcher).SetWorkDir(os.Getenv("CC_KIMI_WEB_TEST_CWD"))
	list, err := agent.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range list {
		if entry.ID == id {
			found = true
			t.Logf("selected session: messages=%d, has_title=%v", entry.MessageCount, entry.Summary != "")
		}
	}
	if !found {
		t.Fatal("selected workspace session not in list")
	}
	session, err := agent.StartSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	model := session.(interface{ GetModel() string }).GetModel()
	usage := session.(core.ContextUsageReporter).GetContextUsage()
	if model == "" || usage == nil || usage.ContextWindow <= 0 {
		t.Fatalf("configured session has no model/context: model=%q usage=%+v", model, usage)
	}
	t.Logf("attached: model=%q, effort=%q, context=%+v", model, session.(*webSession).GetReasoningEffort(), usage)
}
