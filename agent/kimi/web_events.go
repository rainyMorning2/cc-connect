package kimi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type webEventPayload struct {
	AgentID  string          `json:"agentId"`
	TurnID   int64           `json:"turnId"`
	PromptID string          `json:"promptId"`
	Prompt   string          `json:"prompt"`
	Delta    string          `json:"delta"`
	Reason   string          `json:"reason"`
	Message  string          `json:"message"`
	ToolID   string          `json:"toolCallId"`
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	Output   json.RawMessage `json:"output"`
	IsError  bool            `json:"isError"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Update struct {
		Text string `json:"text"`
	} `json:"update"`
}

func (s *webSession) handleFrame(f webFrame) error {
	if f.SessionID != s.id {
		return nil
	}
	if f.Epoch != "" && f.Epoch != s.epoch {
		return fmt.Errorf("kimi web: event epoch changed")
	}
	// Volatile deltas can share a durable watermark; never drop them on seq alone.
	if !f.Volatile && f.Seq > 0 && f.Seq <= s.seq {
		return nil
	}
	if strings.HasPrefix(f.Type, "subagent.") {
		if err := s.handleSubagentFrame(f); err != nil {
			return err
		}
		if !f.Volatile && f.Seq > s.seq {
			s.seq = f.Seq
		}
		return nil
	}
	var p webEventPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return fmt.Errorf("kimi web: event payload: %w", err)
	}
	if p.AgentID != "" && p.AgentID != "main" {
		if !f.Volatile && f.Seq > s.seq {
			s.seq = f.Seq
		}
		return nil
	}
	if f.Type == "agent.status.updated" {
		if err := s.updateStatus(f.Payload); err != nil {
			return err
		}
	}
	key := s.turnKeys[p.TurnID]
	if f.Type == "turn.started" {
		key = p.PromptID
		if key == "" {
			key = fmt.Sprintf("%s/%d", s.epoch, p.TurnID)
		}
		s.turnKeys[p.TurnID] = key
		if !s.completed[key] {
			s.mu.Lock()
			s.turn, s.prompt = key, p.PromptID
			s.usage.InputTokens, s.usage.OutputTokens = 0, 0
			s.usage.CachedInputTokens, s.usage.CacheCreationInputTokens = 0, 0
			own := s.own[key]
			s.mu.Unlock()
			s.emit(core.Event{Type: core.EventTurnStarted, TurnID: key})
			if !own && p.Prompt != "" {
				s.emit(core.Event{Type: core.EventUserMessage, TurnID: key, Content: p.Prompt})
			}
		}
	}
	if key != "" && !s.completed[key] {
		e := core.Event{TurnID: key, ItemID: key}
		switch f.Type {
		case "assistant.delta":
			s.text[key] += p.Delta
			e.Type = core.EventText
			e.Content = p.Delta
			e.Metadata = map[string]any{"text_delta": true}
			s.emit(e)
		case "thinking.delta":
			e.Type = core.EventThinking
			e.Content = p.Delta
			s.emit(e)
		case "tool.call.started":
			e.Type = core.EventToolUse
			e.ItemID = p.ToolID
			e.ToolName = p.Name
			e.ToolInput = webDisplay(p.Args)
			s.emit(e)
		case "tool.progress":
			e.Type = core.EventToolOutput
			e.ItemID = p.ToolID
			e.Content = p.Update.Text
			s.emit(e)
		case "tool.result":
			e.Type = core.EventToolResult
			e.ItemID = p.ToolID
			e.ToolResult = webDisplay(p.Output)
			ok := !p.IsError
			e.ToolSuccess = &ok
			s.emit(e)
		case "turn.ended":
			if p.Error != nil {
				s.emit(core.Event{Type: core.EventNotice, Notice: &core.AgentNotice{Kind: "warning", Message: p.Error.Message}})
			}
			if err := s.finishTurn(key, p.Reason); err != nil {
				return err
			}
		}
	}
	switch f.Type {
	case "event.approval.requested":
		var a webApproval
		if err := json.Unmarshal(f.Payload, &a); err != nil {
			return fmt.Errorf("kimi web: approval: %w", err)
		}
		s.addApproval(a)
	case "event.question.requested":
		var q webQuestion
		if err := json.Unmarshal(f.Payload, &q); err != nil {
			return fmt.Errorf("kimi web: question: %w", err)
		}
		s.addQuestion(q)
	case "event.approval.resolved", "event.approval.expired", "event.question.answered", "event.question.dismissed":
		var r struct {
			Approval string `json:"approval_id"`
			Question string `json:"question_id"`
		}
		if err := json.Unmarshal(f.Payload, &r); err != nil {
			return err
		}
		id := "approval/" + r.Approval
		if r.Question != "" {
			id = "question/" + r.Question
		}
		s.resolvePending(id)
	case "warning", "error":
		if p.Message != "" {
			s.emit(core.Event{Type: core.EventNotice, Notice: &core.AgentNotice{Kind: "warning", Message: p.Message}})
		}
	}
	if !f.Volatile && f.Seq > s.seq {
		s.seq = f.Seq
	}
	return nil
}
func webDisplay(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}
func (s *webSession) finishTurn(key, reason string) error {
	// Deltas are volatile. Read authoritative persisted text so reconnects cannot
	// silently truncate the final answer. Filter by prompt ID, never "last reply".
	messages, err := s.agent.messages(s.ctx, s.id, 0)
	if err != nil {
		return err
	}
	var parts []string
	for _, m := range messages {
		if m.PromptID == key && m.Role == "assistant" && m.text() != "" {
			parts = append(parts, m.text())
		}
	}
	text := s.text[key]
	if len(parts) > 0 {
		text = strings.Join(parts, "\n\n")
	}
	s.completed[key] = true
	s.completedOrder = append(s.completedOrder, key)
	if len(s.completedOrder) > 256 {
		expired := s.completedOrder[0]
		s.completedOrder = s.completedOrder[1:]
		delete(s.completed, expired)
		for turn, id := range s.turnKeys {
			if id == expired {
				delete(s.turnKeys, turn)
			}
		}
	}
	delete(s.text, key)
	s.mu.Lock()
	if s.turn == key {
		s.turn, s.prompt = "", ""
	}
	delete(s.own, key)
	delete(s.submitted, key)
	usage := s.usage
	s.mu.Unlock()
	status := reason
	if reason == "cancelled" {
		status = "interrupted"
	}
	s.emit(core.Event{
		Type: core.EventResult, TurnID: key, Content: text, Done: true,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadInputTokens: usage.CachedInputTokens, CacheCreationInputTokens: usage.CacheCreationInputTokens,
		Metadata: map[string]any{"turn_status": status},
	})
	return nil
}
