package core

import (
	"context"
	"strings"
	"time"
)

func (e *Engine) cmdFast(p Platform, msg *Message, args []string) {
	if len(args) > 1 || (len(args) == 1 && !strings.EqualFold(args[0], "on") && !strings.EqualFold(args[0], "off") && !strings.EqualFold(args[0], "status")) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastUsage))
		return
	}
	agent, _, key, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	var target any = agent
	usingDefaults := true
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state != nil {
		state.mu.Lock()
		if state.agentSession != nil {
			target = state.agentSession
			usingDefaults = false
		}
		state.mu.Unlock()
	}
	sw, ok := target.(ServiceTierSwitcher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFastNotSupported))
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if reader, ok := target.(AgentRuntimeSettingsReader); ok {
		if _, err := reader.ReadRuntimeSettings(ctx); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
	}
	if len(args) == 0 || !strings.EqualFold(args[0], "status") {
		enabled := !isFastTier(sw.GetServiceTier())
		if len(args) == 1 {
			enabled = strings.EqualFold(args[0], "on")
		}
		tier := "default"
		if enabled {
			tier = "fast"
		}
		if err := sw.SetServiceTier(ctx, tier); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
	}
	message := MsgFastOff
	if isFastTier(sw.GetServiceTier()) {
		message = MsgFastOn
	}
	text := e.i18n.T(message)
	if usingDefaults {
		if sw.GetServiceTier() == "" {
			text = e.i18n.T(MsgFastInherit)
		}
		text = e.i18n.Tf(MsgFastDefaults, text)
	}
	e.reply(p, msg.ReplyCtx, text)
}

func isFastTier(tier string) bool {
	return strings.EqualFold(strings.TrimSpace(tier), "fast") || strings.EqualFold(strings.TrimSpace(tier), "priority")
}

func replyFooterFast(session AgentSession, agent Agent) string {
	var reporter ServiceTierReporter
	if session != nil {
		reporter, _ = session.(ServiceTierReporter)
	}
	if reporter == nil && agent != nil {
		reporter, _ = agent.(ServiceTierReporter)
	}
	if reporter != nil && isFastTier(reporter.GetServiceTier()) {
		return "fast"
	}
	return ""
}
