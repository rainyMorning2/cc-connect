package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type interactiveControl struct {
	Action         string                     `json:"action"`
	RequestID      json.RawMessage            `json:"requestId,omitempty"`
	Decision       string                     `json:"decision,omitempty"`
	Answers        map[string]userInputAnswer `json:"answers,omitempty"`
	ThreadID       string                     `json:"threadId,omitempty"`
	ExpectedTurnID string                     `json:"expectedTurnId,omitempty"`
	Text           string                     `json:"text,omitempty"`
}

type received struct {
	message message
	err     error
}
type controlLine struct {
	control interactiveControl
	err     error
}

func watchInteractive(ctx context.Context, c *client, thread string, out *json.Encoder, input io.Reader, snapshot threadSnapshot) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	turns, err := newTurnControls(thread, snapshot)
	if err != nil {
		return err
	}
	events := make(chan received, 64)
	controls := make(chan controlLine)
	go readInteractiveEvents(ctx, c, events)
	go readInteractiveControls(ctx, input, controls)
	tracker := &approvalTracker{thread: thread, pending: make(map[string]pendingApproval)}
	questions := &userInputTracker{thread: thread, pending: make(map[string]pendingUserInput)}
	handle := func(r received) error {
		return handleInteractiveEvent(r, tracker, questions, turns, out)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-events:
			if err := handle(r); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		case line, ok := <-controls:
			if !ok {
				controls = nil
				continue
			}
			// Apply already-received resolution events before accepting input.
		drain:
			for {
				select {
				case r := <-events:
					if err := handle(r); err != nil {
						return err
					}
				default:
					break drain
				}
			}
			if line.err != nil {
				if err := out.Encode(map[string]any{"probe": "controlRejected", "reason": line.err.Error()}); err != nil {
					return err
				}
				continue
			}
			if line.control.Action == "detach" {
				return nil
			}
			if handled, err := executeTurnControl(ctx, c, turns, out, line.control); handled {
				if err != nil {
					return err
				}
				continue
			}
			if err := executeInteractiveControl(ctx, c, tracker, questions, out, line.control); err != nil {
				return err
			}
		}
	}
}

func handleInteractiveEvent(r received, approvals *approvalTracker, questions *userInputTracker, turns *turnControls, out *json.Encoder) error {
	if r.err != nil {
		return r.err
	}
	if err := approvals.observe(r.message); err != nil {
		return fmt.Errorf("track approval: %w", err)
	}
	if err := questions.observe(r.message); err != nil {
		return fmt.Errorf("track user input: %w", err)
	}
	if err := turns.observe(r.message); err != nil {
		return err
	}
	if report, ok := turns.response(r.message); ok {
		return out.Encode(report)
	}
	return out.Encode(r.message)
}

func executeTurnControl(ctx context.Context, c *client, turns *turnControls, out *json.Encoder, cmd interactiveControl) (bool, error) {
	switch cmd.Action {
	case "status":
		return true, out.Encode(map[string]any{"probe": "turnState", "threadId": turns.thread, "activeTurnId": turns.active, "canSteer": turns.canSteer})
	case "steer", "interrupt":
		id, err := turns.start(ctx, c, cmd)
		if err != nil {
			return true, out.Encode(map[string]any{"probe": "controlRejected", "reason": err.Error()})
		}
		return true, out.Encode(map[string]any{"probe": cmd.Action + "RequestSent", "threadId": turns.thread, "expectedTurnId": cmd.ExpectedTurnID, "requestId": id})
	default:
		return false, nil
	}
}

func executeInteractiveControl(ctx context.Context, c *client, approvals *approvalTracker, questions *userInputTracker, out *json.Encoder, cmd interactiveControl) error {
	switch cmd.Action {
	case "pending-questions":
		return out.Encode(map[string]any{"probe": "pendingQuestions", "threadId": questions.thread, "requests": questions.list()})
	case "answer", "skip":
		if err := questions.reply(cmd, func(v any) error { return c.write(ctx, v) }); err != nil {
			return out.Encode(map[string]any{"probe": "controlRejected", "reason": err.Error()})
		}
		// Do not echo answers: questions may contain secrets.
		return out.Encode(map[string]any{"probe": "userInputReplySent", "threadId": questions.thread, "requestId": cmd.RequestID, "action": cmd.Action})
	default:
		return executeApprovalControl(ctx, c, approvals, out, cmd)
	}
}

func readInteractiveEvents(ctx context.Context, c *client, events chan<- received) {
	for {
		m, err := c.next(ctx)
		select {
		case events <- received{m, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func readInteractiveControls(ctx context.Context, input io.Reader, controls chan<- controlLine) {
	defer close(controls)
	s := bufio.NewScanner(input)
	s.Buffer(make([]byte, 4096), 64*1024)
	for s.Scan() {
		var cmd interactiveControl
		err := json.Unmarshal(s.Bytes(), &cmd)
		select {
		case controls <- controlLine{cmd, err}:
		case <-ctx.Done():
			return
		}
	}
	if err := s.Err(); err != nil {
		select {
		case controls <- controlLine{err: err}:
		case <-ctx.Done():
		}
	}
}

func executeApprovalControl(ctx context.Context, c *client, t *approvalTracker, out *json.Encoder, cmd interactiveControl) error {
	if cmd.Action == "pending" {
		keys := make([]string, 0, len(t.pending))
		for key := range t.pending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pending := make([]pendingApproval, 0, len(keys))
		for _, key := range keys {
			pending = append(pending, t.pending[key])
		}
		return out.Encode(map[string]any{"probe": "pendingApprovals", "threadId": t.thread, "requests": pending})
	}
	var err error
	if cmd.Action != "reply" {
		err = fmt.Errorf("supported actions: status, steer, interrupt, pending, reply, pending-questions, answer, skip, detach")
	} else {
		err = t.reply(cmd, func(v any) error { return c.write(ctx, v) })
	}
	if err != nil {
		return out.Encode(map[string]any{"probe": "controlRejected", "reason": err.Error()})
	}
	return out.Encode(map[string]any{"probe": "approvalReplySent", "threadId": t.thread, "requestId": cmd.RequestID, "decision": cmd.Decision})
}
