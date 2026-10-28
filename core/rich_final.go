package core

import "log/slog"

// Send first, then shrink the old preview. A failed send must not erase the
// preview or prevent the caller from finalizing it in place.
func (e *Engine) sendLatestRichFinal(p Platform, replyCtx, previewHandle any, finalCard string, builder RichCardSupporter) bool {
	policy, ok := p.(ApprovalFinalReplyPlatform)
	if !ok || !policy.FinalReplyAfterApproval() || previewHandle == nil {
		return false
	}
	sender, ok := p.(RichCardSender)
	if !ok {
		return false
	}
	if err := sender.SendRichCard(e.ctx, replyCtx, finalCard); err != nil {
		slog.Warn("send latest final reply failed; completing existing preview", "platform", p.Name(), "error", err)
		return false
	}
	if updater, ok := p.(MessageUpdater); ok {
		compact := builder.BuildRichCard(CardStatusDone, "", nil, e.i18n.T(MsgFinalReplyLatest), false, "")
		if err := updater.UpdateMessage(e.ctx, previewHandle, compact); err != nil {
			slog.Warn("compact completed preview failed; latest final reply remains available", "platform", p.Name(), "error", err)
		}
	}
	return true
}

// Only a confirmed insertion belongs to the active turn. Queued or uncertain
// submissions must not relocate an unrelated answer.
func (state *interactiveState) noteRichFinalSteer(turnID string, result MsgKey) {
	if turnID == "" || result != MsgSharedSteerAccepted {
		return
	}
	state.mu.Lock()
	state.lastSteeredTurn = turnID
	state.mu.Unlock()
}

func (state *interactiveState) richFinalWasSteered(turnID string) bool {
	state.mu.Lock()
	owner := state
	if state.sharedPresentation != nil {
		owner = state.sharedPresentation.runtime
		if turnID == "" {
			turnID = state.sharedPresentation.turnID
		}
	}
	state.mu.Unlock()
	if owner == nil || turnID == "" {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.lastSteeredTurn == turnID
}
