package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type received struct {
	message message
	err     error
}
type controlLine struct {
	control approvalControl
	err     error
}

func watchInteractive(ctx context.Context, c *client, thread string, out *json.Encoder, input io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan received, 64)
	controls := make(chan controlLine)
	go readInteractiveEvents(ctx, c, events)
	go readApprovalControls(ctx, input, controls)
	tracker := &approvalTracker{thread: thread, pending: make(map[string]pendingApproval)}
	handle := func(r received) error {
		if r.err != nil {
			return r.err
		}
		if err := tracker.observe(r.message); err != nil {
			return fmt.Errorf("track approval: %w", err)
		}
		return out.Encode(r.message)
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
			if err := executeApprovalControl(ctx, c, tracker, out, line.control); err != nil {
				return err
			}
		}
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

func readApprovalControls(ctx context.Context, input io.Reader, controls chan<- controlLine) {
	defer close(controls)
	s := bufio.NewScanner(input)
	s.Buffer(make([]byte, 4096), 64*1024)
	for s.Scan() {
		var cmd approvalControl
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

func executeApprovalControl(ctx context.Context, c *client, t *approvalTracker, out *json.Encoder, cmd approvalControl) error {
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
		err = fmt.Errorf("supported actions: pending, reply, detach")
	} else {
		err = t.reply(cmd, func(v any) error { return c.write(ctx, v) })
	}
	if err != nil {
		return out.Encode(map[string]any{"probe": "controlRejected", "reason": err.Error()})
	}
	return out.Encode(map[string]any{"probe": "approvalReplySent", "threadId": t.thread, "requestId": cmd.RequestID, "decision": cmd.Decision})
}
