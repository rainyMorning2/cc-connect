package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedSnapshot_AttachAndReconnectHistoryExceedsWebSocketLimit(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		t.Run(fmt.Sprintf("reconnect=%t", reconnect), func(t *testing.T) {
			cwd := t.TempDir()
			var connections, pages atomic.Int32
			attached := make(chan struct{})
			text := strings.Repeat("x", 12*1024*1024)
			turns := []any{
				map[string]any{"id": "active", "status": "inProgress", "items": []any{}},
				map[string]any{"id": "old", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "OFFLINE FINAL"}}},
			}
			for i := 0; i < 3; i++ {
				turns = append(turns, map[string]any{"id": fmt.Sprint(i), "status": "completed", "items": []any{map[string]any{"type": "commandExecution", "aggregatedOutput": text}}})
			}
			socket := managedFixture(t, func(ws *websocket.Conn) {
				_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
				conn := connections.Add(1)
				fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
					switch m.Method {
					case "thread/resume":
						var params map[string]any
						_ = json.Unmarshal(m.Params, &params)
						if params["excludeTurns"] != true {
							t.Error("resume requested unbounded history")
							return nil, true
						}
						if reconnect && conn == 1 {
							_ = ws.WriteJSON(map[string]any{"id": m.ID, "result": fixtureSnapshot(cwd, "target", "old")})
							<-attached
							_ = ws.Close()
							return nil, false
						}
						snap := fixtureSnapshot(cwd, "target", "")
						snap["turnsBackwardsCursor"] = "0"
						return snap, true
					case "thread/turns/list":
						var params struct {
							Cursor    string `json:"cursor"`
							Limit     int    `json:"limit"`
							Direction string `json:"sortDirection"`
						}
						_ = json.Unmarshal(m.Params, &params)
						if params.Limit != 1 || params.Direction != "desc" {
							t.Errorf("unbounded/unsorted snapshot page: %s", m.Params)
						}
						n, err := strconv.Atoi(params.Cursor)
						if err != nil || n < 0 || n >= len(turns) {
							t.Errorf("invalid cursor: %s", m.Params)
							return nil, true
						}
						pages.Add(1)
						page := map[string]any{"data": []any{turns[n]}}
						if n+1 < len(turns) {
							page["nextCursor"] = strconv.Itoa(n + 1)
						}
						return page, true
					default:
						t.Errorf("unexpected RPC %s", m.Method)
						return nil, true
					}
				})
			})
			as, err := fixtureManagedAgent(t, socket, cwd, map[string]any{"daemon_reconnect_attempts": 1}).AttachSession(context.Background(), "target")
			close(attached)
			if err != nil {
				t.Fatal(err)
			}
			defer as.Close()
			if reconnect {
				deadline := time.NewTimer(30 * time.Second)
				defer deadline.Stop()
				found := false
				for !found {
					select {
					case e := <-as.Events():
						if e.Type == core.EventError {
							t.Fatal(e.Error)
						}
						if e.Type == core.EventResult {
							if e.Content != "OFFLINE FINAL" || e.TurnID != "old" {
								t.Fatalf("bad recovery: %+v", e)
							}
							found = true
						}
					case <-deadline.C:
						t.Fatal("reconnect did not recover result")
					}
				}
			}
			if got := as.(core.SharedAgentSession).RuntimeState().TurnID; got != "active" {
				t.Fatalf("active turn=%q", got)
			}
			if pages.Load() != int32(len(turns)) {
				t.Fatalf("pages=%d", pages.Load())
			}
		})
	}
}

func TestManagedSnapshot_RejectsRepeatedCursor(t *testing.T) {
	cwd := t.TempDir()
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method == "thread/resume" {
				return fixtureSnapshot(cwd, "target", ""), true
			}
			return map[string]any{"data": []any{}, "nextCursor": "repeat"}, true
		})
	})
	as, err := fixtureManagedAgent(t, socket, cwd, nil).AttachSession(context.Background(), "target")
	if err == nil {
		as.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("err=%v", err)
	}
}
