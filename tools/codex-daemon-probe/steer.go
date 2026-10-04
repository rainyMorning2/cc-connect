package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Owned by the interactive loop. The event goroutine remains the only reader;
// steering sends a request without calling client.request's synchronous read.
type turnControls struct {
	thread   string
	active   string
	canSteer bool
	pending  map[string]pendingTurnControl // client RPC ID -> pinned action and turn
}

type pendingTurnControl struct {
	Action string
	TurnID string
}

func newTurnControls(thread string, snapshot threadSnapshot) (*turnControls, error) {
	if snapshot.Thread.ID != thread {
		return nil, fmt.Errorf("resume snapshot does not match attached thread")
	}
	var source map[string]json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(string(snapshot.Thread.Source)), "{") {
		if err := json.Unmarshal(snapshot.Thread.Source, &source); err != nil {
			return nil, fmt.Errorf("decode thread source: %w", err)
		}
	}
	_, subagent := source["subagent"]
	allowed := snapshot.Thread.CanAcceptDirectInput
	return &turnControls{thread: thread, active: activeTurn(snapshot), canSteer: !subagent && (allowed == nil || *allowed), pending: map[string]pendingTurnControl{}}, nil
}

func (t *turnControls) observe(m message) error {
	if m.Method != "turn/started" && m.Method != "turn/completed" {
		return nil
	}
	var p struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return fmt.Errorf("decode turn state: %w", err)
	}
	if p.ThreadID == t.thread {
		if m.Method == "turn/started" {
			t.active = p.Turn.ID
		} else if t.active == p.Turn.ID {
			t.active = ""
		}
	}
	return nil
}

func (t *turnControls) start(ctx context.Context, c *client, cmd interactiveControl) (int, error) {
	action := cmd.Action
	if action == "" {
		action = "steer"
	}
	if action != "steer" && action != "interrupt" {
		return 0, fmt.Errorf("unsupported turn action")
	}
	if cmd.ThreadID != "" && cmd.ThreadID != t.thread {
		return 0, fmt.Errorf("%s can only target the attached thread", action)
	}
	if action == "steer" && !t.canSteer {
		return 0, fmt.Errorf("steer requires an attached root thread accepting direct input")
	}
	if cmd.ExpectedTurnID == "" {
		return 0, fmt.Errorf("%s requires expectedTurnId", action)
	}
	if action == "steer" && strings.TrimSpace(cmd.Text) == "" {
		return 0, fmt.Errorf("steer requires non-empty text")
	}
	if t.active == "" || cmd.ExpectedTurnID != t.active {
		return 0, fmt.Errorf("expectedTurnId does not match an attached active turn")
	}
	if len(t.pending) >= 16 {
		return 0, fmt.Errorf("too many outstanding turn control requests")
	}
	c.nextID++
	id := c.nextID
	params := map[string]any{"threadId": t.thread, "turnId": cmd.ExpectedTurnID}
	if action == "steer" {
		params = map[string]any{"threadId": t.thread, "expectedTurnId": cmd.ExpectedTurnID, "input": input(cmd.Text)}
	}
	if err := c.write(ctx, map[string]any{"id": id, "method": "turn/" + action, "params": params}); err != nil {
		return 0, err
	}
	t.pending[fmt.Sprint(id)] = pendingTurnControl{Action: action, TurnID: cmd.ExpectedTurnID}
	return id, nil
}

// Server requests may share numeric IDs with client requests. Only a message
// without a method is eligible for client-response matching.
func (t *turnControls) response(m message) (map[string]any, bool) {
	if m.Method != "" {
		return nil, false
	}
	pending, ok := t.pending[string(m.ID)]
	if !ok {
		return nil, false
	}
	delete(t.pending, string(m.ID))
	report := map[string]any{"probe": pending.Action + "Accepted", "threadId": t.thread, "expectedTurnId": pending.TurnID, "requestId": m.ID}
	if m.Error != nil {
		report["probe"], report["code"], report["reason"] = pending.Action+"Rejected", m.Error.Code, m.Error.Message
		return report, true
	}
	if pending.Action == "interrupt" {
		var result map[string]json.RawMessage
		if err := json.Unmarshal(m.Result, &result); err != nil || result == nil || len(result) != 0 {
			report["probe"], report["reason"] = "interruptRejected", "invalid interrupt response"
		}
		return report, true
	}
	var result struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil || result.TurnID != pending.TurnID {
		report["probe"], report["reason"] = "steerRejected", "response did not confirm the expected turnId"
		return report, true
	}
	report["turnId"] = result.TurnID
	return report, true
}
