package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.ContextUsageReporter = (*webSession)(nil)

// Kimi sends partial status updates. Pointers distinguish omitted fields from
// explicit resets, including zero usage after a new turn or compaction.
type webStatus struct {
	Model            *string `json:"model"`
	ThinkingEffort   *string `json:"thinkingEffort"`
	ContextTokens    *int    `json:"contextTokens"`
	MaxContextTokens *int    `json:"maxContextTokens"`
	Usage            *struct {
		CurrentTurn *struct {
			InputOther         int `json:"inputOther"`
			Output             int `json:"output"`
			InputCacheRead     int `json:"inputCacheRead"`
			InputCacheCreation int `json:"inputCacheCreation"`
		} `json:"currentTurn"`
	} `json:"usage"`
}

func (s *webSession) GetModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

func (s *webSession) GetReasoningEffort() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effort
}

func (s *webSession) GetWorkDir() string { return s.workDir }

func (s *webSession) GetContextUsage() *core.ContextUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasUsage {
		return nil
	}
	usage := s.usage
	return &usage
}

func (s *webSession) updateRecordStatusLocked(record webRecord) {
	if record.AgentConfig.Model != "" {
		s.model = record.AgentConfig.Model
	}
	if record.Usage.ContextLimit > 0 {
		s.hasUsage = true
		s.usage.UsedTokens = record.Usage.ContextTokens
		s.usage.ContextWindow = record.Usage.ContextLimit
	}
}

func (s *webSession) updateStatus(raw json.RawMessage) error {
	var status webStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("kimi web: status update: %w", err)
	}
	s.applyStatus(status)
	return nil
}

// /status is the server's current runtime state, including resolved defaults.
// Read it only on attachment/reconnection; subsequent updates arrive over WS.
func (s *webSession) refreshRuntimeStatus() {
	var status struct {
		Model            *string `json:"model"`
		ThinkingLevel    *string `json:"thinking_level"`
		ContextTokens    *int    `json:"context_tokens"`
		MaxContextTokens *int    `json:"max_context_tokens"`
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	if err := s.agent.api.request(ctx, "GET", webSessionPath(s.id)+"/status", nil, &status); err != nil {
		slog.Warn("kimi web: read runtime status; retaining snapshot status", "session_id", s.id, "error", err)
		return
	}
	s.applyStatus(webStatus{Model: status.Model, ThinkingEffort: status.ThinkingLevel, ContextTokens: status.ContextTokens, MaxContextTokens: status.MaxContextTokens})
}

func (s *webSession) applyStatus(status webStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status.Model != nil {
		s.model = *status.Model
	}
	if status.ThinkingEffort != nil {
		s.effort = *status.ThinkingEffort
	}
	if status.ContextTokens != nil {
		s.hasUsage = true
		s.usage.UsedTokens = *status.ContextTokens
	}
	if status.MaxContextTokens != nil {
		s.hasUsage = true
		s.usage.ContextWindow = *status.MaxContextTokens
	}
	if status.Usage != nil && status.Usage.CurrentTurn != nil {
		u := status.Usage.CurrentTurn
		s.hasUsage = true
		s.usage.InputTokens = u.InputOther
		s.usage.OutputTokens = u.Output
		s.usage.CachedInputTokens = u.InputCacheRead
		s.usage.CacheCreationInputTokens = u.InputCacheCreation
	}
}
