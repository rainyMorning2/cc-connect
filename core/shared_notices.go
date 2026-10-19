package core

import (
	"strings"
	"time"
)

func (e *Engine) showSharedNotice(state *interactiveState, notice *AgentNotice) {
	if notice == nil {
		return
	}
	state.mu.Lock()
	p, reply := state.platform, state.replyCtx
	state.mu.Unlock()
	title := e.i18n.T(MsgRuntimeNoticeTitle)
	body := notice.Message
	color := "orange"
	switch notice.Kind {
	case "subagent":
		if notice.Subagent == nil {
			return
		}
		title = e.i18n.T(MsgSubagentTitle)
		body = e.subagentNoticeText(*notice.Subagent)
		color = "indigo"

	case "usage_limit", "usage_warning":
		title = e.i18n.T(MsgRuntimeUsageLimit)
		if notice.Kind == "usage_warning" {
			title = e.i18n.T(MsgRuntimeUsageWarning)
		}
		var lines []string
		if notice.Code != "" {
			lines = append(lines, e.i18n.Tf(MsgRuntimeNoticeReason, notice.Code))
		}
		if notice.Usage != nil {
			for _, bucket := range notice.Usage.Buckets {
				lines = append(lines, bucket.Name)
				for _, window := range bucket.Windows {
					line := e.i18n.Tf(MsgRuntimeUsagePercent, window.UsedPercent)
					if window.ResetAtUnix > 0 {
						line += " · " + e.i18n.Tf(MsgRuntimeUsageReset, time.Unix(window.ResetAtUnix, 0).Local().Format("2006-01-02 15:04:05 MST"))
					}
					lines = append(lines, line)
				}
			}
		}
		lines = append(lines, e.i18n.T(MsgRuntimeUsageModelScope))
		body = strings.Join(lines, "\n")
	case "retry":
		body += "\n\n" + e.i18n.T(MsgRuntimeRetrying)
	case "model_rerouted":
		body = e.i18n.Tf(MsgRuntimeModelRerouted, notice.FromModel, notice.ToModel)
		if notice.Code != "" {
			body += "\n" + e.i18n.Tf(MsgRuntimeNoticeReason, notice.Code)
		}
	}
	if strings.TrimSpace(body) == "" {
		return
	}
	if supportsCards(p) {
		e.sendWithCard(p, reply, NewCard().Title(title, color).Markdown(body).Build())
	} else {
		e.send(p, reply, title+"\n"+body)
	}
}

func (e *Engine) subagentNoticeText(n SubagentNotice) string {
	status := e.subagentStatus(n.Status)
	name := n.Name
	if name == "" {
		name = n.ID
	}
	text := e.i18n.Tf(MsgSubagentState, name, n.ID, status)
	if n.TaskID != "" {
		text += "\n" + e.i18n.Tf(MsgSubagentTask, n.TaskID)
	}
	if n.Model != "" {
		text += "\n" + n.Model
	}
	if n.Description != "" {
		text += "\n" + n.Description
	}
	if n.Detail != "" {
		detail := []rune(n.Detail)
		if len(detail) > 4000 {
			detail = append(detail[:4000], '…')
		}
		text += "\n\n" + string(detail)
	}
	return text
}

func (e *Engine) subagentStatus(status string) string {
	statuses := map[string]MsgKey{"queued": MsgSubagentQueued, "running": MsgSubagentRunning, "suspended": MsgSubagentSuspended, "completed": MsgSubagentCompleted, "failed": MsgSubagentFailed, "cancelled": MsgSubagentCancelled}
	if key, ok := statuses[status]; ok {
		return e.i18n.T(key)
	}
	return status
}
