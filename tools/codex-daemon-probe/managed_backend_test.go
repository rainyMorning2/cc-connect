package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/codex"
	"github.com/chenhg5/cc-connect/core"
)

// Opt-in integration: production adapter + Engine, existing daemon and local
// Responses fixture. Every control targets a newly created isolated thread.
func TestManagedBackendWithDaemonAndSimulatedModel(t *testing.T) {
	if os.Getenv("CC_CONNECT_DAEMON_SIMULATED") != "1" {
		t.Skip("set CC_CONNECT_DAEMON_SIMULATED=1 to test the existing daemon with a local simulated model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := os.Getenv("CODEX_CLI_PATH")
	if binary == "" {
		binary = "codex"
	}
	info, err := discover(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var sawAnswers, sawSteer atomic.Bool
	var sawApplicationContext atomic.Bool
	holding := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
		if err != nil || r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		n := calls.Add(1)
		if n == 2 {
			sawAnswers.Store(strings.Contains(string(body), "MANAGED-OTHER-ANSWER"))
			sawSteer.Store(strings.Contains(string(body), "MANAGED-STEER-MARKER"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("managed-%d", n)}})
		if n >= 4 {
			sawApplicationContext.Store(strings.Contains(string(body), "CC Connect CLI tools"))
			select {
			case holding <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		item := map[string]any{"type": "message", "role": "assistant", "id": "managed-final", "content": []any{map[string]any{"type": "output_text", "text": "MANAGED-FIXTURE-DONE"}}}
		if n == 1 {
			questions := []any{}
			for _, id := range []string{"choice", "detail"} {
				questions = append(questions, map[string]any{"id": id, "header": id, "question": "MANAGED-QUESTION " + id,
					"options": []any{map[string]any{"label": "A", "description": "First"}, map[string]any{"label": "B", "description": "Second"}}})
			}
			args, _ := json.Marshal(map[string]any{"questions": questions})
			item = map[string]any{"type": "function_call", "name": "request_user_input", "call_id": "managed-question", "arguments": string(args)}
		} else if n == 3 {
			args, _ := json.Marshal(map[string]any{"cmd": "python3 -c 'print(42)'", "sandbox_permissions": "require_escalated", "justification": "MANAGED-CANCEL-ONLY; cancel this request, do not execute"})
			item = map[string]any{"type": "function_call", "name": "exec_command", "call_id": "managed-approval", "arguments": string(args)}
		}
		emit(map[string]any{"type": "response.output_item.done", "item": item})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("managed-%d", n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer func() { cancel(); server.Close() }()
	primary, err := connect(ctx, info.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.close()
	stopPrimary := context.AfterFunc(ctx, primary.close)
	defer stopPrimary()
	cwd, err := os.MkdirTemp("", "cc-connect-managed-integration-")
	if err != nil {
		t.Fatal(err)
	}
	// Retain the empty workspace for the daemon's retained diagnostic thread.
	var started threadSnapshot
	err = primary.request(ctx, "thread/start", map[string]any{
		"cwd": cwd, "model": "gpt-5.4", "modelProvider": "cc_connect_managed_fixture", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only",
		"config": map[string]any{"model_providers.cc_connect_managed_fixture": map[string]any{"name": "CC Connect simulated integration", "base_url": server.URL, "wire_api": "responses", "requires_openai_auth": false, "supports_websockets": false, "request_max_retries": 0, "stream_max_retries": 0}},
	}, &started)
	if err != nil {
		t.Fatal(err)
	}
	thread := started.Thread.ID
	if thread == "" || thread == os.Getenv("CODEX_THREAD_ID") {
		t.Fatal("did not create an isolated thread")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		var snap threadSnapshot
		if primary.request(cleanup, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &snap) == nil {
			if turn := activeTurn(snap); turn != "" {
				_ = primary.request(cleanup, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, nil)
			}
		}
	}()
	agent, err := codex.New(map[string]any{"backend": "app_server", "app_server_transport": "managed_daemon", "daemon_socket": info.SocketPath, "daemon_attach_only": true, "daemon_busy_message_mode": "steer", "work_dir": cwd})
	if err != nil {
		t.Fatal(err)
	}
	agent.(core.ProviderSwitcher).SetProviders([]core.ProviderConfig{{Name: "cc_connect_managed_fixture"}, {Name: "future-only"}})
	p := &managedIntegrationPlatform{}
	engine := core.NewEngine("managed-integration", agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	defer engine.Stop()
	api, err := core.NewAPIServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api.RegisterEngine("managed-integration", engine)
	api.Start()
	defer api.Stop()
	send := func(text string) {
		engine.ReceiveMessage(p, &core.Message{SessionKey: "integration:user", Platform: p.Name(), UserID: "user", Content: text, ReplyCtx: "reply"})
	}
	await := func(text string) {
		t.Helper()
		for {
			if strings.Contains(p.visible(), text) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("missing %q; got %s", text, p.visible())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	startTurn := func() {
		t.Helper()
		var result json.RawMessage
		if err := primary.request(ctx, "turn/start", map[string]any{"threadId": thread, "input": input("isolated production adapter integration"), "collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{"model": "gpt-5.4", "reasoning_effort": "medium", "developer_instructions": nil}}}, &result); err != nil {
			t.Fatal(err)
		}
	}
	startTurn()
	if _, err := waitFor(ctx, primary, "item/tool/requestUserInput", thread); err != nil {
		t.Fatal(err)
	}
	// A brand-new thread has no persisted rollout until its first turn.
	// Attach after the primary has started work, matching the target journey.
	send("/attach " + thread)
	await("MANAGED-QUESTION choice")
	send("/detach")
	await("Detached")
	p.clear()
	send("/attach " + thread)
	await("MANAGED-QUESTION choice") // Pending request replay after client detach.
	// Creation-default commands must preserve the observer, pending question,
	// active turn, and this thread's simulated provider across both clients.
	send("/model switch future-model")
	send("/reasoning high")
	send("/mode full-auto")
	send("/provider switch future-only")
	await("Model: gpt-5.4 · Reasoning: medium · Provider: cc_connect_managed_fixture")
	await("Model: future-model · Reasoning: high · Mode: full-auto · Provider: future-only")
	send("/steer MANAGED-STEER-MARKER")
	await("Adjustment accepted")
	send(p.button(t, "A"))
	await("MANAGED-QUESTION detail")
	send("MANAGED-OTHER-ANSWER")
	await("MANAGED-FIXTURE-DONE")
	if _, err := waitFor(ctx, primary, "turn/completed", thread); err != nil {
		t.Fatal(err)
	}
	if !sawAnswers.Load() || !sawSteer.Load() {
		t.Fatalf("input not delivered: answers=%v steer=%v; visible=%s", sawAnswers.Load(), sawSteer.Load(), p.visible())
	}
	p.clear()
	startTurn()
	if _, err := waitFor(ctx, primary, "item/commandExecution/requestApproval", thread); err != nil {
		t.Fatal(err)
	}
	await("python3 -c")
	send(p.button(t, "Cancel turn"))
	await("interrupted")
	if _, err := waitFor(ctx, primary, "turn/completed", thread); err != nil {
		t.Fatal(err)
	}
	p.clear()
	send("start isolated simulated turn")
	select {
	case <-holding:
	case <-ctx.Done():
		t.Fatal("simulated turn did not start")
	}
	p.clear()
	send("AUTO-STEER-ORDINARY-REPLY")
	await(core.NewI18n(core.LangEnglish).T(core.MsgSharedSteerAccepted))
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", api.SocketPath())
	}}}
	payload, _ := json.Marshal(core.SendRequest{AgentSessionID: thread, Message: "AUTO-BOUND-TOOL-SEND"})
	response, err := client.Post("http://unix/send", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bound send failed: %d %s", response.StatusCode, responseBody)
	}
	await("AUTO-BOUND-TOOL-SEND")
	send("/stop")
	await("interrupted")
	if _, err := waitFor(ctx, primary, "turn/completed", thread); err != nil {
		t.Fatal(err)
	}
	send("/detach")
	var snap threadSnapshot
	if err := primary.request(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &snap); err != nil {
		t.Fatal(err)
	}
	if activeTurn(snap) != "" || calls.Load() != 4 {
		t.Fatalf("unexpected final state: active=%s simulated requests=%d", activeTurn(snap), calls.Load())
	}
	if sawApplicationContext.Load() {
		t.Fatal("unexpected CC Connect application context reached the simulated model")
	}
	var history struct {
		Thread struct {
			Turns []struct {
				Items []map[string]any `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := primary.request(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &history); err != nil {
		t.Fatal(err)
	}
	var lastUser string
	for _, turn := range history.Thread.Turns {
		for _, item := range turn.Items {
			if item["type"] == "userMessage" {
				data, _ := json.Marshal(item)
				lastUser = string(data)
			}
		}
	}
	if !strings.Contains(lastUser, "start isolated simulated turn") || strings.Contains(lastUser, "session_key") || strings.Contains(lastUser, "CC Connect CLI tools") {
		t.Fatalf("application context leaked into daemon userMessage: %s", lastUser)
	}
	t.Logf("production integration passed: thread=%s cwd=%s daemon=%s simulated_requests=%d", thread, cwd, info.AppServerVersion, calls.Load())
}

type managedIntegrationPlatform struct {
	mu    sync.Mutex
	texts []string
	cards []*core.Card
}

func (*managedIntegrationPlatform) Name() string                    { return "integration" }
func (*managedIntegrationPlatform) Start(core.MessageHandler) error { return nil }
func (*managedIntegrationPlatform) Stop() error                     { return nil }
func (p *managedIntegrationPlatform) Reply(ctx context.Context, reply any, text string) error {
	return p.Send(ctx, reply, text)
}
func (p *managedIntegrationPlatform) Send(_ context.Context, _ any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, text)
	return nil
}
func (p *managedIntegrationPlatform) SendCard(_ context.Context, _ any, card *core.Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cards = append(p.cards, card)
	return nil
}
func (p *managedIntegrationPlatform) ReplyCard(ctx context.Context, reply any, card *core.Card) error {
	return p.SendCard(ctx, reply, card)
}
func (p *managedIntegrationPlatform) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = nil
	p.cards = nil
}
func (p *managedIntegrationPlatform) visible() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	texts := append([]string(nil), p.texts...)
	for _, card := range p.cards {
		for _, element := range card.Elements {
			if md, ok := element.(core.CardMarkdown); ok {
				texts = append(texts, md.Content)
			}
		}
	}
	return strings.Join(texts, "\n")
}
func (p *managedIntegrationPlatform) button(t *testing.T, text string) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.cards) - 1; i >= 0; i-- {
		for _, element := range p.cards[i].Elements {
			if row, ok := element.(core.CardActions); ok {
				for _, b := range row.Buttons {
					if b.Text == text {
						return strings.TrimPrefix(b.Value, "cmd:")
					}
				}
			}
		}
	}
	t.Fatalf("missing button %q", text)
	return ""
}
