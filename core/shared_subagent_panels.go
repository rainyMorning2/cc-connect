package core

import (
	"context"
	"log/slog"
	"sync"
)

// Retains finalized main cards for children that outlive their parent turn.
// Active cards remain owned exclusively by processInteractiveEvents.
type sharedSubagentPanels struct {
	mu       sync.Mutex
	ctx      context.Context
	turns    map[string]*subagentTurnPanel
	children map[string]string
	order    []string
}
type subagentTurnPanel struct {
	notices map[string]SubagentNotice
	order   []string
	final   *subagentFinalCard
	wake    chan struct{}
	cancel  context.CancelFunc
}
type subagentFinalCard struct {
	platform         Platform
	handle           any
	steps            []ToolStep
	markdown, footer string
	status           CardStatus
}

func newSharedSubagentPanels(ctx context.Context) *sharedSubagentPanels {
	return &sharedSubagentPanels{ctx: ctx, turns: map[string]*subagentTurnPanel{}, children: map[string]string{}}
}
func (s *sharedSubagentPanels) turn(id string) *subagentTurnPanel {
	t := s.turns[id]
	if t != nil {
		return t
	}
	t = &subagentTurnPanel{notices: map[string]SubagentNotice{}, wake: make(chan struct{}, 1)}
	s.turns[id] = t
	s.order = append(s.order, id)
	if len(s.order) > 256 {
		old := s.order[0]
		s.order = s.order[1:]
		if prior := s.turns[old]; prior != nil {
			if prior.cancel != nil {
				prior.cancel()
			}
			for child := range prior.notices {
				if s.children[child] == old {
					delete(s.children, child)
				}
			}
		}
		delete(s.turns, old)
	}
	return t
}
func (s *sharedSubagentPanels) record(n SubagentNotice, fallbackTurn string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.children[n.ID]
	if id == "" {
		id = n.TurnID
	}
	if id == "" {
		id = fallbackTurn
	}
	if id == "" {
		return "", false
	}
	t := s.turn(id)
	if _, exists := t.notices[n.ID]; !exists {
		t.order = append(t.order, n.ID)
		if len(t.order) > 256 {
			old := t.order[0]
			t.order = t.order[1:]
			delete(t.notices, old)
			delete(s.children, old)
		}
	}
	n.TurnID = id
	t.notices[n.ID] = n
	s.children[n.ID] = id
	if t.final != nil {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
	return id, t.final != nil
}
func subagentPanelsForState(state *interactiveState) (*sharedSubagentPanels, string) {
	state.mu.Lock()
	runtime := state
	id := ""
	if state.sharedPresentation != nil {
		runtime = state.sharedPresentation.runtime
		id = state.sharedPresentation.turnID
	}
	state.mu.Unlock()
	if runtime == nil {
		return nil, id
	}
	runtime.mu.Lock()
	panels := runtime.shared.subagentPanels
	runtime.mu.Unlock()
	return panels, id
}
func (e *Engine) mergeSubagentPanels(steps []ToolStep, notices map[string]SubagentNotice, order []string) []ToolStep {
	result := make([]ToolStep, 0, len(steps)+len(order))
	for _, step := range steps {
		if step.Kind != ToolStepKindSubagent {
			result = append(result, step)
		}
	}
	for _, id := range order {
		n := notices[id]
		result = append(result, ToolStep{ItemID: n.ID, Kind: ToolStepKindSubagent,
			Name:    e.subagentPanelTitle(n),
			Summary: e.subagentNoticeText(n), ResultTail: subagentPreview(n), Status: n.Status,
			Done: n.Status == "completed" || n.Status == "failed" || n.Status == "cancelled"})
	}
	return result
}
func (e *Engine) sharedSubagentSteps(state *interactiveState, turnID string, steps []ToolStep) []ToolStep {
	s, id := subagentPanelsForState(state)
	if s == nil {
		return steps
	}
	if turnID == "" {
		turnID = id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.turns[turnID]; t != nil {
		return e.mergeSubagentPanels(steps, t.notices, t.order)
	}
	return steps
}
func (e *Engine) retainSubagentMainCard(state *interactiveState, turnID string, card subagentFinalCard) {
	s, id := subagentPanelsForState(state)
	if s == nil || card.handle == nil {
		return
	}
	if turnID == "" {
		turnID = id
	}
	if turnID == "" {
		return
	}
	if _, ok := card.platform.(MessageUpdater); !ok {
		return
	}
	s.mu.Lock()
	t := s.turn(turnID)
	card.steps = append([]ToolStep(nil), card.steps...)
	t.final = &card
	if t.cancel == nil {
		ctx, cancel := context.WithCancel(s.ctx)
		t.cancel = cancel
		go e.runSubagentMainCard(ctx, s, t)
	}
	if len(t.notices) > 0 {
		select {
		case t.wake <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}
func (e *Engine) runSubagentMainCard(ctx context.Context, s *sharedSubagentPanels, t *subagentTurnPanel) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
		}
		s.mu.Lock()
		card := *t.final
		steps := e.mergeSubagentPanels(card.steps, t.notices, t.order)
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		content := card.platform.(RichCardSupporter).BuildRichCard(card.status, "", steps, card.markdown, false, card.footer)
		if err := card.platform.(MessageUpdater).UpdateMessage(ctx, card.handle, content); err != nil {
			slog.Warn("subagent: update parent card", "error", err)
		}
	}
}

func hasSubagentSteps(steps []ToolStep) bool {
	for _, step := range steps {
		if step.Kind == ToolStepKindSubagent {
			return true
		}
	}
	return false
}

func (e *Engine) sendRetainedRichCard(state *interactiveState, p Platform, reply any, content string) (any, error) {
	if panels, _ := subagentPanelsForState(state); panels != nil {
		if starter, ok := p.(PreviewStarter); ok {
			return starter.SendPreviewStart(e.ctx, reply, content)
		}
	}
	return nil, p.Send(e.ctx, reply, content)
}

func supportsSubagentPanels(p Platform) bool {
	_, rich := p.(RichCardSupporter)
	_, start := p.(PreviewStarter)
	_, update := p.(MessageUpdater)
	return rich && start && update
}

func (f *sharedForeground) acceptsSubagentTurn(id string) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.terminal && f.ctx.Err() == nil && (!f.bound || f.turnID == "" || f.turnID == id)
}
