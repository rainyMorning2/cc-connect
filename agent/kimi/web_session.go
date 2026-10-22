package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type webFrame struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Code      int             `json:"code"`
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	Epoch     string          `json:"epoch"`
	Volatile  bool            `json:"volatile"`
	Offset    *int64          `json:"offset"`
	Payload   json.RawMessage `json:"payload"`
}
type webSnapshot struct {
	Subagents []webTask `json:"subagents"`
	Seq       int64     `json:"as_of_seq"`
	Epoch     string    `json:"epoch"`
	Session   webRecord `json:"session"`
	InFlight  *struct {
		TurnID   int64  `json:"turn_id"`
		Text     string `json:"assistant_text"`
		PromptID string `json:"current_prompt_id"`
	} `json:"in_flight_turn"`
	Approvals []webApproval `json:"pending_approvals"`
	Questions []webQuestion `json:"pending_questions"`
}
type webSession struct {
	agent             *webAgent
	id                string
	workDir           string // workspace captured at attachment; independent of later /dir changes
	ctx               context.Context
	cancel            context.CancelFunc
	events            chan core.Event // run is the only sender and closer
	done              chan struct{}
	mu                sync.Mutex
	conn              *websocket.Conn
	closed, connected bool
	turn, prompt      string
	model, effort     string // guarded by mu, like usage
	usage             core.ContextUsage
	hasUsage          bool
	own               map[string]bool
	submitted         map[string]bool
	// Reader-owned protocol state. No other goroutine touches these fields.
	seq            int64
	epoch          string
	turnKeys       map[int64]string
	text           map[string]string
	completed      map[string]bool
	pending        map[string]webPending // guarded by mu; RespondPermission reads it
	completedOrder []string
	initial        webSnapshot
	subagents      map[string]core.SubagentNotice // Reader-owned lifecycle state.
	subagentOrder  []string
}

var _ core.SharedAgentSession = (*webSession)(nil)
var _ core.AgentTurnSender = (*webSession)(nil)
var _ core.AgentTurnCanceller = (*webSession)(nil)

