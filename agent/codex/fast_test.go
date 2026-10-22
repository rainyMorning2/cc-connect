package codex

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedFast_UpdatesOnlyAttachedThreadAndTracksExternalChanges(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	tier := "priority"
	var updates []map[string]any
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			mu.Lock()
			defer mu.Unlock()
			switch m.Method {
			case "thread/resume":
				snapshot := fixtureSnapshot(cwd, "target", "")
				snapshot["serviceTier"] = tier
				return snapshot, true
			case "thread/settings/update":
				var params map[string]any
				if err := json.Unmarshal(m.Params, &params); err != nil {
					t.Error(err)
				}
				updates = append(updates, params)
				tier, _ = params["serviceTier"].(string)
				return map[string]any{}, true
			}
			t.Errorf("unexpected method %s", m.Method)
			return map[string]any{}, true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, nil)
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	s := as.(*managedSession)
	if s.GetServiceTier() != "priority" {
		t.Fatalf("attach lost Fast tier: %q", s.GetServiceTier())
	}
	for _, want := range []string{"default", "fast"} {
		if err := s.SetServiceTier(context.Background(), want); err != nil {
			t.Fatal(err)
		}
		if s.GetServiceTier() != want {
			t.Fatalf("tier = %q, want %q", s.GetServiceTier(), want)
		}
	}
	mu.Lock()
	got := append([]map[string]any(nil), updates...)
	mu.Unlock()
	want := []map[string]any{{"threadId": "target", "serviceTier": "default"}, {"threadId": "target", "serviceTier": "fast"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %#v", got)
	}
	if a.GetServiceTier() != "" {
		t.Fatal("changed agent defaults")
	}
	for _, tier := range []any{"priority", nil, "fast", "default"} {
		raw, _ := json.Marshal(map[string]any{"threadId": "target", "threadSettings": map[string]any{"model": "external-model", "effort": "high", "serviceTier": tier}})
		if err := s.handleMessage(daemonMessage{Method: "thread/settings/updated", Params: raw}); err != nil {
			t.Fatal(err)
		}
		want, _ := tier.(string)
		if s.GetServiceTier() != want {
			t.Fatalf("external tier = %q, want %q", s.GetServiceTier(), want)
		}
	}
	// A metadata read also refreshes tier, including clearing a stale Fast value.
	mu.Lock()
	tier = "default"
	mu.Unlock()
	if _, err := s.ReadRuntimeSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.GetServiceTier() != "default" {
		t.Fatal("metadata read did not refresh tier")
	}
}

func TestManagedFast_ReportsRejectionAndBusy(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPCWithEmptyHistory(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			if m.Method == "thread/settings/update" {
				if err := ws.WriteJSON(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "tier unavailable"}}); err != nil {
					t.Error(err)
				}
				return nil, false
			}
			return map[string]any{}, true
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	s := as.(*managedSession)
	if err := s.SetServiceTier(context.Background(), "fast"); err == nil || !strings.Contains(err.Error(), "tier unavailable") {
		t.Fatalf("rejection = %v", err)
	}
	if s.GetServiceTier() != "" {
		t.Fatal("rejection changed tier")
	}
	s.mu.Lock()
	s.turn = "busy"
	s.mu.Unlock()
	if err := s.SetServiceTier(context.Background(), "fast"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy error = %v", err)
	}
}

func TestExecFast_PreservesResumeAndOverridesTier(t *testing.T) {
	cs := &codexSession{workDir: t.TempDir(), model: "model", effort: "high"}
	cs.threadID.Store("existing-thread")
	for _, tier := range []string{"fast", "default"} {
		if err := cs.SetServiceTier(context.Background(), tier); err != nil {
			t.Fatal(err)
		}
		args := strings.Join(cs.buildExecArgs("hello", nil), " ")
		if !strings.Contains(args, "exec resume") || !strings.Contains(args, "existing-thread") || !strings.Contains(args, `service_tier="`+tier+`"`) {
			t.Fatalf("args = %s", args)
		}
		if cs.GetServiceTier() != tier {
			t.Fatalf("tier = %q", cs.GetServiceTier())
		}
	}
	cs.turnInFlight.Store(true)
	if err := cs.SetServiceTier(context.Background(), "fast"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy error = %v", err)
	}
}

func TestAppServerFast_SettingsNotificationsClearFast(t *testing.T) {
	s := &appServerSession{}
	s.threadID.Store("target")
	for _, tier := range []any{"priority", nil, "fast", "default"} {
		raw, _ := json.Marshal(map[string]any{"threadId": "target", "threadSettings": map[string]any{"model": "model", "effort": "high", "serviceTier": tier}})
		s.handleNotification("thread/settings/updated", raw)
		want, _ := tier.(string)
		if s.GetServiceTier() != want {
			t.Fatalf("tier = %q, want %q", s.GetServiceTier(), want)
		}
	}
	s.storeServiceTier("fast")
	if s.threadRequestParams()["serviceTier"] != "fast" {
		t.Fatal("thread creation/resume omitted tier")
	}
}

type fastRPCWriter func([]byte) (int, error)

func (w fastRPCWriter) Write(b []byte) (int, error) { return w(b) }
func (w fastRPCWriter) Close() error                { return nil }

func TestAppServerFast_RequestsAndServerRejection(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), workDir: t.TempDir(), serviceTier: "fast"}
	s.alive.Store(true)
	var mu sync.Mutex
	var calls []map[string]any
	reject := false
	s.stdin = fastRPCWriter(func(b []byte) (int, error) {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return 0, err
		}
		mu.Lock()
		calls = append(calls, req.Params)
		denied := reject
		mu.Unlock()
		resp := rpcResponseEnvelope{ID: req.ID}
		switch req.Method {
		case "thread/start":
			resp.Result = json.RawMessage(`{"model":"model","reasoningEffort":"high","serviceTier":"priority","thread":{"id":"target"}}`)
		case "turn/start":
			resp.Result = json.RawMessage(`{"turn":{"id":"turn"}}`)
		case "thread/settings/update":
			if denied {
				resp.Error = &rpcError{Message: "tier denied"}
			}
		default:
			return 0, errors.New("unexpected RPC method: " + req.Method)
		}
		s.handleResponse(resp)
		return len(b), nil
	})
	if err := s.ensureThread(""); err != nil {
		t.Fatal(err)
	}
	if s.GetServiceTier() != "priority" {
		t.Fatal("start response lost tier")
	}
	if err := s.Send("hello", "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServiceTier(context.Background(), "default"); !errors.Is(err, core.ErrAgentTurnBusy) {
		t.Fatalf("busy error = %v", err)
	}
	// Simulate the transport reporting that the active turn is finished.
	s.stateMu.Lock()
	s.currentTurn = ""
	s.stateMu.Unlock()
	if err := s.SetServiceTier(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	reject = true
	mu.Unlock()
	if err := s.SetServiceTier(context.Background(), "fast"); err == nil || !strings.Contains(err.Error(), "tier denied") {
		t.Fatalf("rejection = %v", err)
	}
	if s.GetServiceTier() != "default" {
		t.Fatal("rejected update changed tier")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls[0]["serviceTier"] != "fast" || calls[1]["serviceTier"] != "priority" || calls[2]["serviceTier"] != "default" {
		t.Fatalf("tier parameters = %#v", calls)
	}
	if len(calls[2]) != 2 || calls[2]["threadId"] != "target" {
		t.Fatalf("Fast changed other settings: %#v", calls[2])
	}
}
