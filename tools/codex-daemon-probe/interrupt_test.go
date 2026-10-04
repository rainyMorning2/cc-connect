package main

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"testing"
)

func TestInterruptPinsAttachedActiveTurn(t *testing.T) {
	for _, cmd := range []interactiveControl{
		{Action: "interrupt"},
		{Action: "interrupt", ExpectedTurnID: "old"},
		{Action: "interrupt", ExpectedTurnID: "turn", ThreadID: "other"},
	} {
		controls, _ := newTurnControls("target", testTurnSnapshot("target", "turn"))
		if _, err := controls.start(context.Background(), nil, cmd); err == nil {
			t.Fatal("invalid interrupt reached writer")
		}
	}
	socket := fixture(t, func(ws *websocket.Conn) {
		var req struct {
			ID     int                        `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := ws.ReadJSON(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "turn/interrupt" || string(req.Params["threadId"]) != `"target"` || string(req.Params["turnId"]) != `"turn"` || len(req.Params) != 2 {
			t.Errorf("wrong interrupt wire request: %+v", req)
		}
		if err := ws.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{}}); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	controls, _ := newTurnControls("target", testTurnSnapshot("target", "turn"))
	if _, err := controls.start(ctx, c, interactiveControl{Action: "interrupt", ExpectedTurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	m, err := c.next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := controls.response(m)
	if !ok || report["probe"] != "interruptAccepted" || controls.active != "turn" {
		t.Fatalf("RPC ack must not mark turn completed: %v", report)
	}
	if err := controls.observe(message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"turn","status":"interrupted"}}`)}); err != nil {
		t.Fatal(err)
	}
	if controls.active != "" {
		t.Fatal("interrupted turn remained active")
	}
}

func TestInterruptRejectsMalformedResponseAndDoesNotConsumeServerRequest(t *testing.T) {
	for _, result := range []string{`null`, `{"turnId":"turn"}`, `[]`} {
		controls, _ := newTurnControls("target", testTurnSnapshot("target", "turn"))
		controls.pending["42"] = pendingTurnControl{Action: "interrupt", TurnID: "turn"}
		if _, ok := controls.response(approvalMessage("42", "target", "turn")); ok {
			t.Fatal("approval mistaken for interrupt ack")
		}
		report, ok := controls.response(message{ID: json.RawMessage("42"), Result: json.RawMessage(result)})
		if !ok || report["probe"] != "interruptRejected" {
			t.Fatalf("malformed interrupt ack: %v", report)
		}
	}
}

func TestTurnControlsMatchMixedInterruptAndSteerResponses(t *testing.T) {
	controls, _ := newTurnControls("target", testTurnSnapshot("target", "turn"))
	controls.pending["1"] = pendingTurnControl{Action: "steer", TurnID: "turn"}
	controls.pending["2"] = pendingTurnControl{Action: "interrupt", TurnID: "turn"}
	report, ok := controls.response(message{ID: json.RawMessage("2"), Result: json.RawMessage(`{}`)})
	if !ok || report["probe"] != "interruptAccepted" || len(controls.pending) != 1 {
		t.Fatalf("wrong interrupt correlation: %v", report)
	}
	report, ok = controls.response(message{ID: json.RawMessage("1"), Result: json.RawMessage(`{"turnId":"turn"}`)})
	if !ok || report["probe"] != "steerAccepted" || len(controls.pending) != 0 {
		t.Fatalf("wrong steer correlation: %v", report)
	}
	var failure message
	if err := json.Unmarshal([]byte(`{"id":3,"error":{"code":-32600,"message":"turn already completed"}}`), &failure); err != nil {
		t.Fatal(err)
	}
	controls.pending["3"] = pendingTurnControl{Action: "interrupt", TurnID: "turn"}
	report, ok = controls.response(failure)
	if !ok || report["probe"] != "interruptRejected" || report["reason"] != "turn already completed" {
		t.Fatalf("interrupt RPC failure lost: %v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := controls.start(ctx, &client{}, interactiveControl{Action: "interrupt", ExpectedTurnID: "turn"}); err == nil || len(controls.pending) != 0 {
		t.Fatal("failed interrupt write left outstanding request")
	}
}