func newWebSession(ctx context.Context, a *webAgent, id, workDir string) (*webSession, error) {
	lifetime, cancel := context.WithCancel(ctx)
	s := &webSession{agent: a, id: id, workDir: workDir, ctx: lifetime, cancel: cancel, events: make(chan core.Event, 128), done: make(chan struct{}), own: map[string]bool{}, submitted: map[string]bool{}, turnKeys: map[int64]string{}, text: map[string]string{}, completed: map[string]bool{}, pending: map[string]webPending{}}
	snap, err := s.snapshot()
	if err != nil {
		cancel()
		return nil, err
	}
	conn, replay, err := s.connect(snap.Seq, snap.Epoch)
	if err != nil {
		cancel()
		return nil, err
	}
	s.conn = conn
	s.connected = true
	s.initial = snap
	s.setSnapshotState(snap)
	s.refreshRuntimeStatus()
	context.AfterFunc(lifetime, func() {
		s.mu.Lock()
		c := s.conn
		s.mu.Unlock()
		if c != nil {
			c.Close()
		}
	})
	go s.run(conn, replay)
	return s, nil
}
func (s *webSession) snapshot() (webSnapshot, error) {
	var snap webSnapshot
	if err := s.agent.api.request(s.ctx, "GET", webSessionPath(s.id)+"/snapshot", nil, &snap); err != nil {
		return snap, err
	}
	if snap.Session.ID != s.id {
		return snap, fmt.Errorf("kimi web: snapshot session ID does not match %q", s.id)
	}
	if filepath.Clean(snap.Session.Metadata.Cwd) != s.workDir {
		return snap, fmt.Errorf("kimi web: session working directory %q does not match selected directory %q", snap.Session.Metadata.Cwd, s.workDir)
	}
	if snap.Epoch == "" {
		return snap, fmt.Errorf("kimi web: snapshot missing epoch")
	}
	return snap, nil
}
func (s *webSession) connect(seq int64, epoch string) (*websocket.Conn, []webFrame, error) {
	token, err := s.agent.api.token()
	if err != nil {
		return nil, nil, err
	}
	addr := "ws" + strings.TrimPrefix(s.agent.api.base, "http") + "/api/v1/ws"
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(s.ctx, addr, http.Header{"Authorization": []string{"Bearer " + token}})
	if resp != nil && err != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("kimi web: websocket connect: %s", core.RedactToken(err.Error(), token))
	}
	conn.SetReadLimit(32 << 20)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = conn.WriteJSON(map[string]any{"type": "subscribe", "id": "cc-connect", "payload": map[string]any{"session_ids": []string{s.id}, "cursors": map[string]any{s.id: map[string]any{"seq": seq, "epoch": epoch}}}})
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("kimi web: subscribe: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var replay []webFrame
	for len(replay) < 2048 {
		var f webFrame
		if err := conn.ReadJSON(&f); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("kimi web: subscribe acknowledgement: %w", err)
		}
		if f.Type == "resync_required" {
			conn.Close()
			return nil, nil, fmt.Errorf("kimi web: subscription requires resync")
		}
		if f.Type != "ack" || f.ID != "cc-connect" {
			replay = append(replay, f)
			continue
		}
		var ack struct {
			Accepted []string `json:"accepted"`
			NotFound []string `json:"not_found"`
			Resync   []string `json:"resync_required"`
		}
		if err := json.Unmarshal(f.Payload, &ack); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("kimi web: invalid subscribe ack: %w", err)
		}
		accepted := false
		for _, id := range ack.Accepted {
			accepted = accepted || id == s.id
		}
		if f.Code != 0 || !accepted || len(ack.NotFound) > 0 || len(ack.Resync) > 0 {
			conn.Close()
			return nil, nil, fmt.Errorf("kimi web: subscription rejected (%d)", f.Code)
		}
		conn.SetReadDeadline(time.Time{})
		return conn, replay, nil
	}
	conn.Close()
	return nil, nil, fmt.Errorf("kimi web: subscription replay exceeds limit")
}
func (s *webSession) setSnapshotState(snap webSnapshot) {
	s.seq, s.epoch = snap.Seq, snap.Epoch
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateRecordStatusLocked(snap.Session)
	previousTurn := s.turn
	s.turn, s.prompt = "", ""
	if f := snap.InFlight; f != nil {
		key := f.PromptID
		if key == "" {
			key = snap.Session.CurrentPrompt
		}
		if key == "" {
			key = fmt.Sprintf("%s/%d", snap.Epoch, f.TurnID)
		}
		s.turnKeys[f.TurnID] = key
		s.turn = key
		s.prompt = f.PromptID
		if s.prompt == "" {
			s.prompt = snap.Session.CurrentPrompt
		}
		s.text[key] = f.Text
	}
	if s.turn != previousTurn {
		s.usage.InputTokens, s.usage.OutputTokens = 0, 0
		s.usage.CachedInputTokens, s.usage.CacheCreationInputTokens = 0, 0
	}
}
func (s *webSession) emit(e core.Event) {
	e.SessionID = s.id
	select {
	case s.events <- e:
	case <-s.ctx.Done():
	}
}
func (s *webSession) run(conn *websocket.Conn, replay []webFrame) {
	defer close(s.done)
	defer close(s.events)
	defer s.cancel()
	defer func() {
		s.mu.Lock()
		s.connected = false
		s.closed = true
		s.mu.Unlock()
		conn.Close()
		s.agent.mu.Lock()
		delete(s.agent.sessions, s)
		s.agent.mu.Unlock()
	}()
	s.emitSnapshot(s.initial)
	attempts := 0
	for s.ctx.Err() == nil {
		var f webFrame
		var err error
		if len(replay) > 0 {
			f = replay[0]
			replay = replay[1:]
		} else {
			err = conn.ReadJSON(&f)
		}
		if err == nil {
			if f.Type == "resync_required" {
				err = fmt.Errorf("kimi web: resynchronization requested")
			} else if f.Type == "ping" {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err = conn.WriteJSON(map[string]any{"type": "pong", "payload": map[string]any{}})
			} else {
				err = s.handleFrame(f)
			}
		}
		if err == nil {
			attempts = 0
			continue
		}
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.connected = false
		s.mu.Unlock()
		conn.Close()
		s.emit(core.Event{Type: core.EventRuntimeStatus, Content: "reconnecting"})
		for {
			attempts++
			if attempts > 3 {
				s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("kimi web: reconnect exhausted: %w", err)})
				return
			}
			timer := time.NewTimer(time.Duration(attempts) * time.Second)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			var snap webSnapshot
			snap, err = s.snapshot()
			if err != nil {
				continue
			}
			nextConn, nextReplay, connectErr := s.connect(snap.Seq, snap.Epoch)
			err = connectErr
			if err != nil {
				continue
			}
			conn, replay = nextConn, nextReplay
			if err = s.reconcileSnapshot(snap); err != nil {
				conn.Close()
				continue
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				conn.Close()
				return
			}
			s.conn = conn
			s.connected = true
			s.mu.Unlock()
			s.emit(core.Event{Type: core.EventRuntimeStatus, Content: "connected"})
			break
		}
	}
}
func (s *webSession) emitSnapshot(snap webSnapshot) {
	s.emitSnapshotSubagents(snap.Subagents)
	state := s.RuntimeState()
	if state.TurnID != "" {
		s.emit(core.Event{Type: core.EventTurnStarted, TurnID: state.TurnID})
		if snap.InFlight.Text != "" {
			s.emit(core.Event{Type: core.EventText, TurnID: state.TurnID, ItemID: state.TurnID, Content: snap.InFlight.Text, Metadata: map[string]any{"text_delta": true}})
		}
	}
	for _, a := range snap.Approvals {
		s.addApproval(a)
	}
	for _, q := range snap.Questions {
		s.addQuestion(q)
	}
}
func (s *webSession) reconcileSnapshot(snap webSnapshot) error {
	old := s.RuntimeState().TurnID
	next := ""
	if snap.InFlight != nil {
		next = snap.InFlight.PromptID
		if next == "" {
			next = snap.Session.CurrentPrompt
		}
	}
	if old != "" && old != next && !s.completed[old] {
		if err := s.finishTurn(old, "completed"); err != nil {
			return err
		}
	}
	if err := s.recoverSubmittedPrompts(next); err != nil {
		return err
	}
	s.mu.Lock()
	oldPending := s.pending
	s.pending = map[string]webPending{}
	s.mu.Unlock()
	for id := range oldPending {
		s.emit(core.Event{Type: core.EventPermissionResolved, RequestID: id})
	}
	previousText := s.text[old]
	s.setSnapshotState(snap)
	s.refreshRuntimeStatus()
	if snap.InFlight != nil && old == s.RuntimeState().TurnID && strings.HasPrefix(snap.InFlight.Text, previousText) {
		copyFlight := *snap.InFlight
		copyFlight.Text = strings.TrimPrefix(copyFlight.Text, previousText)
		snap.InFlight = &copyFlight
	}
	s.emitSnapshot(snap)
	return nil
}
func (s *webSession) Events() <-chan core.Event { return s.events }
func (s *webSession) CurrentSessionID() string  { return s.id }
func (s *webSession) RuntimeState() core.AgentRuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return core.AgentRuntimeState{SessionID: s.id, TurnID: s.turn, Connected: s.connected && !s.closed, CanSteer: s.connected && !s.closed && s.turn != ""}
}
func (s *webSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.ctx.Err() == nil
}
func (s *webSession) Close() error {
	s.mu.Lock()
	s.closed = true
	s.connected = false
	conn := s.conn
	s.mu.Unlock()
	s.cancel()
	if conn != nil {
		conn.Close()
	}
	<-s.done
	return nil
}
func (s *webSession) Send(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	_, err := s.SendTurn(prompt, messageID, images, files)
	return err
}
func (s *webSession) SendTurn(prompt, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (string, error) {
	s.mu.Lock()
	if s.closed || !s.connected {
		s.mu.Unlock()
		return "", fmt.Errorf("kimi web: observer disconnected")
	}
	if s.turn != "" {
		s.mu.Unlock()
		return "", core.ErrAgentTurnBusy
	}
	id := "prompt_" + uuid.NewString()
	s.own[id] = true
	s.mu.Unlock()
	accepted := false
	defer func() {
		if !accepted {
			s.mu.Lock()
			delete(s.own, id)
			s.mu.Unlock()
		}
	}()
	content, uploads, err := s.promptContent(prompt, images, files)
	if err != nil {
		return "", err
	}
	body, err := s.promptBody(id, content)
	if err != nil {
		s.discardUploads(uploads)
		return "", err
	}
	// Once submission is attempted, an ambiguous transport error may still mean
	// the prompt was accepted. Retain its files; never delete a possible reference.
	result, err := s.postPrompt(body)
	if err != nil {
		return "", err
	}
	if result.ID != id {
		return "", fmt.Errorf("kimi web: server returned a different prompt ID")
	}
	accepted = true
	s.mu.Lock()
	if s.own[id] {
		s.submitted[id] = true
	}
	s.mu.Unlock()
	// A second client may have won the race: Kimi queues this exact prompt.
	// Keep its ID bound to the foreground; never resend an already accepted prompt.
	return id, nil
}
func (s *webSession) CancelTurn() error { return s.CancelExpectedTurn(s.RuntimeState().TurnID) }
func (s *webSession) CancelExpectedTurn(turn string) error {
	s.mu.Lock()
	if turn == "" || s.turn != turn || s.prompt == "" {
		s.mu.Unlock()
		return fmt.Errorf("kimi web: active prompt changed or unavailable")
	}
	id := s.prompt
	s.mu.Unlock()
	var result struct {
		Aborted bool `json:"aborted"`
	}
	if err := s.agent.api.request(s.ctx, "POST", webSessionPath(s.id)+"/prompts/"+id+":abort", nil, &result); err != nil {
		return err
	}
	if !result.Aborted {
		return fmt.Errorf("kimi web: prompt was no longer running")
	}
	return nil
}

// A prompt accepted into the server queue may both start and finish while the
// observer is offline. Complete its foreground route too, not just the turn
// that happened to be running at disconnect time.
func (s *webSession) recoverSubmittedPrompts(current string) error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.own))
	for id := range s.submitted {
		if id != current {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	var queue struct {
		Active *struct {
			ID string `json:"prompt_id"`
		} `json:"active"`
		Queued []struct {
			ID string `json:"prompt_id"`
		} `json:"queued"`
	}
	if err := s.agent.api.request(s.ctx, "GET", webSessionPath(s.id)+"/prompts", nil, &queue); err != nil {
		return err
	}
	pending := map[string]bool{}
	if queue.Active != nil {
		pending[queue.Active.ID] = true
	}
	for _, p := range queue.Queued {
		pending[p.ID] = true
	}
	for _, id := range ids {
		if pending[id] || s.completed[id] {
			continue
		}
		if err := s.finishTurn(id, "completed"); err != nil {
			return err
		}
	}
	return nil
}
