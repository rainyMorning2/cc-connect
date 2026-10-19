package kimi

import (
	"encoding/json"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

// Lifecycle notifications are advisory and never touch main-turn state or text.
func (s *webSession) handleSubagentFrame(f webFrame) error {
	var p struct {
		ID          string `json:"subagentId"`
		Name        string `json:"subagentName"`
		TaskID      string `json:"taskId"`
		Description string `json:"description"`
		Model       string `json:"model"`
		Reason      string `json:"reason"`
		Result      string `json:"resultSummary"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return fmt.Errorf("kimi web: subagent event: %w", err)
	}
	update := core.SubagentNotice{ID: p.ID, Name: p.Name, TaskID: p.TaskID, Description: p.Description, Model: p.Model}
	switch f.Type {
	case "subagent.spawned":
		update.Status = "queued"
	case "subagent.started":
		update.Status = "running"
	case "subagent.suspended":
		update.Status = "suspended"
		update.Detail = p.Reason
	case "subagent.completed":
		update.Status = "completed"
		update.Detail = p.Result
	case "subagent.failed":
		update.Status = "failed"
		update.Detail = p.Error
	case "subagent.cancelled":
		update.Status = "cancelled"
	default:
		return nil
	}
	s.emitSubagent(update)
	return nil
}

func (s *webSession) emitSubagent(update core.SubagentNotice) {
	if update.ID == "" {
		return
	}
	if s.subagents == nil {
		s.subagents = map[string]core.SubagentNotice{}
	}
	previous, exists := s.subagents[update.ID]
	update.TurnID = previous.TurnID
	if update.TurnID == "" {
		s.mu.Lock()
		update.TurnID = s.turn
		s.mu.Unlock()
	}
	if update.Name == "" {
		update.Name = previous.Name
	}
	if update.TaskID == "" {
		update.TaskID = previous.TaskID
	}
	if update.Description == "" {
		update.Description = previous.Description
	}
	if update.Model == "" {
		update.Model = previous.Model
	}
	if update.Detail == "" && update.Status == previous.Status {
		update.Detail = previous.Detail
	}
	if exists && previous == update {
		return
	}
	if !exists {
		s.subagentOrder = append(s.subagentOrder, update.ID)
		if len(s.subagentOrder) > 256 {
			delete(s.subagents, s.subagentOrder[0])
			s.subagentOrder = s.subagentOrder[1:]
		}
	}
	s.subagents[update.ID] = update
	s.emit(core.Event{Type: core.EventNotice, Notice: &core.AgentNotice{Kind: "subagent", Subagent: &update}})
}

func (s *webSession) emitSnapshotSubagents(tasks []webTask) {
	for _, task := range tasks {
		if task.SessionID != s.id || task.Kind != "subagent" || task.AgentID == "" {
			continue
		}
		phase := task.Phase
		if phase == "" {
			phase = task.Status
		}
		if phase == "working" {
			phase = "running"
		}
		// On initial attach, only announce live work. On reconnect, reconcile known agents.
		if _, known := s.subagents[task.AgentID]; !known && task.Status != "running" {
			continue
		}
		s.emitSubagent(core.SubagentNotice{ID: task.AgentID, Name: task.AgentType, TaskID: task.ID, Description: task.Description, Model: task.Model, Status: phase, Detail: task.SuspendedReason})
	}
}
