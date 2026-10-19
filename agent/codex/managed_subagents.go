package codex

import (
	"encoding/json"
	"log/slog"
	"sort"

	"github.com/chenhg5/cc-connect/core"
)

// Collaboration items describe child state independently of the parent turn.
// A completed wait/spawn tool call does not mean that its children completed.
func (s *managedSession) collabUpdates(item map[string]any, previous map[string]core.SubagentNotice) []core.SubagentNotice {
	if item["type"] != "collabAgentToolCall" {
		return nil
	}
	var call struct {
		ID        string   `json:"id"`
		Sender    string   `json:"senderThreadId"`
		Receivers []string `json:"receiverThreadIds"`
		Tool      string   `json:"tool"`
		Status    string   `json:"status"`
		Prompt    string   `json:"prompt"`
		Model     string   `json:"model"`
		States    map[string]struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"agentsStates"`
	}
	raw, err := json.Marshal(item)
	if err != nil {
		slog.Warn("codex managed: encode subagent item", "error", err)
		return nil
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		slog.Warn("codex managed: decode subagent item", "error", err)
		return nil
	}
	root := s.CurrentSessionID()
	if call.Sender != "" && call.Sender != root {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range call.Receivers {
		ids[id] = true
	}
	for id := range call.States {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	var updates []core.SubagentNotice
	for _, id := range ordered {
		if id == "" || id == root {
			continue
		}
		n := previous[id]
		n.ID = id
		if call.Tool == "spawnAgent" {
			if call.Prompt != "" {
				n.Description = call.Prompt
			}
			if call.Model != "" {
				n.Model = call.Model
			}
			n.TaskID = call.ID
		}
		state, exists := call.States[id]
		status := ""
		if exists {
			switch state.Status {
			case "pendingInit":
				status = "queued"
			case "running":
				status = "running"
			case "interrupted":
				status = "suspended"
			case "completed":
				status = "completed"
			case "errored", "notFound":
				status = "failed"
			case "shutdown":
				status = "cancelled"
				if n.Status == "completed" || n.Status == "failed" {
					status = n.Status
				}
			}
		} else if call.Tool == "spawnAgent" && (n.Status == "" || n.Status == "queued") {
			if call.Status == "inProgress" {
				status = "queued"
			}
			if call.Status == "completed" {
				status = "running"
			}
		}
		if status != "" {
			if n.Status != status {
				n.Detail = ""
			}
			n.Status = status
			if state.Message != "" {
				n.Detail = state.Message
			}
		}
		if n.Status != "" {
			updates = append(updates, n)
		}
	}
	return updates
}

// Owned by the event reader, including reconnect reconciliation.
func (s *managedSession) emitSubagent(n core.SubagentNotice) {
	if s.subagents == nil {
		s.subagents = map[string]core.SubagentNotice{}
	}
	old, exists := s.subagents[n.ID]
	if exists && old == n {
		return
	}
	if !exists {
		s.subagentOrder = append(s.subagentOrder, n.ID)
		if len(s.subagentOrder) > 256 {
			delete(s.subagents, s.subagentOrder[0])
			s.subagentOrder = s.subagentOrder[1:]
		}
	}
	s.subagents[n.ID] = n
	s.emit(core.Event{Type: core.EventNotice, Notice: &core.AgentNotice{Kind: "subagent", Subagent: &n}})
}

func (s *managedSession) reconcileSubagents(turns []managedTurn) {
	// Fold history before emitting, so reconnect does not replay running/completed
	// transitions or announce historical children that finished before attachment.
	latest := map[string]core.SubagentNotice{}
	var order []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			for _, n := range s.collabUpdates(item, latest) {
				if n.TurnID == "" {
					n.TurnID = turn.ID
				}
				if _, exists := latest[n.ID]; !exists {
					order = append(order, n.ID)
				}
				latest[n.ID] = n
			}
		}
	}
	for _, id := range order {
		n := latest[id]
		_, known := s.subagents[id]
		if !known && (n.Status == "completed" || n.Status == "failed" || n.Status == "cancelled") {
			continue
		}
		if old, exists := s.subagents[id]; exists {
			if n.Description == "" {
				n.Description = old.Description
			}
			if n.Model == "" {
				n.Model = old.Model
			}
			if n.TaskID == "" {
				n.TaskID = old.TaskID
			}
		}
		s.emitSubagent(n)
	}
}
