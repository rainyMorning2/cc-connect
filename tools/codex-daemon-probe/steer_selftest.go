package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Drive the same stdin/event loop used by watch -interactive, rather than
// issuing a synchronous RPC on a socket with an active reader.
func selfTestInteractiveSteer(ctx context.Context, c *client, thread string, snapshot threadSnapshot, out *json.Encoder) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.close()
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	stopClose := context.AfterFunc(ctx, func() { c.close(); _ = outR.Close(); _ = inR.Close() })
	defer stopClose()
	done := make(chan error, 1)
	go func() {
		defer outW.Close()
		done <- watchInteractive(ctx, c, thread, json.NewEncoder(outW), inR, snapshot)
	}()
	commands := json.NewEncoder(inW)
	responses := json.NewDecoder(outR)
	if err := commands.Encode(interactiveControl{Action: "steer", ExpectedTurnID: "not-the-active-turn", Text: "must be rejected"}); err != nil {
		return err
	}
	if err := readSteerReport(responses, "controlRejected", out); err != nil {
		return err
	}
	turn := activeTurn(snapshot)
	if err := commands.Encode(interactiveControl{Action: "steer", ExpectedTurnID: turn, Text: "cc-connect-steer-marker"}); err != nil {
		return err
	}
	if err := readSteerReport(responses, "steerRequestSent", out); err != nil {
		return err
	}
	if err := readSteerReport(responses, "steerAccepted", out); err != nil {
		return err
	}
	if err := commands.Encode(interactiveControl{Action: "detach"}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func readSteerReport(decoder *json.Decoder, wanted string, out *json.Encoder) error {
	for {
		var report map[string]json.RawMessage
		if err := decoder.Decode(&report); err != nil {
			return fmt.Errorf("read interactive steer report: %w", err)
		}
		if err := out.Encode(report); err != nil {
			return err
		}
		var kind string
		if err := json.Unmarshal(report["probe"], &kind); err != nil {
			continue // Notifications can arrive before the response.
		}
		if kind == wanted {
			return nil
		}
		if kind == "steerRejected" || kind == "controlRejected" {
			return fmt.Errorf("interactive steer rejected while waiting for %s: %s", wanted, report["reason"])
		}
	}
}

func selfTestRejectedSteer(ctx context.Context, c *client, thread, expected, scenario string, out *json.Encoder) error {
	params := map[string]any{"threadId": thread, "expectedTurnId": expected, "input": input("cc-connect-rejected-steer-marker")}
	err := c.request(ctx, "turn/steer", params, nil)
	if err == nil {
		return fmt.Errorf("daemon accepted invalid steer: %s", scenario)
	}
	// A disconnect or timeout does not prove server-side rejection.
	if !strings.Contains(err.Error(), ": RPC ") {
		return fmt.Errorf("invalid steer did not receive an RPC error: %w", err)
	}
	return out.Encode(map[string]any{"check": "daemon rejected steer: " + scenario, "threadId": thread,
		"expectedTurnId": expected, "request": params, "error": err.Error(), "passed": true})
}
