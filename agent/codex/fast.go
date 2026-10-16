package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func validateServiceTier(tier string) error {
	if tier != "fast" && tier != "default" {
		return fmt.Errorf("codex: unsupported service tier %q", tier)
	}
	return nil
}

func (a *Agent) GetServiceTier() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.serviceTier
}

func (a *Agent) SetServiceTier(_ context.Context, tier string) error {
	if err := validateServiceTier(tier); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serviceTier = tier
	return nil
}

func (cs *codexSession) GetServiceTier() string {
	cs.runtimeCfgMu.Lock()
	tier := cs.serviceTier
	cs.runtimeCfgMu.Unlock()
	if tier != "" {
		return tier
	}
	cs.runtimeConfig()
	cs.runtimeCfgMu.Lock()
	defer cs.runtimeCfgMu.Unlock()
	return cs.runtimeCfgTier
}

func (cs *codexSession) SetServiceTier(_ context.Context, tier string) error {
	if err := validateServiceTier(tier); err != nil {
		return err
	}
	// Serialize with Send so an active turn keeps its original tier in footers.
	if !cs.turnInFlight.CompareAndSwap(false, true) {
		return core.ErrAgentTurnBusy
	}
	defer cs.turnInFlight.Store(false)
	cs.runtimeCfgMu.Lock()
	defer cs.runtimeCfgMu.Unlock()
	cs.serviceTier = tier
	return nil
}

func (s *appServerSession) GetServiceTier() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.serviceTier
}

func (s *appServerSession) storeServiceTier(tier string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.serviceTier = strings.TrimSpace(tier)
}

func (s *appServerSession) SetServiceTier(ctx context.Context, tier string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := validateServiceTier(tier); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.stateMu.Lock()
	busy := s.currentTurn != ""
	s.stateMu.Unlock()
	if busy {
		return core.ErrAgentTurnBusy
	}
	timeout := appServerRequestTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if err := s.requestWithTimeout("thread/settings/update", map[string]any{"threadId": s.CurrentSessionID(), "serviceTier": tier}, nil, timeout); err != nil {
		return fmt.Errorf("codex app-server update service tier: %w", err)
	}
	s.storeServiceTier(tier)
	return nil
}

func (s *managedSession) GetServiceTier() string { return s.decoder.GetServiceTier() }

func (s *managedSession) SetServiceTier(ctx context.Context, tier string) error {
	if err := validateServiceTier(tier); err != nil {
		return err
	}
	if err := s.updateCurrentThreadParams(ctx, map[string]any{"serviceTier": tier}); err != nil {
		return err
	}
	s.decoder.storeServiceTier(tier)
	return nil
}

func (s *appServerSession) handleSettingsUpdated(raw json.RawMessage) {
	var p struct {
		ThreadID       string `json:"threadId"`
		ThreadSettings struct {
			Model       string  `json:"model"`
			Effort      *string `json:"effort"`
			ServiceTier *string `json:"serviceTier"`
		} `json:"threadSettings"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		s.emitError(fmt.Errorf("codex app-server decode thread settings: %w", err))
		return
	}
	if p.ThreadID != s.CurrentSessionID() {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.model = p.ThreadSettings.Model
	s.effort = normalizeRuntimeReasoningEffort(stringValue(p.ThreadSettings.Effort))
	s.serviceTier = stringValue(p.ThreadSettings.ServiceTier)
}
