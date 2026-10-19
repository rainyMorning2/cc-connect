package core

import (
	"context"
	"log/slog"
	"strings"
)

// Reader-owned index. Each worker serializes one card's create/update calls;
// slow platform edits cannot block main-turn output or reorder that card.
type sharedSubagentCards struct {
	items map[string]*sharedSubagentCard
	order []string
}
type sharedSubagentCard struct {
	updates chan SubagentNotice
	cancel  context.CancelFunc
}

func (e *Engine) showSharedSubagentCard(ctx context.Context, state *interactiveState, cards *sharedSubagentCards, notice *AgentNotice) {
	if notice == nil || notice.Subagent == nil || notice.Subagent.ID == "" {
		return
	}
	state.mu.Lock()
	p, reply := state.platform, state.replyCtx
	state.mu.Unlock()
	updater, ok := p.(CardMessageUpdater)
	if !ok {
		e.showSharedNotice(state, notice)
		return
	}
	n := *notice.Subagent
	if cards.items == nil {
		cards.items = map[string]*sharedSubagentCard{}
	}
	item := cards.items[n.ID]
	if item == nil {
		workerCtx, cancel := context.WithCancel(ctx)
		item = &sharedSubagentCard{updates: make(chan SubagentNotice, 1), cancel: cancel}
		cards.items[n.ID] = item
		cards.order = append(cards.order, n.ID)
		if len(cards.order) > 256 {
			oldest := cards.order[0]
			cards.order = cards.order[1:]
			cards.items[oldest].cancel()
			delete(cards.items, oldest)
		}
		go e.runSharedSubagentCard(workerCtx, p, reply, updater, item.updates)
	}
	// Coalesce rapid lifecycle changes to the latest state while an edit is in flight.
	select {
	case <-item.updates:
	default:
	}
	select {
	case item.updates <- n:
	case <-ctx.Done():
	}
}

func (e *Engine) runSharedSubagentCard(ctx context.Context, p Platform, reply any, updater CardMessageUpdater, updates <-chan SubagentNotice) {
	var handle any
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-updates:
			if ctx.Err() != nil {
				return
			}
			card := e.buildSubagentCard(n)
			if handle == nil {
				sent, err := updater.SendCardWithHandle(ctx, reply, card)
				if err == nil {
					handle = sent
					continue
				}
				if err != nil {
					slog.Warn("subagent: create status card", "agent_id", n.ID, "error", err)
				}
			} else {
				if err := updater.UpdateCard(ctx, handle, card); err == nil {
					continue
				} else {
					slog.Warn("subagent: update status card", "agent_id", n.ID, "error", err)
				}
			}
			if ctx.Err() == nil {
				var err error
				if sender, ok := p.(CardSender); ok {
					err = sender.SendCard(ctx, reply, card)
				} else {
					err = p.Send(ctx, reply, card.RenderText())
				}
				if err != nil {
					slog.Warn("subagent: send fallback status", "agent_id", n.ID, "error", err)
				}
			}
		}
	}
}

func (e *Engine) buildSubagentCard(n SubagentNotice) *Card {
	label := e.subagentPanelTitle(n)
	color := "indigo"
	switch n.Status {
	case "completed":
		color = "green"
	case "failed":
		color = "red"
	case "suspended":
		color = "orange"
	case "cancelled":
		color = "grey"
	}
	card := NewCard().Title(e.i18n.T(MsgSubagentTitle)+" · "+label, color).Markdown(e.subagentNoticeText(n)).Build()
	card.RichMarkdown = true
	card.PreviewText = subagentPreview(n)
	if n.Status != "failed" && n.Status != "suspended" {
		card.CollapsedDetailsTitle = label
	}
	return card
}

// Keep the visible preview short; the complete task/result remains in details.
func subagentPreview(n SubagentNotice) string {
	text := n.Description
	if n.Status != "running" && n.Status != "queued" && strings.TrimSpace(n.Detail) != "" {
		text = n.Detail
	}
	if strings.TrimSpace(text) == "" {
		text = n.Detail
	}
	return subagentFirstLine(text, 48)
}

func subagentFirstLine(text string, limit int) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		chars := []rune(line)
		if len(chars) > limit {
			return string(chars[:limit-1]) + "…"
		}
		return line
	}
	return ""
}

func (e *Engine) subagentPanelTitle(n SubagentNotice) string {
	name := subagentFirstLine(n.Name, 12)
	id := subagentFirstLine(n.ID, 12)
	if name == "" {
		name = id
	} else if id != "" && strings.TrimSpace(n.Name) != strings.TrimSpace(n.ID) {
		name = e.i18n.Tf(MsgSubagentPanelIdentity, name, id)
	}
	if name == "" {
		name = e.i18n.T(MsgSubagentTitle)
	}
	status := subagentStatusEmoji(n.Status)
	task := subagentFirstLine(n.Description, 16)
	if task == "" {
		return e.i18n.Tf(MsgSubagentPanelState, status, name)
	}
	return e.i18n.Tf(MsgSubagentPanelTask, status, name, task)
}

func subagentStatusEmoji(status string) string {
	switch status {
	case "queued":
		return "⏳"
	case "running":
		return "🔄"
	case "suspended":
		return "⏸️"
	case "completed":
		return "✅"
	case "failed":
		return "❌"
	case "cancelled":
		return "🚫"
	default:
		return "❔"
	}
}
