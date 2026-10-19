package kimi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.AgentBackgroundTerminals = (*webSession)(nil)

type webTask struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Command         string `json:"command"`
	Description     string `json:"description"`
	Background      bool   `json:"run_in_background"`
	AgentID         string `json:"agent_id"`
	AgentType       string `json:"subagent_type"`
	Phase           string `json:"subagent_phase"`
	SuspendedReason string `json:"suspended_reason"`
	Model           string `json:"model"`
}

// Match Codex's background-shell semantics. Web PTYs and subagents are separate.
func (s *webSession) ListBackgroundTerminals(ctx context.Context) ([]core.BackgroundTerminal, error) {
	if _, err := s.ReadRuntimeSettings(ctx); err != nil {
		return nil, err
	}
	var response struct {
		Items []webTask `json:"items"`
	}
	if err := s.agent.api.request(ctx, "GET", webSessionPath(s.id)+"/tasks?status=running", nil, &response); err != nil {
		return nil, fmt.Errorf("kimi web: list background shells: %w", err)
	}
	var result []core.BackgroundTerminal
	for _, task := range response.Items {
		if task.SessionID != s.id {
			return nil, fmt.Errorf("kimi web: task is outside attached session")
		}
		if task.Kind != "bash" || !task.Background || task.Status != "running" {
			continue
		}
		if !validWebTaskID(task.ID) {
			return nil, fmt.Errorf("kimi web: invalid background task ID")
		}
		command := task.Command
		if command == "" {
			command = task.Description
		}
		result = append(result, core.BackgroundTerminal{ID: task.ID, Command: command, Cwd: s.workDir})
	}
	return result, nil
}

func validWebTaskID(id string) bool {
	return strings.TrimSpace(id) != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#:")
}

func (s *webSession) TerminateBackgroundTerminal(ctx context.Context, id string) error {
	if !validWebTaskID(id) {
		return fmt.Errorf("kimi web: invalid background task ID")
	}
	if _, err := s.ReadRuntimeSettings(ctx); err != nil {
		return err
	}
	path := webSessionPath(s.id) + "/tasks/" + url.PathEscape(id)
	var task webTask
	if err := s.agent.api.request(ctx, "GET", path, nil, &task); err != nil {
		return fmt.Errorf("kimi web: read background shell: %w", err)
	}
	if task.ID != id || task.SessionID != s.id || task.Kind != "bash" || !task.Background {
		return fmt.Errorf("kimi web: task is not a background shell of the attached session")
	}
	switch task.Status {
	case "completed", "failed", "cancelled":
		return nil // Finished between listing and stopping.
	case "running":
	default:
		return fmt.Errorf("kimi web: unknown background task status %q", task.Status)
	}
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := s.agent.api.request(ctx, "POST", path+":cancel", nil, &result); err != nil {
		var apiErr *webAPIError
		if errors.As(err, &apiErr) && apiErr.Code == 40904 {
			return nil
		} // Already finished; never cancel a replacement.
		return fmt.Errorf("kimi web: stop background shell: %w", err)
	}
	if !result.Cancelled {
		return fmt.Errorf("kimi web: background shell cancellation was not confirmed")
	}
	return nil
}
