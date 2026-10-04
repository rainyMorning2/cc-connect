package main

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testTurnSnapshot(thread, turn string) threadSnapshot {
	var snapshot threadSnapshot
	snapshot.Thread.ID = thread
	snapshot.Thread.Source = json.RawMessage(`"vscode"`)
	if turn != "" {
		data, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "source": "vscode",
			"turns": []any{map[string]any{"id": turn, "status": "inProgress"}}}})
		_ = json.Unmarshal(data, &snapshot)
	}
	return snapshot
}

func TestSteerRejectsWrongThreadMissingTextStaleTurnAndRestrictedThread(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  interactiveControl
	}{
		{"wrongThread", interactiveControl{ThreadID: "other", ExpectedTurnID: "turn", Text: "hello"}},
		{"missingTurn", interactiveControl{Text: "hello"}},
		{"emptyText", interactiveControl{ExpectedTurnID: "turn", Text: " "}},
		{"staleTurn", interactiveControl{ExpectedTurnID: "old", Text: "hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns, err := newTurnControls("target", testTurnSnapshot("target", "turn"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := turns.start(context.Background(), nil, tc.cmd); err == nil {
				t.Fatal("invalid steer reached writer")
			}
		})
	}
	no := false
	snapshots := []threadSnapshot{testTurnSnapshot("target", ""), testTurnSnapshot("target", "turn"), testTurnSnapshot("target", "turn")}
	snapshots[1].Thread.CanAcceptDirectInput = &no
	snapshots[2].Thread.Source = json.RawMessage(`{"subagent":{"thread_spawn":{"parent_thread_id":"root"}}}`)
	for _, snapshot := range snapshots {
		turns, err := newTurnControls("target", snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := turns.start(context.Background(), nil, interactiveControl{ExpectedTurnID: "turn", Text: "hello"}); err == nil {
			t.Fatal("inactive or restricted thread accepted steer")
		}
	}
	if _, err := newTurnControls("target", testTurnSnapshot("other", "turn")); err == nil {
		t.Fatal("attached mismatched snapshot")
	}
}

func TestSteerTurnTrackingIgnoresOtherThreadAndOldCompletion(t *testing.T) {
	turns, err := newTurnControls("target", testTurnSnapshot("target", "turn"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, params, active string }{
		{"turn/completed", `{"threadId":"other","turn":{"id":"turn"}}`, "turn"},
		{"turn/started", `{"threadId":"other","turn":{"id":"other-turn"}}`, "turn"},
		{"turn/started", `{"threadId":"target","turn":{"id":"next"}}`, "next"},
		{"turn/completed", `{"threadId":"target","turn":{"id":"turn"}}`, "next"},
		{"turn/completed", `{"threadId":"target","turn":{"id":"next"}}`, ""},
	} {
		if err := turns.observe(message{Method: tc.method, Params: json.RawMessage(tc.params)}); err != nil {
			t.Fatal(err)
		}
		if turns.active != tc.active {
			t.Fatalf("active turn=%q want %q", turns.active, tc.active)
		}
	}
}

func TestSteerResponseRejectsWrongTurnAndDoesNotConsumeServerRequest(t *testing.T) {
	turns, _ := newTurnControls("target", testTurnSnapshot("target", "turn"))
	turns.pending["42"] = pendingTurnControl{Action: "steer", TurnID: "turn"}
	if _, ok := turns.response(approvalMessage("42", "target", "turn")); ok || len(turns.pending) != 1 {
		t.Fatal("server request consumed client response slot")
	}
	report, ok := turns.response(message{ID: json.RawMessage("42"), Result: json.RawMessage(`{"turnId":"other-turn"}`)})
	if !ok || report["probe"] != "steerRejected" || len(turns.pending) != 0 {
		t.Fatalf("wrong-turn response accepted: %v", report)
	}
}

func TestSteerCancelledWriteDoesNotLeaveOutstandingRequest(t *testing.T) {
	turns, err := newTurnControls("target", testTurnSnapshot("target", "turn"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := turns.start(ctx, &client{}, interactiveControl{ExpectedTurnID: "turn", Text: "hello"}); err == nil {
		t.Fatal("cancelled write accepted")
	}
	if len(turns.pending) != 0 {
		t.Fatal("failed write left an outstanding request")
	}
	for i := 0; i < 16; i++ {
		turns.pending[strconv.Itoa(i)] = pendingTurnControl{Action: "steer", TurnID: "turn"}
	}
	if _, err := turns.start(context.Background(), nil, interactiveControl{ExpectedTurnID: "turn", Text: "hello"}); err == nil {
		t.Fatal("allowed unbounded pending steer requests")
	}
}

func TestInteractiveSteerSingleReaderPreservesApprovalAndReturnsRPCFailure(t *testing.T) {
	socket := fixture(t, func(ws *websocket.Conn) {
		for i := 0; i < 2; i++ {
			var req struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := ws.ReadJSON(&req); err != nil {
				t.Error(err)
				return
			}
			if req.Method != "turn/steer" || !strings.Contains(string(req.Params), `"expectedTurnId":"turn"`) || !strings.Contains(string(req.Params), `"threadId":"target"`) {
				t.Errorf("unexpected control request: %+v", req)
			}
			if i == 0 {
				if err := ws.WriteJSON(approvalMessage(strconv.Itoa(req.ID), "target", "turn")); err != nil {
					t.Error(err)
					return
				}
				_ = ws.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{"turnId": "turn"}})
			} else {
				_ = ws.WriteJSON(map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": "no active turn"}})
			}
		}
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Error("steer rejection caused an automatic retry or new turn")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	stopClose := context.AfterFunc(ctx, func() { c.close(); _ = outR.Close() })
	defer stopClose()
	done := make(chan error, 1)
	go func() {
		done <- watchInteractive(ctx, c, "target", json.NewEncoder(outW), inR, testTurnSnapshot("target", "turn"))
	}()
	decoder := json.NewDecoder(outR)
	read := func(key, value string) {
		t.Helper()
		var report map[string]json.RawMessage
		if err := decoder.Decode(&report); err != nil {
			t.Fatal(err)
		}
		if string(report[key]) != value {
			t.Fatalf("unexpected output: %v", report)
		}
	}
	send := func(turn string) {
		b, _ := json.Marshal(interactiveControl{Action: "steer", ExpectedTurnID: turn, Text: "继续验证"})
		_, _ = inW.Write(append(b, '\n'))
	}
	send("turn")
	read("probe", `"steerRequestSent"`)
	read("method", `"item/commandExecution/requestApproval"`)
	read("probe", `"steerAccepted"`)
	send("old")
	read("probe", `"controlRejected"`)
	_, _ = io.WriteString(inW, "{\"action\":\"pending\"}\n")
	read("probe", `"pendingApprovals"`)
	send("turn")
	read("probe", `"steerRequestSent"`)
	read("probe", `"steerRejected"`)
	_, _ = io.WriteString(inW, "{\"action\":\"detach\"}\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watch did not detach")
	}
}
