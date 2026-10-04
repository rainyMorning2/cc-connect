// codex-daemon-probe validates the shared runtime without changing the CC
// Connect backend. The default mode only lists loaded thread IDs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	binary := flag.String("codex", "codex", "Codex CLI binary used for passive discovery")
	watch := flag.String("watch", "", "explicit thread ID to resume and observe; approval replies require interactive mode")
	findCwd := flag.String("find-cwd", "", "passively find loaded threads in an exact workspace directory")
	selfTest := flag.Bool("self-test", false, "create a separate test thread using a local fake model; test two clients, approval decline/cancel, steer, reconnect and interrupt")
	questionTest := flag.Bool("user-input-self-test", false, "create an isolated thread with a local fake model; test question replay, Other answers, external skip and resolution")
	interactive := flag.Bool("interactive", false, "with watch: accept explicit approval/question JSON commands on stdin; never auto-reply")
	duration := flag.Duration("timeout", 60*time.Second, "total probe deadline")
	flag.Parse()
	if *interactive && (*watch == "" || *selfTest || *questionTest || *findCwd != "") {
		return fmt.Errorf("interactive requires only an explicit watch thread")
	}
	if *questionTest && (*selfTest || *watch != "" || *findCwd != "") {
		return fmt.Errorf("user-input-self-test cannot be combined with other modes")
	}
	if *selfTest && *watch != "" {
		return fmt.Errorf("choose self-test or watch")
	}
	if *findCwd != "" && (*watch != "" || *selfTest) {
		return fmt.Errorf("find-cwd cannot be combined with watch or self-test")
	}
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(base, *duration)
	defer cancel()
	info, err := discover(ctx, *binary)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(info); err != nil {
		return err
	}
	if *selfTest {
		return selfTestDaemon(ctx, info, enc)
	}
	if *questionTest {
		return selfTestUserInput(ctx, info, enc)
	}
	c, err := connect(ctx, info.SocketPath)
	if err != nil {
		return err
	}
	defer c.close()
	stopClose := context.AfterFunc(ctx, c.close)
	defer stopClose()
	if *findCwd != "" {
		ids, err := findLoadedThreads(ctx, c, *findCwd)
		if err != nil {
			return err
		}
		return enc.Encode(map[string]any{"cwd": *findCwd, "threadIds": ids})
	}
	if *watch == "" {
		var loaded struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := c.request(ctx, "thread/loaded/list", map[string]any{"limit": 100}, &loaded); err != nil {
			return err
		}
		return enc.Encode(loaded)
	}
	if err := guardWatch(*watch, os.Getenv("CODEX_THREAD_ID")); err != nil {
		return err
	}
	var resumed json.RawMessage
	// Do not pass cwd/model/approval/sandbox overrides when joining shared work.
	if err := c.request(ctx, "thread/resume", map[string]any{"threadId": *watch}, &resumed); err != nil {
		return err
	}
	if err := enc.Encode(map[string]any{"resume": resumed}); err != nil {
		return err
	}
	if *interactive {
		var snapshot threadSnapshot
		if err := json.Unmarshal(resumed, &snapshot); err != nil {
			return fmt.Errorf("decode interactive resume: %w", err)
		}
		return watchInteractive(ctx, c, *watch, enc, os.Stdin, snapshot)
	}
	for {
		m, err := c.next(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
}

func findLoadedThreads(ctx context.Context, c *client, cwd string) ([]string, error) {
	if !filepath.IsAbs(cwd) {
		return nil, fmt.Errorf("find-cwd requires an absolute workspace path")
	}
	ids := []string{}
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var loaded struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := c.request(ctx, "thread/loaded/list", params, &loaded); err != nil {
			return nil, err
		}
		for _, id := range loaded.Data {
			var read struct {
				Thread struct {
					Cwd string `json:"cwd"`
				} `json:"thread"`
			}
			if err := c.request(ctx, "thread/read", map[string]any{"threadId": id}, &read); err != nil {
				return nil, err
			}
			if filepath.Clean(read.Thread.Cwd) == filepath.Clean(cwd) {
				ids = append(ids, id)
			}
		}
		if loaded.NextCursor == nil || *loaded.NextCursor == "" {
			return ids, nil
		}
		cursor = *loaded.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("loaded thread pagination repeated cursor")
		}
		seen[cursor] = true
	}
}

func guardWatch(thread, current string) error {
	if thread == "" {
		return fmt.Errorf("explicit thread ID required")
	}
	if current != "" && thread == current {
		return fmt.Errorf("refusing to subscribe to this agent's own CODEX_THREAD_ID")
	}
	return nil
}
