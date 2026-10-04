package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"time"

	"github.com/gorilla/websocket"
)

// This probe never owns the daemon process. Close only closes its own socket.
type daemonInfo struct {
	Status           string `json:"status"`
	SocketPath       string `json:"socketPath"`
	CLIVersion       string `json:"cliVersion"`
	AppServerVersion string `json:"appServerVersion"`
}

func discover(ctx context.Context, binary string) (daemonInfo, error) {
	b, err := exec.CommandContext(ctx, binary, "app-server", "daemon", "version").Output()
	if err != nil {
		return daemonInfo{}, fmt.Errorf("daemon discovery (version only): %w", err)
	}
	return parseDiscovery(b)
}

func parseDiscovery(b []byte) (daemonInfo, error) {
	var info daemonInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return info, fmt.Errorf("decode daemon discovery: %w", err)
	}
	if info.Status != "running" || info.SocketPath == "" {
		return info, fmt.Errorf("daemon is not running or did not report socketPath (status %q)", info.Status)
	}
	return info, nil
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// client has one reader and one writer. request is only used before entering
// an interactive event reader; interactive RPCs match responses in that loop.
// Messages received before a synchronous RPC response are retained in order.
type client struct {
	conn   *websocket.Conn
	nextID int
	queued []message
}

func connect(ctx context.Context, socket string) (*client, error) {
	d := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	// Same HTTP handshake URI as Codex RemoteAppServerClient. No TCP or proxy.
	conn, resp, err := d.DialContext(ctx, "ws://localhost/rpc", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("daemon websocket over UDS: %w", err)
	}
	conn.SetReadLimit(32 * 1024 * 1024)
	c := &client{conn: conn}
	var result json.RawMessage
	err = c.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "cc-connect-daemon-probe", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &result)
	if err == nil {
		err = c.write(ctx, map[string]any{"method": "initialized"})
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func deadline(ctx context.Context) time.Time {
	if t, ok := ctx.Deadline(); ok {
		return t
	}
	return time.Now().Add(30 * time.Second)
}

func (c *client) write(ctx context.Context, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.conn.SetWriteDeadline(deadline(ctx)); err != nil {
		return err
	}
	if err := c.conn.WriteJSON(v); err != nil {
		return fmt.Errorf("daemon write: %w", err)
	}
	return nil
}

func (c *client) read(ctx context.Context) (message, error) {
	var m message
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if err := c.conn.SetReadDeadline(deadline(ctx)); err != nil {
		return m, err
	}
	if err := c.conn.ReadJSON(&m); err != nil {
		return m, fmt.Errorf("daemon read: %w", err)
	}
	return m, nil
}

func (c *client) request(ctx context.Context, method string, params, out any) error {
	c.nextID++
	id := c.nextID
	if err := c.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		m, err := c.read(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if m.Method == "" && string(m.ID) == fmt.Sprint(id) {
			if m.Error != nil {
				return fmt.Errorf("%s: RPC %d: %s", method, m.Error.Code, m.Error.Message)
			}
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("%s response: %w", method, err)
			}
			return nil
		}
		if len(c.queued) >= 4096 {
			return fmt.Errorf("%s: probe event queue full", method)
		}
		c.queued = append(c.queued, m)
	}
}

func (c *client) next(ctx context.Context) (message, error) {
	if len(c.queued) == 0 {
		return c.read(ctx)
	}
	m := c.queued[0]
	c.queued[0] = message{}
	c.queued = c.queued[1:]
	return m, nil
}

func (c *client) close() { _ = c.conn.Close() }
