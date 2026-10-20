package codex

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionModelSwitcher = (*managedSession)(nil)
var _ core.SessionReasoningEffortSwitcher = (*managedSession)(nil)
var _ core.AgentDefaultSettingsReader = (*managedAgent)(nil)

type managedModel struct {
	ID            string `json:"id"`
	Model         string `json:"model"`
	Description   string `json:"description"`
	Hidden        bool   `json:"hidden"`
	IsDefault     bool   `json:"isDefault"`
	DefaultEffort string `json:"defaultReasoningEffort"`
	Efforts       []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

func (a *managedAgent) ReadDefaultSettings(ctx context.Context) (core.AgentRuntimeSettings, error) {
	rpc, err := a.openRPC(ctx)
	if err != nil {
		return core.AgentRuntimeSettings{}, err
	}
	defer rpc.close()
	cwd, err := filepath.Abs(a.GetWorkDir())
	if err != nil {
		return core.AgentRuntimeSettings{}, fmt.Errorf("codex managed: resolve defaults directory: %w", err)
	}
	var response struct {
		Config struct {
			Model  string `json:"model"`
			Effort string `json:"model_reasoning_effort"`
		} `json:"config"`
	}
	if err := rpc.request(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": cwd}, &response); err != nil {
		return core.AgentRuntimeSettings{}, fmt.Errorf("codex managed: read server defaults: %w", err)
	}
	settings := core.AgentRuntimeSettings{Model: response.Config.Model, ReasoningEffort: response.Config.Effort}
	if settings.Model != "" && settings.ReasoningEffort != "" {
		return settings, nil
	}
	models, err := readManagedModelCatalog(ctx, rpc)
	if err != nil {
		return settings, fmt.Errorf("codex managed: resolve model defaults: %w", err)
	}
	if settings.Model == "" {
		for _, model := range models {
			if model.IsDefault {
				settings.Model = model.Model
				if settings.Model == "" {
					settings.Model = model.ID
				}
				break
			}
		}
	}
	if settings.ReasoningEffort == "" {
		for _, model := range models {
			if model.Model == settings.Model || model.ID == settings.Model {
				settings.ReasoningEffort = model.DefaultEffort
				break
			}
		}
	}
	return settings, nil
}

func (s *managedSession) currentModelCatalog(ctx context.Context) ([]managedModel, error) {
	rpc, err := s.connection()
	if err != nil {
		return nil, err
	}
	return readManagedModelCatalog(ctx, rpc)
}

func readManagedModelCatalog(ctx context.Context, rpc *daemonRPC) ([]managedModel, error) {
	var models []managedModel
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{"limit": 100, "includeHidden": true}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data       []managedModel `json:"data"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := rpc.request(ctx, "model/list", params, &page); err != nil {
			return nil, fmt.Errorf("codex managed list current models: %w", err)
		}
		models = append(models, page.Data...)
		if page.NextCursor == "" {
			return models, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("codex managed model pagination did not advance")
		}
		cursor = page.NextCursor
		seen[cursor] = true
	}
}

func (s *managedSession) CurrentModels(ctx context.Context) ([]core.ModelOption, error) {
	catalog, err := s.currentModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return managedModelOptions(catalog), nil
}

func managedModelOptions(catalog []managedModel) []core.ModelOption {
	var options []core.ModelOption
	for _, model := range catalog {
		if model.Hidden {
			continue
		}
		name := model.Model
		if name == "" {
			name = model.ID
		}
		if name != "" {
			options = append(options, core.ModelOption{Name: name, Desc: model.Description})
		}
	}
	return options
}

func (s *managedSession) CurrentReasoningEfforts(ctx context.Context) ([]string, error) {
	settings, err := s.ReadRuntimeSettings(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := s.currentModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return managedModelEfforts(catalog, settings.Model), nil
}

func managedModelEfforts(catalog []managedModel, name string) []string {
	for _, model := range catalog {
		if model.Model != name && model.ID != name {
			continue
		}
		var efforts []string
		for _, option := range model.Efforts {
			if option.Effort != "" {
				efforts = append(efforts, option.Effort)
			}
		}
		return efforts
	}
	return nil
}

func (s *managedSession) SetCurrentModel(ctx context.Context, model string) error {
	return s.updateCurrentModelSetting(ctx, "model", model)
}

func (s *managedSession) SetCurrentReasoningEffort(ctx context.Context, effort string) error {
	return s.updateCurrentModelSetting(ctx, "effort", effort)
}

func (s *managedSession) updateCurrentModelSetting(ctx context.Context, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("codex managed current %s must not be empty", key)
	}
	if err := s.updateCurrentThreadParams(ctx, map[string]any{key: value}); err != nil {
		return err
	}
	settings, err := s.ReadRuntimeSettings(ctx)
	if err != nil {
		return fmt.Errorf("codex managed %s update accepted but verification failed; query current settings again: %w", key, err)
	}
	actual := settings.Model
	if key == "effort" {
		actual = settings.ReasoningEffort
	}
	if actual != value {
		return fmt.Errorf("codex managed %s update was not confirmed: server reports %q instead of %q", key, actual, value)
	}
	return nil
}

// Creation defaults must use the same daemon catalog as attached sessions.
func (a *managedAgent) AvailableModels(ctx context.Context) []core.ModelOption {
	rpc, err := a.openRPC(ctx)
	if err != nil {
		slog.Warn("codex managed: connect for model catalog", "error", err)
		return nil
	}
	defer rpc.close()
	catalog, err := readManagedModelCatalog(ctx, rpc)
	if err != nil {
		slog.Warn("codex managed: read model catalog", "error", err)
		return nil
	}
	return managedModelOptions(catalog)
}

func (a *managedAgent) AvailableReasoningEfforts() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rpc, err := a.openRPC(ctx)
	if err != nil {
		slog.Warn("codex managed: connect for effort options", "error", err)
		return nil
	}
	defer rpc.close()
	model := a.GetModel()
	if model == "" {
		var response struct {
			Config struct {
				Model string `json:"model"`
			} `json:"config"`
		}
		cwd, err := filepath.Abs(a.GetWorkDir())
		if err != nil {
			slog.Warn("codex managed: resolve config directory", "error", err)
			return nil
		}
		if err := rpc.request(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": cwd}, &response); err != nil {
			slog.Warn("codex managed: read default model", "error", err)
			return nil
		}
		model = response.Config.Model
	}
	catalog, err := readManagedModelCatalog(ctx, rpc)
	if err != nil {
		slog.Warn("codex managed: read effort options", "error", err)
		return nil
	}
	if model == "" {
		for _, option := range catalog {
			if option.IsDefault {
				model = option.Model
				if model == "" {
					model = option.ID
				}
				break
			}
		}
	}
	return managedModelEfforts(catalog, model)
}

func (a *managedAgent) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Server-advertised names are opaque, including spaces and future values.
	a.reasoningEffort = strings.TrimSpace(effort)
}

func (a *managedAgent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model != model {
		a.reasoningEffort = ""
	}
	a.model = model
}
