package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type pendingApproval struct {
	RequestID          json.RawMessage   `json:"requestId"`
	Method             string            `json:"method"`
	ThreadID           string            `json:"threadId"`
	TurnID             string            `json:"turnId"`
	Command            *string           `json:"command,omitempty"`
	AvailableDecisions []json.RawMessage `json:"availableDecisions,omitempty"`
}

// This tracker is owned by the interactive loop. It never auto-approves and
// never permits a response to requests outside the explicitly attached thread.
type approvalTracker struct {
	thread  string
	pending map[string]pendingApproval
}

func approvalID(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("invalid requestId: %w", err)
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid requestId JSON")
	}
	switch v := v.(type) {
	case string:
		return "string:" + v, nil
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return "", fmt.Errorf("requestId must be an integer or string")
		}
		return "number:" + strconv.FormatInt(n, 10), nil
	default:
		return "", fmt.Errorf("requestId must be an integer or string")
	}
}

func (t *approvalTracker) observe(m message) error {
	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var a pendingApproval
		if err := json.Unmarshal(m.Params, &a); err != nil {
			return err
		}
		if a.ThreadID != t.thread {
			return nil
		}
		key, err := approvalID(m.ID)
		if err != nil {
			return err
		}
		a.RequestID = append(json.RawMessage(nil), m.ID...)
		a.Method = m.Method
		t.pending[key] = a
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string          `json:"threadId"`
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadID != t.thread {
			return nil
		}
		key, err := approvalID(p.RequestID)
		if err != nil {
			return err
		}
		delete(t.pending, key)
	case "turn/completed":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadID == t.thread {
			for key, approval := range t.pending {
				if approval.TurnID == p.Turn.ID {
					delete(t.pending, key)
				}
			}
		}
	}
	return nil
}

type approvalControl struct {
	Action    string          `json:"action"`
	RequestID json.RawMessage `json:"requestId,omitempty"`
	Decision  string          `json:"decision,omitempty"`
}

func (t *approvalTracker) reply(control approvalControl, write func(any) error) error {
	key, err := approvalID(control.RequestID)
	if err != nil {
		return err
	}
	a, ok := t.pending[key]
	if !ok {
		return fmt.Errorf("request is no longer pending or belongs to another thread")
	}
	if control.Decision != "accept" && control.Decision != "decline" {
		return fmt.Errorf("only accept or decline is supported; no persistent policy changes")
	}
	if a.AvailableDecisions != nil {
		allowed := false
		for _, raw := range a.AvailableDecisions {
			var decision string
			if json.Unmarshal(raw, &decision) == nil && decision == control.Decision {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("decision is not offered by this approval request")
		}
	}
	if err := write(map[string]any{"id": a.RequestID, "result": map[string]any{"decision": control.Decision}}); err != nil {
		return err
	}
	// The server's resolved notification confirms the outcome. Locally remove
	// the request immediately so a second click never sends another response.
	delete(t.pending, key)
	return nil
}
