package codex

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionModelSwitcher = (*managedSession)(nil)
var _ core.SessionReasoningEffortSwitcher = (*managedSession)(nil)

type managedModel struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	Description string `json:"description"`
	Hidden      bool   `json:"hidden"`
	Efforts     []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

func (s *managedSession) currentModelCatalog(ctx context.Context) ([]managedModel, error) {
	rpc, err := s.connection()
	if err != nil {
		return nil, err
	}
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
	return options, nil
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
	for _, model := range catalog {
		if model.Model != settings.Model && model.ID != settings.Model {
			continue
		}
		var efforts []string
		for _, option := range model.Efforts {
			if option.Effort != "" {
				efforts = append(efforts, option.Effort)
			}
		}
		return efforts, nil
	}
	return nil, nil
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
