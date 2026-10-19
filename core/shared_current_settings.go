package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) currentSettingsSession(sessionKey, command string) AgentSession {
	if command != "model" && command != "reasoning" {
		return nil
	}
	key := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	as := state.agentSession
	state.mu.Unlock()
	if _, ok := as.(AgentRuntimeSettingsReader); !ok {
		return nil
	}
	if command == "model" {
		if _, ok := as.(SessionModelSwitcher); ok {
			return as
		}
	} else if _, ok := as.(SessionReasoningEffortSwitcher); ok {
		return as
	}
	return nil
}

// Shared by text commands, menu navigation and in-place selector callbacks.
// A runtime lacking these capabilities keeps the creation-defaults workflow.
func (e *Engine) currentSessionSettingsCard(sessionKey, command string, args []string) *Card {
	if command == "mode" {
		agent, _ := e.sessionContextForKey(sessionKey)
		// Agents without creation-time permission controls expose current settings.
		if _, defaults := agent.(ModeSwitcher); !defaults {
			if len(args) > 0 && strings.EqualFold(args[0], "current") {
				args = args[1:]
			}
			return e.currentPermissionModeCard(e.interactiveKeyForSessionKey(sessionKey), args)
		}
	}
	as := e.currentSettingsSession(sessionKey, command)
	if as == nil {
		return nil
	}
	title := MsgCardTitleModel
	if command == "reasoning" {
		title = MsgCardTitleReasoning
	}
	failure := func(err error) *Card {
		return e.simpleCard(e.i18n.T(title), "red", e.i18n.Tf(MsgError, err))
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	names, err := sessionSettingChoices(ctx, as, command)
	if err != nil {
		return failure(err)
	}
	if len(args) > 0 {
		if err := e.applyCurrentSessionSetting(ctx, as, command, args, names); err != nil {
			return failure(err)
		}
	}
	settings, err := as.(AgentRuntimeSettingsReader).ReadRuntimeSettings(ctx)
	if err != nil {
		return failure(err)
	}
	card := NewCard().Title(e.i18n.T(title), "indigo")
	if len(args) > 0 {
		card.Markdown(e.i18n.T(MsgSessionSettingsSaved))
	}
	value := func(v string) string {
		if v == "" {
			return e.i18n.T(MsgDefaultSettingsUnknown)
		}
		return v
	}
	card.Markdown(e.i18n.Tf(MsgSessionSettingsRuntime, value(settings.Model), value(settings.ReasoningEffort)))
	current := settings.Model
	if command == "reasoning" {
		current = settings.ReasoningEffort
	}
	var options []CardSelectOption
	selected := ""
	for i, name := range names {
		action := fmt.Sprintf("act:/%s %s", command, name)
		if command == "model" {
			action = "act:/model switch " + name
		}
		options = append(options, CardSelectOption{Text: fmt.Sprintf("%d. %s", i+1, name), Value: action})
		if name == current {
			selected = action
		}
	}
	if len(options) > 0 {
		card.Select(e.i18n.T(MsgDefaultSettingsSelect), options, selected)
	}
	return card.Note(e.i18n.T(MsgSessionSettingsScope)).Buttons(e.cardBackButton()).Build()
}

func sessionSettingChoices(ctx context.Context, as AgentSession, command string) ([]string, error) {
	if command == "reasoning" {
		return as.(SessionReasoningEffortSwitcher).CurrentReasoningEfforts(ctx)
	}
	models, err := as.(SessionModelSwitcher).CurrentModels(ctx)
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Name)
	}
	return names, err
}

func (e *Engine) applyCurrentSessionSetting(ctx context.Context, as AgentSession, command string, args, choices []string) error {
	target := strings.Join(args, " ")
	if command == "model" {
		var valid bool
		target, valid = parseModelSwitchArgs(args)
		if !valid {
			return fmt.Errorf("%s", e.i18n.T(MsgModelUsage))
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(target)); err == nil && n > 0 && n <= len(choices) {
		target = choices[n-1]
	}
	for _, choice := range choices {
		if strings.EqualFold(target, choice) {
			if command == "model" {
				return as.(SessionModelSwitcher).SetCurrentModel(ctx, choice)
			}
			return as.(SessionReasoningEffortSwitcher).SetCurrentReasoningEffort(ctx, choice)
		}
	}
	if command == "model" {
		return fmt.Errorf("%s", e.i18n.T(MsgModelUsage))
	}
	return fmt.Errorf("%s", e.reasoningUsage(choices))
}
