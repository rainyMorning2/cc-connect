package kimi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
)

var _ core.AgentBestEffortSteerer = (*webSession)(nil)

// SteerCurrent matches Kimi Web's submit-then-steer operation. The server chooses
// the turn at injection time. Once submitted, this method never submits again,
// aborts the prompt, or reports a plain failure that could invite a blind retry.
func (s *webSession) SteerCurrent(text string) (core.SteerOutcome, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("kimi web: steer text is empty")
	}
	if state := s.RuntimeState(); !state.Connected {
		return "", fmt.Errorf("kimi web: observer disconnected")
	}
	id := "prompt_" + uuid.NewString()
	body, err := s.promptBody(id, []map[string]any{{"type": "text", "text": text}})
	if err != nil {
		return "", err
	}
	prompt, err := s.postPrompt(body)
	if err != nil {
		var apiErr *webAPIError
		if errors.As(err, &apiErr) {
			return "", err
		}
		slog.Warn("kimi web: steer submission result unknown", "error", err)
		return core.SteerUnconfirmed, nil
	}
	if prompt.ID != id {
		return core.SteerUnconfirmed, nil
	}
	if prompt.Status == "running" {
		return core.SteerRunning, nil
	}
	var result struct {
		Steered bool     `json:"steered"`
		IDs     []string `json:"prompt_ids"`
	}
	err = s.agent.api.request(s.ctx, "POST", webSessionPath(s.id)+"/prompts/"+url.PathEscape(id)+":steer", nil, &result)
	if err == nil && result.Steered {
		for _, got := range result.IDs {
			if got == id {
				return core.SteerInserted, nil
			}
		}
	}
	if err != nil {
		slog.Warn("kimi web: steer not confirmed; checking accepted prompt", "error", err)
	}
	return s.steerPromptStatus(id), nil
}

func (s *webSession) steerPromptStatus(id string) core.SteerOutcome {
	var queue struct {
		Active *webPrompt  `json:"active"`
		Queued []webPrompt `json:"queued"`
	}
	if err := s.agent.api.request(s.ctx, "GET", webSessionPath(s.id)+"/prompts", nil, &queue); err != nil {
		slog.Warn("kimi web: read steer prompt status", "error", err)
		return core.SteerUnconfirmed
	}
	if queue.Active != nil && queue.Active.ID == id {
		return core.SteerRunning
	}
	for _, p := range queue.Queued {
		if p.ID == id {
			return core.SteerQueued
		}
	}
	// Absence from the queue alone cannot prove delivery. Durable history can
	// confirm that a turn consumed this exact prompt, including another client's
	// successful steer during the race.
	messages, err := s.agent.messages(s.ctx, s.id, 0)
	if err != nil {
		slog.Warn("kimi web: read steer history", "error", err)
		return core.SteerUnconfirmed
	}
	for _, m := range messages {
		if m.PromptID == id {
			return core.SteerProcessed
		}
	}
	return core.SteerUnconfirmed
}
