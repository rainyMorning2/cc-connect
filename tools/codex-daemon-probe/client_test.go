package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDiscoveryRequiresRunningDaemonAndReportedSocket(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"running", `{"status":"running","socketPath":"/discovered/socket","cliVersion":"a","appServerVersion":"b"}`, true},
		{"notRunning", `{"status":"notRunning","socketPath":"/discovered/socket"}`, false},
		{"missingSocket", `{"status":"running"}`, false},
		{"invalidJSON", `warning\n{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseDiscovery([]byte(tc.data))
			if (err == nil) != tc.valid {
				t.Fatalf("info=%+v err=%v", info, err)
			}
			if tc.valid && (info.SocketPath != "/discovered/socket" || info.CLIVersion == info.AppServerVersion) {
				t.Fatalf("discovery lost reported fields: %+v", info)
			}
		})
	}
}

func TestDiscoveryOnlyInvokesVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n[ \"$*\" = \"app-server daemon version\" ] || exit 99\nprintf '%s' '{\"status\":\"running\",\"socketPath\":\"/test/socket\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket probe")
	}
	// Short path avoids the 108-byte UDS limit under long temporary roots.
	dir, err := os.MkdirTemp("", "cc-uds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "rpc.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" || r.Host != "localhost" {
			t.Errorf("unexpected UDS handshake: %s %s", r.Host, r.URL.Path)
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		defer close(done)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := c.ReadJSON(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "initialize" {
			t.Errorf("first method: %q", req.Method)
		}
		if err := c.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{}}); err != nil {
			t.Error(err)
			return
		}
		if err := c.ReadJSON(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "initialized" {
			t.Errorf("second method: %q", req.Method)
		}
		handle(c)
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("fixture connection not closed")
		}
	})
	return socket
}

func TestUDSWebSocketPreservesEventsBeforeResponseAndClosesOnlyConnection(t *testing.T) {
	closed := make(chan struct{})
	socket := fixture(t, func(ws *websocket.Conn) {
		var req struct {
			ID int `json:"id"`
		}
		if err := ws.ReadJSON(&req); err != nil {
			t.Error(err)
			return
		}
		// Server request has the SAME numeric ID as the client request. It
		// must not be mistaken for the client's response.
		for _, v := range []any{
			map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "test"}},
			map[string]any{"id": req.ID, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "test"}},
			map[string]any{"id": req.ID, "result": map[string]any{"data": []string{"test"}}},
		} {
			if err := ws.WriteJSON(v); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, err := ws.ReadMessage()
		if err == nil {
			t.Error("client close sent unexpected JSON-RPC instead of closing socket")
		}
		close(closed)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var result struct {
		Data []string `json:"data"`
	}
	if err := c.request(ctx, "thread/loaded/list", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0] != "test" {
		t.Fatalf("result=%+v", result)
	}
	for _, want := range []string{"turn/started", "item/commandExecution/requestApproval"} {
		m, err := c.next(ctx)
		if err != nil || m.Method != want {
			t.Fatalf("message=%+v err=%v want=%s", m, err, want)
		}
	}
	c.close()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("close did not disconnect observer")
	}
}

func TestRPCErrorIsReturned(t *testing.T) {
	socket := fixture(t, func(ws *websocket.Conn) {
		var req message
		if err := ws.ReadJSON(&req); err != nil {
			t.Error(err)
			return
		}
		_ = ws.WriteJSON(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "expectedTurnId mismatch"}})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	err = c.request(ctx, "turn/steer", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "expectedTurnId mismatch") {
		t.Fatalf("err=%v", err)
	}
}

func TestWatchRefusesCurrentAgentThread(t *testing.T) {
	if guardWatch("current", "current") == nil {
		t.Fatal("allowed own thread")
	}
	if guardWatch("", "current") == nil {
		t.Fatal("allowed empty target")
	}
	if err := guardWatch("test", "current"); err != nil {
		t.Fatal(err)
	}
}

func TestActiveTurnRecoveredFromResumeSnapshot(t *testing.T) {
	var s threadSnapshot
	if err := json.Unmarshal([]byte(`{"thread":{"id":"t","turns":[{"id":"old","status":"completed"},{"id":"live","status":"inProgress"}]}}`), &s); err != nil {
		t.Fatal(err)
	}
	if activeTurn(s) != "live" {
		t.Fatal("active turn was not recovered")
	}
}

func TestFindLoadedThreadsUsesAllPagesAndOnlyReadsMetadata(t *testing.T) {
	socket := fixture(t, func(ws *websocket.Conn) {
		for _, step := range []struct {
			method string
			result any
		}{
			{"thread/loaded/list", map[string]any{"data": []string{"other"}, "nextCursor": "page2"}},
			{"thread/read", map[string]any{"thread": map[string]any{"cwd": "/other"}}},
			{"thread/loaded/list", map[string]any{"data": []string{"target"}}},
			{"thread/read", map[string]any{"thread": map[string]any{"cwd": "/target"}}},
		} {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := ws.ReadJSON(&req); err != nil {
				t.Error(err)
				return
			}
			if req.Method != step.method {
				t.Errorf("got %s, want %s", req.Method, step.method)
			}
			if _, ok := req.Params["includeTurns"]; ok {
				t.Error("requested historical content")
			}
			if req.Method == "thread/loaded/list" && req.ID == 4 && req.Params["cursor"] != "page2" {
				t.Error("did not page loaded threads")
			}
			if err := ws.WriteJSON(map[string]any{"id": req.ID, "result": step.result}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ids, err := findLoadedThreads(ctx, c, "/target/.")
	if err != nil || len(ids) != 1 || ids[0] != "target" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}
