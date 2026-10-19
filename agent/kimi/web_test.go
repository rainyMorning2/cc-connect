package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

type webFixture struct {
	t               *testing.T
	server          *httptest.Server
	agent           *webAgent
	mu              sync.Mutex
	peers           map[*websocket.Conn]bool
	seq             int64
	messages        []webMessage
	requests        []string
	responses       []map[string]any
	snapshot        webSnapshot
	rejectSubscribe bool
	queueOnly       bool
	status          map[string]any
	runtimeStatus   map[string]any
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	f := &webFixture{t: t, peers: map[*websocket.Conn]bool{}}
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	f.snapshot = webSnapshot{Epoch: "epoch-1", Session: webRecord{ID: "session_test"}}
	f.snapshot.Session.Metadata.Cwd = dir
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	a, err := New(map[string]any{"backend": "web", "web_url": f.server.URL, "web_token_file": token, "work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = a.(*webAgent)
	t.Cleanup(func() {
		f.agent.Stop()
		f.mu.Lock()
		for c := range f.peers {
			c.Close()
		}
		f.mu.Unlock()
		f.server.Close()
	})
	return f
}
func (f *webFixture) reply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "success", "data": data}); err != nil {
		f.t.Errorf("encode: %v", err)
	}
}
func (f *webFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer fixture-token" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.URL.Path == "/api/v1/ws" {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var sub struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Payload struct {
				Cursors map[string]struct {
					Seq   int64  `json:"seq"`
					Epoch string `json:"epoch"`
				} `json:"cursors"`
			} `json:"payload"`
		}
		if err = c.ReadJSON(&sub); err != nil {
			return
		}
		if sub.Type != "subscribe" || sub.Payload.Cursors["session_test"].Epoch != "epoch-1" {
			f.t.Errorf("bad subscription: %+v", sub)
			return
		}
		f.mu.Lock()
		code := 0
		if f.rejectSubscribe {
			code = 40401
		}
		err = c.WriteJSON(map[string]any{"type": "ack", "id": sub.ID, "code": code, "payload": map[string]any{"accepted": []string{"session_test"}, "not_found": []string{}, "resync_required": []string{}}})
		f.peers[c] = true
		f.mu.Unlock()
		if err != nil {
			return
		}
		defer func() { f.mu.Lock(); delete(f.peers, c); f.mu.Unlock() }()
		for {
			if _, _, err = c.ReadMessage(); err != nil {
				return
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/sessions/session_test":
		f.reply(w, f.snapshot.Session)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/status"):
		if f.runtimeStatus != nil {
			f.reply(w, f.runtimeStatus)
		} else {
			f.reply(w, map[string]any{"model": f.snapshot.Session.AgentConfig.Model})
		}
	case r.Method == "GET" && r.URL.Path == "/api/v1/config":
		f.reply(w, map[string]string{"default_model": "fixture-model"})
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/snapshot"):
		f.snapshot.Seq = f.seq
		f.reply(w, f.snapshot)
	case r.Method == "GET" && r.URL.Path == "/api/v1/sessions":
		f.reply(w, map[string]any{"items": []webRecord{f.snapshot.Session}, "has_more": false})
	case r.Method == "POST" && r.URL.Path == "/api/v1/sessions":
		f.reply(w, f.snapshot.Session)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/prompts"):
		f.reply(w, map[string]any{"active": nil, "queued": []any{}})
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/messages"):
		f.reply(w, map[string]any{"items": f.messages, "has_more": false})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompts"):
		var body struct {
			ID      string `json:"prompt_id"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Error(err)
			return
		}
		if f.queueOnly {
			f.reply(w, map[string]any{"prompt_id": body.ID, "status": "queued"})
			return
		}
		// Deliberately finish over WS BEFORE the REST response reaches the caller.
		f.broadcastLocked("turn.started", map[string]any{"agentId": "main", "turnId": 2, "promptId": body.ID, "prompt": body.Content[0].Text}, false)
		f.broadcastLocked("assistant.delta", map[string]any{"agentId": "main", "turnId": 2, "delta": "IM answer"}, true)
		if f.status != nil {
			f.broadcastLocked("agent.status.updated", f.status, false)
		}
		f.appendMessage(body.ID, "assistant", "IM answer")
		f.broadcastLocked("turn.ended", map[string]any{"agentId": "main", "turnId": 2, "reason": "completed"}, false)
		f.reply(w, map[string]any{"prompt_id": body.ID, "status": "running"})
	case strings.HasSuffix(r.URL.Path, ":abort"):
		f.reply(w, map[string]any{"aborted": true})
	case strings.Contains(r.URL.Path, "/approvals/") || strings.Contains(r.URL.Path, "/questions/"):
		var b map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&b)
		}
		f.responses = append(f.responses, b)
		f.reply(w, map[string]any{"resolved": true})
	default:
		http.Error(w, "unexpected request", 404)
	}
}
func (f *webFixture) appendMessage(prompt, role, text string) {
	var m webMessage
	json.Unmarshal([]byte(fmt.Sprintf(`{"id":%q,"prompt_id":%q,"role":%q,"content":[{"type":"text","text":%q}],"created_at":"2026-10-17T09:00:00Z"}`, prompt+role, prompt, role, text)), &m)
	f.messages = append(f.messages, m)
}
func (f *webFixture) broadcastLocked(kind string, p any, volatile bool) {
	f.seq++
	for c := range f.peers {
		c.SetWriteDeadline(time.Now().Add(time.Second))
		if err := c.WriteJSON(map[string]any{"type": kind, "seq": f.seq, "epoch": "epoch-1", "session_id": "session_test", "volatile": volatile, "payload": p}); err != nil {
			c.Close()
		}
	}
}
func (f *webFixture) broadcast(kind string, p any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcastLocked(kind, p, kind == "assistant.delta")
}
func (f *webFixture) attach() *webSession {
	f.t.Helper()
	as, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		f.t.Fatal(err)
	}
	return as.(*webSession)
}
func awaitWebEvent(t *testing.T, s *webSession, kind core.EventType) core.Event {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("event stream closed")
			}
			if e.Notice != nil {
				t.Logf("notice: %+v", e.Notice)
			}
			if e.Type == kind {
				return e
			}
			if e.Type == core.EventError {
				t.Fatalf("observer error: %v", e.Error)
			}
		case <-timer.C:
			t.Fatalf("missing %s", kind)
		}
	}
}
func TestWebSharedTwoClientsAndDetach(t *testing.T) {
	f := newWebFixture(t)
	a, b := f.attach(), f.attach()
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 1, "promptId": "browser", "prompt": "browser question"})
	for _, s := range []*webSession{a, b} {
		e := awaitWebEvent(t, s, core.EventUserMessage)
		if e.Content != "browser question" {
			t.Fatal(e)
		}
	}
	f.broadcast("assistant.delta", map[string]any{"agentId": "main", "turnId": 1, "delta": "partial"})
	f.mu.Lock()
	f.appendMessage("browser", "assistant", "complete browser answer")
	f.mu.Unlock()
	f.broadcast("turn.ended", map[string]any{"agentId": "main", "turnId": 1, "reason": "completed"})
	for _, s := range []*webSession{a, b} {
		e := awaitWebEvent(t, s, core.EventResult)
		if e.Content != "complete browser answer" || e.TurnID != "browser" {
			t.Fatal(e)
		}
	}
	id, err := a.SendTurn("IM question", "message", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*webSession{a, b} {
		e := awaitWebEvent(t, s, core.EventResult)
		if e.Content != "IM answer" || e.TurnID != id {
			t.Fatal(e)
		}
	}
	a.Close()
	<-a.done
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 3, "promptId": "still-running", "prompt": "after detach"})
	if e := awaitWebEvent(t, b, core.EventUserMessage); e.Content != "after detach" {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.Contains(r, "abort") || strings.Contains(r, "shutdown") {
			t.Fatalf("detach mutated server: %s", r)
		}
	}
}
func TestWebAttachPreservesSettingsAndRejectsWrongWorkspace(t *testing.T) {
	f := newWebFixture(t)
	s := f.attach()
	s.Close()
	<-s.done
	f.mu.Lock()
	for _, r := range f.requests {
		if strings.HasPrefix(r, "POST") {
			t.Errorf("attach wrote settings: %s", r)
		}
	}
	f.snapshot.Session.Metadata.Cwd = "/another-workspace"
	f.mu.Unlock()
	if _, err := f.agent.AttachSession(context.Background(), "session_test"); err == nil {
		t.Fatal("attached wrong workspace")
	}
}
func TestWebAttachWaitsForSubscriptionAcceptance(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.rejectSubscribe = true
	f.mu.Unlock()
	if _, err := f.agent.AttachSession(context.Background(), "session_test"); err == nil {
		t.Fatal("attach accepted rejected subscription")
	}
}
func TestWebApprovalResolvedByOtherClientAndQuestionMapping(t *testing.T) {
	f := newWebFixture(t)
	s := f.attach()
	f.broadcast("event.approval.requested", map[string]any{"approval_id": "a", "tool_name": "Bash", "tool_input_display": "echo hello"})
	e := awaitWebEvent(t, s, core.EventPermissionRequest)
	if err := s.RespondPermission(e.RequestID, core.PermissionResult{Behavior: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.responses[0]["scope"] != "session" {
		t.Error(f.responses)
	}
	f.mu.Unlock()
	f.broadcast("event.approval.resolved", map[string]any{"approval_id": "a"})
	awaitWebEvent(t, s, core.EventPermissionResolved)
	if err := s.RespondPermission(e.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil {
		t.Fatal("stale approval accepted")
	}
	f.broadcast("event.question.requested", map[string]any{"question_id": "q", "questions": []any{map[string]any{"id": "q_0", "question": "Choose", "options": []any{map[string]string{"id": "opt_a", "label": "Alpha"}, map[string]string{"id": "opt_b", "label": "Beta"}}}}})
	e = awaitWebEvent(t, s, core.EventPermissionRequest)
	if err := s.RespondPermission(e.RequestID, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"q_0": []string{"Beta"}}}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(f.responses[1])
	if !strings.Contains(string(raw), `"option_id":"opt_b"`) {
		t.Fatal(string(raw))
	}
}
func TestWebReconnectRecoversCompletedTurnWithoutDuplicating(t *testing.T) {
	f := newWebFixture(t)
	s := f.attach()
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 1, "promptId": "browser", "prompt": "start"})
	awaitWebEvent(t, s, core.EventTurnStarted)
	f.broadcast("assistant.delta", map[string]any{"agentId": "main", "turnId": 1, "delta": "partial"})
	awaitWebEvent(t, s, core.EventText)
	f.mu.Lock()
	f.appendMessage("browser", "assistant", "answer completed while disconnected")
	for c := range f.peers {
		c.Close()
	}
	f.mu.Unlock()
	e := awaitWebEvent(t, s, core.EventResult)
	if e.Content != "answer completed while disconnected" {
		t.Fatal(e)
	}
	awaitWebEvent(t, s, core.EventRuntimeStatus)
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 4, "promptId": "next", "prompt": "next prompt"})
	e = awaitWebEvent(t, s, core.EventUserMessage)
	if e.Content != "next prompt" {
		t.Fatal(e)
	}
}
func TestWebCancelTargetsExpectedPrompt(t *testing.T) {
	f := newWebFixture(t)
	s := f.attach()
	f.broadcast("turn.started", map[string]any{"agentId": "main", "turnId": 1, "promptId": "browser"})
	awaitWebEvent(t, s, core.EventTurnStarted)
	if err := s.CancelExpectedTurn("old"); err == nil {
		t.Fatal("stale cancel accepted")
	}
	if err := s.CancelExpectedTurn("browser"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.requests[len(f.requests)-1]; got != "POST /api/v1/sessions/session_test/prompts/browser:abort" {
		t.Fatal(got)
	}
}
func TestWebConfigurationAndTokenRedaction(t *testing.T) {
	for _, opts := range []map[string]any{{"backend": "bad"}, {"backend": "web", "web_url": "https://user:secret@example.test"}, {"backend": "web", "mode": "yolo"}, {"backend": "web", "web_attach_only": "true"}} {
		if _, err := New(opts); err == nil {
			t.Fatalf("accepted %v", opts)
		}
	}
	f := newWebFixture(t)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 40101, "msg": "invalid fixture-token", "data": nil})
	})
	_, err := f.agent.ListSessions(context.Background())
	if err == nil || strings.Contains(err.Error(), "fixture-token") {
		t.Fatalf("token leak or missing error: %v", err)
	}
}

// Opt-in contract check against a separately started Kimi 2.2 server. It creates
// only an empty session; no model request or provider credentials are needed.
func TestWebLiveTwoObservers(t *testing.T) {
	addr := os.Getenv("CC_KIMI_WEB_TEST_URL")
	if addr == "" {
		t.Skip("set CC_KIMI_WEB_TEST_URL, CC_KIMI_WEB_TEST_TOKEN_FILE, CC_KIMI_WEB_TEST_CWD")
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
	if !first.(core.SharedAgentSession).RuntimeState().Connected || !second.(core.SharedAgentSession).RuntimeState().Connected {
		t.Fatal("observers not connected")
	}
	if os.Getenv("CC_KIMI_WEB_TEST_PROMPT") == "1" {
		for _, sender := range []core.AgentSession{first, second} {
			if err := sender.Send("Reply with the fixture answer", "live-test", nil, []core.FileAttachment{{FileName: "测试日志.txt", MimeType: "text/plain", Data: []byte("fixture attachment content")}}); err != nil {
				t.Fatal(err)
			}
			for _, observer := range []core.AgentSession{first, second} {
				e := awaitWebEvent(t, observer.(*webSession), core.EventResult)
				if e.Content != "KIMI LIVE FIXTURE ANSWER" {
					t.Fatalf("unexpected live result: %+v", e)
				}
			}
		}
	}
	if gate := os.Getenv("CC_KIMI_WEB_TEST_MODEL_GATE"); gate != "" {
		if err := os.WriteFile(gate, []byte("hold"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(gate) })
		if err := first.Send("Check the fixture", "steer-live", nil, nil); err != nil {
			t.Fatal(err)
		}
		awaitWebEvent(t, first.(*webSession), core.EventTurnStarted)
		awaitWebEvent(t, second.(*webSession), core.EventTurnStarted)
		outcome, err := second.(core.AgentBestEffortSteerer).SteerCurrent("Also consider this additional instruction")
		if err != nil || outcome != core.SteerInserted {
			t.Fatalf("live steer: %s, %v", outcome, err)
		}
		if err := os.Remove(gate); err != nil {
			t.Fatal(err)
		}
		for _, observer := range []core.AgentSession{first, second} {
			e := awaitWebEvent(t, observer.(*webSession), core.EventResult)
			if !strings.Contains(e.Content, "KIMI LIVE FIXTURE ANSWER") {
				t.Fatalf("live steered result: %+v", e)
			}
		}
	}
	first.Close()
	if !second.Alive() {
		t.Fatal("detach stopped peer")
	}
	list, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list {
		found = found || r.ID == second.CurrentSessionID()
	}
	if !found {
		t.Fatal("session disappeared after detach")
	}
	if _, err := a.(core.HistoryProvider).GetSessionHistory(context.Background(), second.CurrentSessionID(), 10); err != nil {
		t.Fatal(err)
	}
}

func TestWebContextCancellationClosesObserver(t *testing.T) {
	f := newWebFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	as, err := f.agent.AttachSession(ctx, "session_test")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-as.(*webSession).done:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation leaked websocket reader")
	}
}

func TestWebNewSessionAndWorkspaceOptions(t *testing.T) {
	f := newWebFixture(t)
	as, err := f.agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	as.Close()
	opts := f.agent.WorkspaceAgentOptions()
	if opts["backend"] != "web" || opts["web_url"] != f.server.URL {
		t.Fatal(opts)
	}
	if _, ok := opts["work_dir"]; ok {
		t.Fatal("workspace snapshot must not pin working directory")
	}
	f.agent.attachOnly = true
	if _, err := f.agent.StartSession(context.Background(), ""); err == nil {
		t.Fatal("attach-only created a session")
	}
}

func TestWebReconnectFinishesPromptThatWasQueuedBeforeDisconnect(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.queueOnly = true
	f.mu.Unlock()
	s := f.attach()
	id, err := s.SendTurn("queued", "message", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.appendMessage(id, "assistant", "queued answer completed offline")
	for c := range f.peers {
		c.Close()
	}
	f.mu.Unlock()
	e := awaitWebEvent(t, s, core.EventResult)
	if e.TurnID != id || e.Content != "queued answer completed offline" {
		t.Fatal(e)
	}
}
