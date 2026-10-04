package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: this test uses the daemon's default REAL model and can
// consume inference quota. All controls target its newly created root thread.
func TestRealDaemonSteer(t *testing.T) {
	if os.Getenv("CC_CONNECT_REAL_STEER") != "1" {
		t.Skip("set CC_CONNECT_REAL_STEER=1 to run against the real model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	info, err := discover(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.MkdirTemp("", "cc-connect-real-steer-")
	if err != nil {
		t.Fatal(err)
	}
	logPath := cwd + "/evidence.jsonl"
	file, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	log := json.NewEncoder(file)
	primary, err := connect(ctx, info.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.close()
	stopPrimary := context.AfterFunc(ctx, primary.close)
	defer stopPrimary()
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model         string `json:"model"`
		ModelProvider string `json:"modelProvider"`
	}
	// No model/provider/config overrides: use the running daemon's real setup.
	if err := primary.request(ctx, "thread/start", map[string]any{"cwd": cwd, "approvalPolicy": "never", "sandbox": "read-only"}, &started); err != nil {
		t.Fatal(err)
	}
	thread := started.Thread.ID
	if thread == "" || thread == os.Getenv("CODEX_THREAD_ID") || strings.Contains(started.ModelProvider, "probe") {
		t.Fatalf("not a new real-model test thread: %+v", started)
	}
	if err := log.Encode(map[string]any{"at": time.Now().UTC(), "cliVersion": info.CLIVersion, "appServerVersion": info.AppServerVersion,
		"threadId": thread, "model": started.Model, "modelProvider": started.ModelProvider, "providerOverride": false, "cwd": cwd}); err != nil {
		t.Fatal(err)
	}
	t.Logf("real model=%s provider=%s thread=%s evidence=%s", started.Model, started.ModelProvider, thread, logPath)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	prompt := `这是 CC Connect 的隔离 steer 验收任务。不要修改文件，不要联网，不要读取配置或其他文件。
只运行一次下面的命令，用 exec_command，yield_time_ms=1000、max_output_tokens=200：
python3 -u -c 'import time; [(print("REAL-STEER-TICK", i, flush=True), time.sleep(2)) for i in range(25)]'
随后使用 write_stdin 轮询同一个进程直到结束，不要重新执行命令。运行期间简短报告进度。
如果没有收到后续调整指示，最后严格回复 REAL-STEER-BASELINE-DONE。`
	if err := primary.request(ctx, "turn/start", map[string]any{"threadId": thread, "input": input(prompt), "effort": "low"}, &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Turn.ID == "" {
		t.Fatal("missing turn ID")
	}
	if err := realSteerWaitForCommand(ctx, primary, thread, log); err != nil {
		t.Fatal(err)
	}
	observer, err := connect(ctx, info.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.close()
	var snapshot threadSnapshot
	if err := observer.request(ctx, "thread/resume", map[string]any{"threadId": thread}, &snapshot); err != nil {
		t.Fatal(err)
	}
	if activeTurn(snapshot) != turn.Turn.ID {
		t.Fatal("resume did not recover original active turn")
	}
	marker := fmt.Sprintf("STEER-SEEN-%d", time.Now().UnixNano())
	final := strings.Replace(marker, "STEER-SEEN", "STEER-REAL-DONE", 1)
	if err := realSteerObserve(ctx, observer, thread, snapshot, marker, final, log); err != nil {
		t.Fatal(err)
	}
	if err := realSteerVerifyPrimary(ctx, primary, thread, turn.Turn.ID, final, log); err != nil {
		t.Fatal(err)
	}
	t.Logf("PASS original turn=%s changed progress=%s changed final=%s", turn.Turn.ID, marker, final)
}

func realSteerWaitForCommand(ctx context.Context, c *client, thread string, log *json.Encoder) error {
	for {
		m, err := c.next(ctx)
		if err != nil {
			return err
		}
		var p struct {
			ThreadID string `json:"threadId"`
			Delta    string `json:"delta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil || p.ThreadID != thread {
			continue
		}
		if err := log.Encode(map[string]any{"side": "primary-before-steer", "message": m}); err != nil {
			return err
		}
		if m.Method == "item/commandExecution/outputDelta" && strings.Contains(p.Delta, "REAL-STEER-TICK") {
			return nil
		}
		if m.Method == "turn/completed" {
			return fmt.Errorf("turn completed before live command output")
		}
		if strings.HasSuffix(m.Method, "requestApproval") {
			return fmt.Errorf("test command unexpectedly requested approval; no automatic reply")
		}
	}
}

func realSteerObserve(ctx context.Context, c *client, thread string, snapshot threadSnapshot, marker, final string, log *json.Encoder) error {
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	defer c.close()
	stopClose := context.AfterFunc(ctx, func() { c.close(); _ = outR.Close(); _ = inR.Close() })
	defer stopClose()
	done := make(chan error, 1)
	go func() {
		defer outW.Close()
		done <- watchInteractive(ctx, c, thread, json.NewEncoder(outW), inR, snapshot)
	}()
	cmd := interactiveControl{Action: "steer", ExpectedTurnID: activeTurn(snapshot),
		Text: fmt.Sprintf("这是中途调整：继续等待原来的打印进程结束，不要中断或重跑命令。下一条进度回复必须以 %s 开头，说明已经收到调整；最终只回复 %s，替代原来的 baseline 标记。", marker, final)}
	if err := log.Encode(map[string]any{"side": "observer-control", "threadId": thread, "command": cmd}); err != nil {
		return err
	}
	commands := json.NewEncoder(inW)
	if err := commands.Encode(cmd); err != nil {
		return err
	}
	decoder := json.NewDecoder(outR)
	accepted, sawProgress, sawFinal := false, false, false
	for {
		var report map[string]json.RawMessage
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		if err := log.Encode(map[string]any{"side": "observer", "report": report}); err != nil {
			return err
		}
		var kind string
		_ = json.Unmarshal(report["probe"], &kind)
		if kind == "steerRejected" || kind == "controlRejected" {
			return fmt.Errorf("real steer rejected: %s", report["reason"])
		}
		if kind == "steerAccepted" {
			accepted = true
		}
		var m message
		data, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil || p.ThreadID != thread {
			continue
		}
		if m.Method == "turn/started" && p.Turn.ID != cmd.ExpectedTurnID {
			return fmt.Errorf("steer unexpectedly started a different turn")
		}
		if m.Method == "item/completed" && p.Item.Type == "agentMessage" {
			sawProgress = sawProgress || strings.HasPrefix(p.Item.Text, marker)
			sawFinal = sawFinal || strings.TrimSpace(p.Item.Text) == final
		}
		if m.Method == "turn/completed" {
			if p.Turn.ID != cmd.ExpectedTurnID || p.Turn.Status != "completed" || !accepted || !sawProgress || !sawFinal {
				return fmt.Errorf("real steer verification failed: turn=%s status=%s accepted=%v progress=%v final=%v", p.Turn.ID, p.Turn.Status, accepted, sawProgress, sawFinal)
			}
			break
		}
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

func realSteerVerifyPrimary(ctx context.Context, c *client, thread, turn, final string, log *json.Encoder) error {
	sawFinal := false
	for {
		m, err := c.next(ctx)
		if err != nil {
			return err
		}
		var p struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil || p.ThreadID != thread {
			continue
		}
		if err := log.Encode(map[string]any{"side": "primary-after-steer", "message": m}); err != nil {
			return err
		}
		if m.Method == "item/completed" && p.Item.Type == "agentMessage" && strings.TrimSpace(p.Item.Text) == final {
			sawFinal = true
		}
		if m.Method == "turn/completed" {
			if p.Turn.ID != turn || p.Turn.Status != "completed" || !sawFinal {
				return fmt.Errorf("primary did not observe changed final on original turn")
			}
			return nil
		}
	}
}
