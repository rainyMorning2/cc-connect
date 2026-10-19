package kimi

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type webTasksFixture struct {
	*webFixture
	mu              sync.Mutex
	tasks           map[string]webTask
	cancelled       []string
	cancelCode      int
	cancelConfirmed bool
}

func newWebTasksFixture(t *testing.T) *webTasksFixture {
	f := &webTasksFixture{webFixture: newWebFixture(t), tasks: map[string]webTask{}, cancelConfirmed: true}
	for _, task := range []webTask{
		{ID: "shell-a", Kind: "bash", Status: "running", Background: true, Command: "sleep 60"},
		{ID: "shell-b", Kind: "bash", Status: "running", Background: true, Command: "watch build"},
		{ID: "foreground", Kind: "bash", Status: "running", Command: "foreground command"},
		{ID: "agent-a", Kind: "subagent", Status: "running", Background: true, Description: "review code"},
		{ID: "tool-a", Kind: "tool", Status: "running", Background: true},
		{ID: "finished", Kind: "bash", Status: "completed", Background: true},
	} {
		task.SessionID = "session_test"
		f.tasks[task.ID] = task
	}
	original := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/api/v1/sessions/session_test/tasks"
		if !strings.HasPrefix(r.URL.Path, base) {
			original.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == "GET" && r.URL.Path == base {
			if r.URL.Query().Get("status") != "running" {
				t.Error("missing running filter")
			}
			var items []webTask
			for _, task := range f.tasks {
				items = append(items, task)
			} // Verify client filtering too.
			f.reply(w, map[string]any{"items": items})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		if r.Method == "POST" {
			if !strings.HasSuffix(id, ":cancel") {
				t.Errorf("unexpected mutation: %s", r.URL.Path)
				return
			}
			id = strings.TrimSuffix(id, ":cancel")
			f.cancelled = append(f.cancelled, id)
			if f.cancelCode != 0 {
				fmt.Fprintf(w, `{"code":%d,"msg":"cancel failed","data":null}`, f.cancelCode)
				return
			}
			task := f.tasks[id]
			task.Status = "cancelled"
			f.tasks[id] = task
			f.reply(w, map[string]any{"cancelled": f.cancelConfirmed})
			return
		}
		if task, ok := f.tasks[id]; ok {
			f.reply(w, task)
		} else {
			fmt.Fprint(w, `{"code":40406,"msg":"missing task","data":null}`)
		}
	})
	return f
}

func TestWebTerminals_OnlyRunningBackgroundShellsCanBeStopped(t *testing.T) {
	f := newWebTasksFixture(t)
	s := f.attach()
	list, err := s.ListBackgroundTerminals(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("background shells: %+v %v", list, err)
	}
	for _, item := range list {
		if item.ID != "shell-a" && item.ID != "shell-b" {
			t.Fatalf("non-shell exposed: %+v", item)
		}
	}
	for _, id := range []string{"agent-a", "tool-a", "foreground", "missing", "../shell-a", "shell-a:detach"} {
		if err := s.TerminateBackgroundTerminal(context.Background(), id); err == nil {
			t.Fatalf("accepted non-background shell %s", id)
		}
	}
	f.mu.Lock()
	foreign := f.tasks["shell-b"]
	foreign.SessionID = "other-session"
	f.tasks["shell-b"] = foreign
	f.mu.Unlock()
	if err := s.TerminateBackgroundTerminal(context.Background(), "shell-b"); err == nil {
		t.Fatal("foreign session task accepted")
	}
	if err := s.TerminateBackgroundTerminal(context.Background(), "shell-a"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cancelled) != 1 || f.cancelled[0] != "shell-a" {
		t.Fatalf("unexpected cancellations: %v", f.cancelled)
	}
}

func TestWebTerminals_CancellationRaceAndErrors(t *testing.T) {
	for _, code := range []int{40904, 50001} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newWebTasksFixture(t)
			f.cancelCode = code
			s := f.attach()
			err := s.TerminateBackgroundTerminal(context.Background(), "shell-a")
			if (code == 40904) != (err == nil) {
				t.Fatalf("code=%d err=%v", code, err)
			}
		})
	}
	f := newWebTasksFixture(t)
	f.cancelConfirmed = false
	if err := f.attach().TerminateBackgroundTerminal(context.Background(), "shell-a"); err == nil {
		t.Fatal("unconfirmed cancellation succeeded")
	}
}

func TestWebEngine_TerminalsStopAllPreservesSubagentsAndObserver(t *testing.T) {
	f := newWebTasksFixture(t)
	p := &webWorkspacePlatform{}
	e := core.NewEngine("tasks", f.agent, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	e.SetAdminFrom("user")
	defer e.Stop()
	send := func(cmd string) string {
		before := p.getSent()
		e.ReceiveMessage(p, &core.Message{SessionKey: "test:user", UserID: "user", Platform: "test", Content: cmd, ReplyCtx: "user"})
		return strings.TrimPrefix(p.getSent(), before)
	}
	send("/attach session_test")
	if text := send("/terminals"); !strings.Contains(text, "sleep 60") || strings.Contains(text, "review code") {
		t.Fatal(text)
	}
	if text := send("/terminals stop all"); !strings.Contains(text, "2") {
		t.Fatal(text)
	}
	if text := send("/terminals"); strings.Contains(text, "sleep 60") || strings.Contains(text, "watch build") {
		t.Fatal(text)
	}
	if text := send("/model"); !strings.Contains(text, "Current session") {
		t.Fatalf("observer lost: %s", text)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cancelled) != 2 || f.tasks["agent-a"].Status != "running" || f.tasks["foreground"].Status != "running" {
		t.Fatalf("unexpected task cleanup: %v", f.cancelled)
	}
}
