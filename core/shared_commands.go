package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Match new commands exactly, so legacy command-prefix aliases remain stable.
func isSharedCommand(command string) bool {
	switch command {
	case "async-answer", "attach", "detach", "steer", "decision", "answer", "skip", "terminals":
		return true
	}
	return false
}

func (e *Engine) handleSharedCommand(p Platform, msg *Message, command string, args []string) bool {
	if !isSharedCommand(command) {
		return false
	}
	agent, sessions, key, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if command == "attach" {
		if len(args) != 1 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedAttachUsage))
			return true
		}
		candidates, err := agent.ListSessions(e.ctx)
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return true
		}
		matched := e.matchSession(candidates, sessions, args[0])
		if matched == nil {
			matched = &AgentSessionInfo{ID: args[0], Summary: args[0]}
		}
		e.attachSharedSession(p, msg, agent, sessions, key, *matched)
		return true
	}
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	var as SharedAgentSession
	if state != nil {
		state.mu.Lock()
		as, _ = state.agentSession.(SharedAgentSession)
		state.mu.Unlock()
	}
	if (command == "decision" || command == "answer" || command == "skip") && len(args) > 0 {
		if pendingState, pendingSession := e.sharedRequestForSource(msg.SessionKey, args[0]); pendingState != nil {
			state, as = pendingState, pendingSession
		}
	}
	if as == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedNotAttached))
		return true
	}
	switch command {
	case "async-answer":
		e.answerSharedAsyncQuestion(p, msg, state, as, args)
	case "detach":
		e.cleanupInteractiveState(key, state)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedDetached))
	case "steer":
		if len(msg.Images) > 0 || len(msg.Files) > 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedSteerTextOnly))
			return true
		}
		text := strings.TrimSpace(strings.Join(args, " "))
		if text == "" {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedSteerUsage))
			return true
		}
		e.replySharedSteer(p, msg, as, text)

	case "decision", "answer", "skip":
		e.sharedRequestCommand(p, msg, state, as, command, args)
	case "terminals":
		e.sharedTerminalsCommand(p, msg, as, args)
	}
	return true
}

func (e *Engine) attachSharedSession(p Platform, msg *Message, agent Agent, sessions *SessionManager, key string, info AgentSessionInfo) {
	attacher, ok := agent.(AgentSessionAttacher)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedUnsupported))
		return
	}
	as, err := attacher.AttachSession(e.ctx, info.ID)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	shared, ok := as.(SharedAgentSession)
	if !ok {
		_ = as.Close()
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedUnsupported))
		return
	}
	// Validate the new connection first. A failed attachment preserves the
	// currently selected conversation and its live observer.
	e.cleanupInteractiveState(key)
	session := sessions.SwitchToAgentSession(msg.SessionKey, info.ID, agent.Name(), info.Summary)
	state := &interactiveState{agentSession: as, busySession: session, agent: agent, platform: p, replyCtx: msg.ReplyCtx, shared: sharedSessionState{sessionKey: msg.SessionKey}, eventsNeedResync: false}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	e.startSharedReader(state, shared, session, sessions, key)
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSharedAttached, info.ID))
	e.hooks.Emit(HookEvent{Event: HookEventSessionStarted, SessionKey: msg.SessionKey, Platform: p.Name()})
}

func (e *Engine) sharedRequestCommand(p Platform, msg *Message, state *interactiveState, as SharedAgentSession, command string, args []string) {
	state.mu.Lock()
	pending := state.pending
	state.mu.Unlock()
	if pending == nil || (len(args) > 0 && args[0] != pending.ActionToken) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
		return
	}
	switch command {
	case "decision":
		if len(args) != 2 || len(pending.Questions) > 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
			return
		}
		e.respondSharedDecision(p, msg, state, pending, args[1])
	case "answer":
		if len(args) != 3 || len(pending.Questions) == 0 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
			return
		}
		index, err := strconv.Atoi(args[1])
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
			return
		}
		e.answerSharedQuestion(p, msg, state, pending, args[0], index, args[2])
	case "skip":
		if len(pending.Questions) == 0 || len(args) > 1 {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedStaleRequest))
			return
		}
		if err := as.RespondPermission(pending.RequestID, PermissionResult{Behavior: "skip"}); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
		e.resolveSharedPending(state, pending.RequestID)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedResponseSent))
	}
}

func (e *Engine) sharedTerminalsCommand(p Platform, msg *Message, as SharedAgentSession, args []string) {
	terminals, ok := as.(AgentBackgroundTerminals)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedUnsupported))
		return
	}
	if len(args) == 2 && args[0] == "stop" {
		if strings.EqualFold(args[1], "all") {
			e.stopAllSharedTerminals(p, msg, terminals)
			return
		}
		if err := terminals.TerminateBackgroundTerminal(e.ctx, args[1]); err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		} else {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedTerminalStopped))
		}
		return
	}
	if len(args) > 0 && (len(args) != 1 || args[0] != "list") {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedTerminalsUsage))
		return
	}
	list, err := terminals.ListBackgroundTerminals(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	if len(list) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedNoTerminals))
		return
	}
	var text strings.Builder
	for _, terminal := range list {
		fmt.Fprintf(&text, "%s: %s\n", terminal.ID, terminal.Command)
	}
	text.WriteString(e.i18n.T(MsgSharedTerminalsUsage))
	e.reply(p, msg.ReplyCtx, text.String())
}

