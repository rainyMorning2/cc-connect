package core

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

func (e *Engine) sendSharedQuestionPrompt(p Platform, reply any, pending *pendingPermission) {
	index := pending.CurrentQuestion
	if index < 0 || index >= len(pending.Questions) {
		return
	}
	q := pending.Questions[index]
	text := fmt.Sprintf("(%d/%d) %s", index+1, len(pending.Questions), q.Question)
	buttons := []ButtonOption{}
	cardButtons := []CardButton{}
	for i, opt := range q.Options {
		label := opt.Label
		if q.MultiSelect && sharedAnswerSelected(pending.MultiAnswers[index], opt.Label) {
			label = "☑ " + label
		}
		text += fmt.Sprintf("\n%d. %s", i+1, label)
		if opt.Description != "" {
			text += " — " + opt.Description
		}
		action := fmt.Sprintf("cmd:/answer %s %d %d", pending.ActionToken, index, i+1)
		buttons = append(buttons, ButtonOption{Text: label, Data: action})
		cardButtons = append(cardButtons, CardButton{Text: label, Value: action, Type: "default"})
	}
	if q.MultiSelect {
		text += "\n" + e.i18n.T(MsgSharedMultiAnswerHint)
		if selected := pending.MultiAnswers[index]; len(selected) > 0 {
			text += "\n" + e.i18n.Tf(MsgSharedSelectedAnswers, strings.Join(selected, ", "))
		}
		action := fmt.Sprintf("cmd:/answer %s %d done", pending.ActionToken, index)
		buttons = append(buttons, ButtonOption{Text: e.i18n.T(MsgSharedSubmitAnswers), Data: action})
		cardButtons = append(cardButtons, CardButton{Text: e.i18n.T(MsgSharedSubmitAnswers), Value: action, Type: "primary"})
	} else {
		text += "\n" + e.i18n.T(MsgSharedAnswerHint)
	}
	skip := ButtonOption{Text: e.i18n.T(MsgSharedSkip), Data: "cmd:/skip " + pending.ActionToken}
	buttons = append(buttons, skip)
	cardButtons = append(cardButtons, CardButton{Text: skip.Text, Value: skip.Data, Type: "default"})
	if bs, ok := p.(InlineButtonSender); ok {
		rows := [][]ButtonOption{}
		for _, button := range buttons {
			rows = append(rows, []ButtonOption{button})
		}
		if err := bs.SendWithButtons(e.ctx, reply, text, rows); err == nil {
			return
		} else {
			slog.Warn("shared question buttons", "project", e.name, "platform", p.Name(), "request_id", pending.RequestID, "error", err)
		}
	}
	if supportsCards(p) {
		card := NewCard().Title(e.i18n.T(MsgAskQuestionTitle), "blue").Markdown(text)
		for _, button := range cardButtons {
			card.Buttons(button)
		}
		e.sendWithCard(p, reply, card.Build())
		return
	}
	e.send(p, reply, text)
}

func (e *Engine) answerSharedQuestion(p Platform, msg *Message, state *interactiveState, pending *pendingPermission, token string, index int, content string) {
	state.mu.Lock()
	if state.pending != pending || (token != "" && token != pending.ActionToken) || (index >= 0 && index != pending.CurrentQuestion) {
		state.mu.Unlock()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
		return
	}
	current := pending.CurrentQuestion
	if current < 0 || current >= len(pending.Questions) {
		state.mu.Unlock()
		return
	}
	q := pending.Questions[current]
	if q.MultiSelect {
		ready, err := e.collectSharedMultiAnswer(pending, current, q, content)
		if err != nil {
			state.mu.Unlock()
			e.reply(p, msg.ReplyCtx, err.Error())
			return
		}
		if !ready {
			state.mu.Unlock()
			e.sendSharedPrompt(state, pending)
			return
		}
	} else {
		answer, ok := sharedQuestionAnswer(q, content)
		if !ok {
			state.mu.Unlock()
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedAnswerHint))
			return
		}
		if pending.Answers == nil {
			pending.Answers = map[int]string{}
		}
		pending.Answers[current] = answer
	}
	if current+1 < len(pending.Questions) {
		pending.CurrentQuestion++
		state.mu.Unlock()
		e.sendSharedPrompt(state, pending)
		return
	}
	answers := map[string]any{}
	for i, q := range pending.Questions {
		if q.MultiSelect {
			answers[q.ID] = append([]string(nil), pending.MultiAnswers[i]...)
		} else {
			answers[q.ID] = []string{pending.Answers[i]}
		}
	}
	as := state.agentSession
	state.mu.Unlock()
	if as == nil {
		return
	}
	if err := as.RespondPermission(pending.RequestID, PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": answers}}); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.resolveSharedPending(state, pending.RequestID)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedResponseSent))
}

func cloneSharedMultiAnswers(answers map[int][]string) map[int][]string {
	out := make(map[int][]string, len(answers))
	for i, values := range answers {
		out[i] = append([]string(nil), values...)
	}
	return out
}

func sharedAnswerSelected(answers []string, value string) bool {
	for _, answer := range answers {
		if answer == value {
			return true
		}
	}
	return false
}

func sharedQuestionAnswer(q UserQuestion, input string) (string, bool) {
	answer := strings.TrimSpace(input)
	if answer == "" {
		return "", false
	}
	if n, err := strconv.Atoi(answer); err == nil && n > 0 && n <= len(q.Options) {
		return q.Options[n-1].Label, true
	}
	if q.IsOther || len(q.Options) == 0 {
		return answer, true
	}
	for _, opt := range q.Options {
		if opt.Label == answer {
			return answer, true
		}
	}
	return "", false
}

// Called with state.mu held. Selection is reversible until the user submits.
func (e *Engine) collectSharedMultiAnswer(pending *pendingPermission, index int, q UserQuestion, input string) (bool, error) {
	input = strings.TrimSpace(input)
	selected := pending.MultiAnswers[index]
	if strings.EqualFold(input, "done") || input == e.i18n.T(MsgSharedSubmitAnswers) {
		if len(selected) == 0 {
			return false, fmt.Errorf("%s", e.i18n.T(MsgSharedMultiAnswerHint))
		}
		return true, nil
	}
	inputs := []string{input}
	// Numeric batches support text-only platforms. Other text stays intact.
	fields := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == '，' || r == ' ' })
	numeric := len(fields) > 1
	for _, field := range fields {
		if _, err := strconv.Atoi(field); err != nil {
			numeric = false
		}
	}
	if numeric {
		inputs = fields
	}
	var values []string
	for _, part := range inputs {
		value, ok := sharedQuestionAnswer(q, part)
		if !ok {
			return false, fmt.Errorf("%s", e.i18n.T(MsgSharedMultiAnswerHint))
		}
		if !sharedAnswerSelected(values, value) {
			values = append(values, value)
		}
	}
	selected = append([]string(nil), selected...)
	for _, value := range values {
		if sharedAnswerSelected(selected, value) {
			for i, old := range selected {
				if old == value {
					selected = append(selected[:i], selected[i+1:]...)
					break
				}
			}
		} else {
			selected = append(selected, value)
		}
	}
	if pending.MultiAnswers == nil {
		pending.MultiAnswers = map[int][]string{}
	}
	pending.MultiAnswers[index] = selected
	return false, nil
}
