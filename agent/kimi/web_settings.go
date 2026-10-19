package kimi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.ModelSwitcher = (*webAgent)(nil)
var _ core.ReasoningEffortSwitcher = (*webAgent)(nil)
var _ core.SessionIDValidator = (*webAgent)(nil)
var _ core.SessionModelSwitcher = (*webSession)(nil)
var _ core.SessionReasoningEffortSwitcher = (*webSession)(nil)

type webModel struct {
	Model   string   `json:"model"`
	Display string   `json:"display_name"`
	Efforts []string `json:"support_efforts"`
}

func (a *webAgent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *webAgent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model == model {
		return
	}
	a.model = model
	// Effort names vary by model. Retain an override only when the newly
	// selected model explicitly supports it; otherwise follow its defaults.
	for _, option := range a.models {
		if option.Model == model && slices.Contains(option.Efforts, a.effort) {
			return
		}
	}
	a.effort = ""
}

func (a *webAgent) AvailableModels(ctx context.Context) []core.ModelOption {
	models, err := a.loadModels(ctx)
	if err != nil {
		slog.Warn("kimi web: list configured models", "error", err)
		return nil
	}
	return models
}

func (a *webAgent) loadModels(ctx context.Context) ([]core.ModelOption, error) {
	var response struct {
		Items []webModel `json:"items"`
	}
	if err := a.api.request(ctx, "GET", "/api/v1/models", nil, &response); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.models = response.Items
	a.mu.Unlock()
	models := make([]core.ModelOption, 0, len(response.Items))
	for _, model := range response.Items {
		models = append(models, core.ModelOption{Name: model.Model, Desc: model.Display})
	}
	return models, nil
}

func (a *webAgent) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}

func (a *webAgent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	a.effort = effort
	a.mu.Unlock()
}

func (a *webAgent) AvailableReasoningEfforts() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := a.GetModel()
	if model == "" {
		var config struct {
			Model string `json:"default_model"`
		}
		if err := a.api.request(ctx, "GET", "/api/v1/config", nil, &config); err != nil {
			slog.Warn("kimi web: read default model for effort options", "error", err)
			return nil
		}
		model = config.Model
	}
	// Refresh from the service, so browser changes to model capabilities are
	// reflected in the next settings command rather than a hardcoded list.
	if _, err := a.loadModels(ctx); err != nil {
		slog.Warn("kimi web: read model catalog for effort options", "error", err)
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, option := range a.models {
		if option.Model == model {
			return append([]string(nil), option.Efforts...)
		}
	}
	return nil
}

// Only definitively stale stored bindings are discarded. Authentication and
// transport failures must retain the binding rather than silently lose context.
// Explicit /attach continues to validate through AttachSession without fallback.
func (a *webAgent) ValidateSessionID(ctx context.Context, id string) bool {
	if a.attachOnly {
		return true
	}
	if strings.TrimSpace(id) == "" {
		return false
	}
	var record webRecord
	if err := a.api.request(ctx, "GET", webSessionPath(id), nil, &record); err != nil {
		var apiErr *webAPIError
		if errors.As(err, &apiErr) && apiErr.Code == 40401 {
			return false
		}
		slog.Warn("kimi web: could not validate stored session; retaining binding", "session_id", id, "error", err)
		return true
	}
	return record.ID == id && filepath.Clean(record.Metadata.Cwd) == a.GetWorkDir()
}

func (s *webSession) CurrentModels(ctx context.Context) ([]core.ModelOption, error) {
	return s.agent.loadModels(ctx)
}

func (s *webSession) CurrentReasoningEfforts(ctx context.Context) ([]string, error) {
	settings, err := s.ReadRuntimeSettings(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.agent.loadModels(ctx); err != nil {
		return nil, err
	}
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	for _, model := range s.agent.models {
		if model.Model == settings.Model {
			return append([]string(nil), model.Efforts...), nil
		}
	}
	return nil, nil
}

func (s *webSession) SetCurrentModel(ctx context.Context, model string) error {
	return s.updateProfile(ctx, map[string]string{"model": model})
}

func (s *webSession) SetCurrentReasoningEffort(ctx context.Context, effort string) error {
	return s.updateProfile(ctx, map[string]string{"thinking": effort})
}

func (s *webSession) updateProfile(ctx context.Context, config map[string]string) error {
	// Verify scope before changing the server-owned session. Change only the
	// requested fields; preserve provider, permissions, tools and history.
	if _, err := s.ReadRuntimeSettings(ctx); err != nil {
		return err
	}
	var record webRecord
	if err := s.agent.api.request(ctx, "POST", webSessionPath(s.id)+"/profile", map[string]any{"agent_config": config}, &record); err != nil {
		return fmt.Errorf("kimi web: update current session profile: %w", err)
	}
	if record.ID != s.id || filepath.Clean(record.Metadata.Cwd) != s.workDir {
		return fmt.Errorf("kimi web: updated profile returned an out-of-scope session")
	}
	settings, err := s.ReadRuntimeSettings(ctx)
	if err != nil {
		return fmt.Errorf("kimi web: profile update accepted but verification failed: %w", err)
	}
	for key, requested := range config {
		var actual string
		switch key {
		case "model":
			actual = settings.Model
		case "thinking":
			actual = settings.ReasoningEffort
		case "permission_mode":
			actual = settings.PermissionMode
		default:
			continue
		}
		if actual != requested {
			return fmt.Errorf("kimi web: %s update not confirmed: server reports %q instead of %q", key, actual, requested)
		}
	}
	return nil
}