func (e *Engine) stopAllSharedTerminals(p Platform, msg *Message, terminals AgentBackgroundTerminals) {
	// Snapshot only this session's terminals. Processes started later are not
	// part of this operation, and the active turn remains untouched.
	list, err := terminals.ListBackgroundTerminals(e.ctx)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	if len(list) == 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgSharedNoTerminals))
		return
	}
	stopped := 0
	var failures []string
	seen := map[string]bool{}
	for _, terminal := range list {
		if seen[terminal.ID] {
			continue
		}
		seen[terminal.ID] = true
		if err := terminals.TerminateBackgroundTerminal(e.ctx, terminal.ID); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", terminal.ID, err))
		} else {
			stopped++
		}
	}
	if len(failures) > 0 {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSharedTerminalsStopPartial, stopped, len(failures))+"\n"+strings.Join(failures, "\n"))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgSharedTerminalsStopped, stopped))
}

// Read only the interrupted thread's current terminals. A failed lookup must
// not imply that all background work has stopped.
func (e *Engine) sharedInterruptedMessage(as AgentSession) string {
	terminals, ok := as.(AgentBackgroundTerminals)
	if !ok {
		return e.i18n.T(MsgSharedInterrupted)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	list, err := terminals.ListBackgroundTerminals(ctx)
	if err != nil {
		slog.Warn("read background terminals after interruption", "error", err)
		return e.i18n.T(MsgSharedInterrupted)
	}
	ids := make(map[string]struct{}, len(list))
	for _, terminal := range list {
		ids[terminal.ID] = struct{}{}
	}
	return e.i18n.Tf(MsgSharedInterruptedCount, len(ids))
}

// In-place card callbacks carry only the session key. Reconstruct their reply
// target and use the text command's attach/validation path before redrawing.
func (e *Engine) sharedSwitchCardAction(args, sessionKey string) *Card {
	target, err := e.resolveSendTarget(sessionKey, false)
	if err != nil || target.platform == nil {
		if err == nil {
			err = fmt.Errorf("session has no reply platform")
		}
		slog.Warn("shared session card switch failed", "project", e.name, "session_key", sessionKey, "error", err)
		return NewCard().Markdown(e.i18n.Tf(MsgError, err)).Build()
	}
	msg := &Message{SessionKey: sessionKey, Platform: target.platform.Name(), UserID: extractUserID(sessionKey), ReplyCtx: target.replyCtx}
	e.cmdSwitch(target.platform, msg, strings.Fields(args))
	return e.renderListCardSafe(sessionKey, 1)
}

// All stop entrypoints invalidate retained input before interrupting: the runtime
// may become idle (and wake a queue drainer) before the interrupt RPC returns.
func (e *Engine) stopSharedTurn(state *interactiveState, as SharedAgentSession, notifyQueued bool) error {
	state.mu.Lock()
	state.shared.stopGeneration++
	if state.shared.foreground != nil {
		state.shared.foreground.cancel()
	}
	state.mu.Unlock()
	canceller, ok := as.(AgentSessionCanceller)
	if !ok {
		return fmt.Errorf("%s", e.i18n.T(MsgSharedUnsupported))
	}
	if notifyQueued {
		e.notifyDroppedQueuedMessages(state, fmt.Errorf("session stopped"))
	} else {
		state.mu.Lock()
		state.pendingMessages = nil
		state.mu.Unlock()
	}
	return canceller.CancelTurn()
}

func (e *Engine) replySharedSteer(p Platform, msg *Message, as SharedAgentSession, text string) {
	key, err := steerSharedSession(as, as.RuntimeState().TurnID, text)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(key))
}

func steerSharedSession(as SharedAgentSession, expectedTurnID, text string) (MsgKey, error) {
	key := MsgSharedSteerAccepted
	var err error
	if steerer, ok := as.(AgentBestEffortSteerer); ok {
		var result SteerOutcome
		result, err = steerer.SteerCurrent(text)
		switch result {
		case SteerInserted:
		case SteerQueued:
			key = MsgSharedSteerQueued
		case SteerRunning:
			key = MsgSharedSteerRunning
		case SteerProcessed:
			key = MsgSharedSteerProcessed
		default:
			key = MsgSharedSteerUnconfirmed
		}
	} else if steerer, ok := as.(AgentSessionSteerer); ok {
		err = steerer.Steer(expectedTurnID, text)
	} else {
		return MsgSharedUnsupported, nil
	}
	return key, err
}
