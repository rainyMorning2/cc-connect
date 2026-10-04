package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// These opt-in tests use the existing daemon and its default real model. They
// control only fresh isolated threads, and retain raw evidence under /tmp.
type realControlRun struct {
	ctx     context.Context
	socket  string
	cwd     string
	thread  string
	primary *client
	log     *json.Encoder
	mu      sync.Mutex
}

func newRealControlRun(t *testing.T, policy string) *realControlRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	info, err := discover(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.MkdirTemp("", "cc-connect-real-controls-")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(cwd + "/evidence.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	c, err := connect(ctx, info.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	stop := context.AfterFunc(ctx, c.close)
	t.Cleanup(func() { stop() })
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model    string `json:"model"`
		Provider string `json:"modelProvider"`
	}
	if err := c.request(ctx, "thread/start", map[string]any{"cwd": cwd, "approvalPolicy": policy, "approvalsReviewer": "user", "sandbox": "read-only"}, &started); err != nil {
		t.Fatal(err)
	}
	if started.Thread.ID == "" || started.Thread.ID == os.Getenv("CODEX_THREAD_ID") || strings.Contains(started.Provider, "probe") {
		t.Fatal("not a fresh real-model thread")
	}
	r := &realControlRun{ctx: ctx, socket: info.SocketPath, cwd: cwd, thread: started.Thread.ID, primary: c, log: json.NewEncoder(file)}
	r.record(t, map[string]any{"threadId": r.thread, "model": started.Model, "modelProvider": started.Provider, "providerOverride": false, "approvalsReviewer": "user", "approvalPolicy": policy, "appServerVersion": info.AppServerVersion, "cwd": cwd})
	t.Logf("real model=%s provider=%s thread=%s evidence=%s/evidence.jsonl", started.Model, started.Provider, r.thread, cwd)
	return r
}

func (r *realControlRun) record(t *testing.T, v any) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.log.Encode(v); err != nil {
		t.Error(err)
	}
}

func (r *realControlRun) start(t *testing.T, prompt string) string {
	t.Helper()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := r.primary.request(r.ctx, "turn/start", map[string]any{"threadId": r.thread, "input": input(prompt), "effort": "low"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Turn.ID == "" {
		t.Fatal("missing turn ID")
	}
	return result.Turn.ID
}

func (r *realControlRun) attach(t *testing.T) (*client, threadSnapshot) {
	t.Helper()
	c, err := connect(r.ctx, r.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	stop := context.AfterFunc(r.ctx, c.close)
	t.Cleanup(func() { stop() })
	var snapshot threadSnapshot
	if err := c.request(r.ctx, "thread/resume", map[string]any{"threadId": r.thread}, &snapshot); err != nil {
		t.Fatal(err)
	}
	return c, snapshot
}

type controlEvent struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	RequestID json.RawMessage `json:"requestId"`
	Delta     string          `json:"delta"`
	Item      struct {
		ID        string `json:"id"`
		ProcessID string `json:"processId"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		ExitCode  *int   `json:"exitCode"`
		Output    string `json:"aggregatedOutput"`
	} `json:"item"`
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

func (r *realControlRun) next(c *client, side string) (message, controlEvent, error) {
	m, err := c.next(r.ctx)
	var p controlEvent
	if err != nil {
		return m, p, err
	}
	if len(m.Params) > 0 {
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return m, p, err
		}
	}
	r.mu.Lock()
	err = r.log.Encode(map[string]any{"at": time.Now().UTC(), "side": side, "message": m})
	r.mu.Unlock()
	return m, p, err
}

func TestRealDaemonInterrupt(t *testing.T) {
	if os.Getenv("CC_CONNECT_REAL_CONTROLS") != "1" {
		t.Skip("set CC_CONNECT_REAL_CONTROLS=1 for real model interrupt and races")
	}
	r := newRealControlRun(t, "never")
	turn := r.start(t, `这是隔离 interrupt 验收。不要修改文件、联网或读取配置。只运行一次 exec_command，yield_time_ms=1000：
python3 -u -c 'import os,time; [(print("REAL-INTERRUPT-PID", os.getpid(), "REAL-INTERRUPT-TICK", i, flush=True), time.sleep(1)) for i in range(90)]'
然后轮询同一个进程，直到结束，不要重跑。`)
	pid := 0
	processID := ""
	t.Cleanup(func() {
		if processID != "" {
			realCleanupTerminal(t, r, processID)
		}
	})
	output := ""
	for pid == 0 {
		m, p, err := r.next(r.primary, "primary-before-interrupt")
		if err != nil {
			t.Fatal(err)
		}
		if p.ThreadID != r.thread {
			continue
		}
		if m.Method == "item/started" && p.Item.Type == "commandExecution" {
			processID = p.Item.ProcessID
		}
		if m.Method == "item/commandExecution/outputDelta" {
			output += p.Delta
			fields := strings.Fields(output)
			for i, s := range fields {
				if s == "REAL-INTERRUPT-PID" && i+1 < len(fields) {
					pid, _ = strconv.Atoi(fields[i+1])
				}
			}
		}
		if m.Method == "turn/completed" || strings.HasSuffix(m.Method, "requestApproval") {
			t.Fatal("no live command before interrupt")
		}
	}
	namespacePID := pid
	pid, err := realPythonHostPID(r.cwd)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual Python OS pid=%d", pid)
	observer, snapshot := r.attach(t)
	if activeTurn(snapshot) != turn {
		t.Fatal("original turn missing from resume")
	}
	if err := realInteractiveInterrupt(r, t, observer, snapshot, turn); err != nil {
		t.Fatal(err)
	}
	for {
		m, p, err := r.next(r.primary, "primary-after-interrupt")
		if err != nil {
			t.Fatal(err)
		}
		if p.ThreadID == r.thread && m.Method == "turn/completed" {
			if p.Turn.ID != turn || p.Turn.Status != "interrupted" {
				t.Fatalf("unexpected primary completion: %+v", p.Turn)
			}
			break
		}
	}
	// Interrupt aborts the agent turn; unifiedExec intentionally keeps background
	// terminals alive. Observe this boundary, then explicitly terminate only ours.
	stopped, err := realProcessStopped(pid, identity, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r.record(t, map[string]any{"result": "interrupt", "turnId": turn, "status": "interrupted", "pythonOSPid": pid, "pythonNamespacePid": namespacePID, "processStoppedByInterrupt": stopped, "processId": processID})
	if !stopped {
		var terminated struct {
			Terminated bool `json:"terminated"`
		}
		if processID == "" {
			t.Fatal("missing owned background terminal handle")
		}
		if err := r.primary.request(r.ctx, "thread/backgroundTerminals/terminate", map[string]any{"threadId": r.thread, "processId": processID}, &terminated); err != nil {
			t.Fatal(err)
		}
		cleaned, err := realProcessStopped(pid, identity, 5*time.Second)
		r.record(t, map[string]any{"result": "backgroundTerminalCleanup", "processId": processID, "terminated": terminated.Terminated, "processStopped": cleaned})
		if err != nil || !terminated.Terminated || !cleaned {
			t.Fatalf("test terminal cleanup failed: terminated=%v stopped=%v err=%v", terminated.Terminated, cleaned, err)
		}
	}
	// Same thread remains usable after interrupt; never fall back by creating a turn automatically.
	next := r.start(t, "只回复 INTERRUPT-RECOVERY-OK，不调用任何工具。")
	for {
		m, p, err := r.next(r.primary, "primary-recovery")
		if err != nil {
			t.Fatal(err)
		}
		if p.ThreadID == r.thread && m.Method == "turn/completed" {
			if p.Turn.ID != next || p.Turn.Status != "completed" {
				t.Fatal("thread unusable after interrupt")
			}
			break
		}
	}
	t.Logf("PASS both clients interrupted same turn=%s Python stoppedByInterrupt=%v explicit cleanup verified; recovery turn=%s", turn, stopped, next)
}

func realInteractiveInterrupt(r *realControlRun, t *testing.T, c *client, snapshot threadSnapshot, turn string) error {
	defer c.close()
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	stop := context.AfterFunc(r.ctx, func() { c.close(); _ = outR.Close(); _ = inR.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() {
		defer outW.Close()
		done <- watchInteractive(r.ctx, c, r.thread, json.NewEncoder(outW), inR, snapshot)
	}()
	commands := json.NewEncoder(inW)
	// A stale turn must be rejected locally before the valid interrupt is sent.
	if err := commands.Encode(interactiveControl{Action: "interrupt", ExpectedTurnID: "stale-test-turn"}); err != nil {
		return err
	}
	decoder := json.NewDecoder(outR)
	var report map[string]json.RawMessage
	report = nil
	if err := decoder.Decode(&report); err != nil {
		return err
	}
	var kind string
	kind = ""
	_ = json.Unmarshal(report["probe"], &kind)
	// Resume may have queued events, so wait for the explicit local rejection.
	for kind != "controlRejected" {
		r.record(t, map[string]any{"side": "observer", "report": report})
		report = nil
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		kind = ""
		_ = json.Unmarshal(report["probe"], &kind)
	}
	r.record(t, map[string]any{"side": "observer-stale-rejected", "report": report})
	if err := commands.Encode(interactiveControl{Action: "interrupt", ExpectedTurnID: turn}); err != nil {
		return err
	}
	accepted, completed := false, false
	for !accepted || !completed {
		report = nil
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		r.record(t, map[string]any{"side": "observer", "report": report})
		kind = ""
		_ = json.Unmarshal(report["probe"], &kind)
		if kind == "interruptRejected" || kind == "controlRejected" {
			return fmt.Errorf("interrupt rejected: %s", report["reason"])
		}
		if kind == "interruptAccepted" {
			accepted = true
		}
		var method string
		_ = json.Unmarshal(report["method"], &method)
		var p controlEvent
		_ = json.Unmarshal(report["params"], &p)
		if method == "turn/completed" && p.ThreadID == r.thread {
			if p.Turn.ID != turn || p.Turn.Status != "interrupted" {
				return fmt.Errorf("wrong interrupted turn")
			}
			completed = true
		}
	}
	if err := commands.Encode(interactiveControl{Action: "detach"}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func TestRealDaemonApprovalRace(t *testing.T) {
	if os.Getenv("CC_CONNECT_REAL_CONTROLS") != "1" {
		t.Skip("set CC_CONNECT_REAL_CONTROLS=1 for real model interrupt and races")
	}
	for _, tc := range []struct {
		name, a, b     string
		delayA, delayB time.Duration
	}{
		{"bothAccept", "accept", "accept", 0, 0},
		{"conflictSimultaneous", "accept", "cancel", 0, 0},
		{"acceptFirst", "accept", "cancel", 0, 100 * time.Millisecond},
		{"cancelFirst", "accept", "cancel", 100 * time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) { realApprovalRace(t, tc.a, tc.b, tc.delayA, tc.delayB) })
	}
}

func realApprovalRace(t *testing.T, a, b string, delayA, delayB time.Duration) {
	r := newRealControlRun(t, "on-request")
	marker := fmt.Sprintf("RACE-EXECUTED-%d", time.Now().UnixNano())
	turn := r.start(t, fmt.Sprintf(`这是隔离审批竞争验收。不要读取文件、修改文件或联网。只发起一次 exec_command：
python3 -c 'print("%s")'
必须设置 sandbox_permissions="require_escalated"，justification="隔离审批竞争验收：只打印测试标记"，yield_time_ms=1000。
如果审批取消或拒绝，不要重试、不要改用其他工具执行。完成后简短回复。`, marker))
	clients := []*client{r.primary, nil}
	trackers := []*approvalTracker{{thread: r.thread, pending: map[string]pendingApproval{}}, {thread: r.thread, pending: map[string]pendingApproval{}}}
	ids := make([]json.RawMessage, 2)
	for i, c := range clients {
		if i == 1 {
			c, _ = r.attach(t)
			clients[i] = c
		}
		ids[i] = r.waitApproval(t, c, trackers[i], i)
	}
	if string(ids[0]) != string(ids[1]) {
		t.Fatal("clients saw different approval IDs")
	}
	gate := make(chan struct{})
	results := make(chan error, 2)
	for i, c := range clients {
		decision, delay := a, delayA
		if i == 1 {
			decision, delay = b, delayB
		}
		go func(i int, c *client, decision string, delay time.Duration) {
			<-gate
			if delay > 0 {
				time.Sleep(delay)
			}
			start := time.Now()
			err := trackers[i].reply(interactiveControl{Action: "reply", RequestID: ids[i], Decision: decision}, func(v any) error { return c.write(r.ctx, v) })
			r.record(t, map[string]any{"side": i, "decision": decision, "requestId": ids[i], "sendStarted": start.UTC(), "sendEnded": time.Now().UTC(), "writeOK": err == nil})
			results <- err
		}(i, c, decision, delay)
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	outcomes := make(chan realRaceOutcome, 2)
	for i, c := range clients {
		go func(i int, c *client) { outcomes <- r.raceOutcome(c, trackers[i], ids[i], turn, marker, i) }(i, c)
	}
	first, second := <-outcomes, <-outcomes
	for _, o := range []realRaceOutcome{first, second} {
		if o.err != nil {
			t.Fatal(o.err)
		}
		if !o.resolved || o.commands > 1 || o.executions > 1 {
			t.Fatalf("approval resolution or duplicate execution failure: %+v", o)
		}
		if (o.status == "completed" && o.executions != 1) || (o.status == "interrupted" && o.executions != 0) || (o.status != "completed" && o.status != "interrupted") {
			t.Fatalf("decision and execution disagree: %+v", o)
		}
	}
	if first.status != second.status || first.executions != second.executions {
		t.Fatal("clients disagree on final outcome")
	}
	if delayB > 0 && first.status != "completed" {
		t.Fatal("accept-first decision did not win")
	}
	if delayA > 0 && first.status != "interrupted" {
		t.Fatal("cancel-first decision did not win")
	}
	for i, c := range clients {
		if len(trackers[i].pending) != 0 {
			t.Fatal("pending approval not cleared")
		}
		if err := trackers[i].reply(interactiveControl{RequestID: ids[i], Decision: "accept"}, func(any) error { t.Error("stale reply reached wire"); return nil }); err == nil {
			t.Fatal("stale local response accepted")
		}
		var read json.RawMessage
		if err := c.request(r.ctx, "thread/read", map[string]any{"threadId": r.thread, "includeTurns": true}, &read); err != nil {
			t.Fatalf("connection broken by duplicate response: %v", err)
		}
	}
	r.record(t, map[string]any{"result": "approvalRace", "turnId": turn, "requestId": ids[0], "decisionA": a, "decisionB": b, "status": first.status, "executions": first.executions, "bothResolved": first.resolved && second.resolved, "connectionsReusable": true})
	t.Logf("PASS decisions=%s/%s same request=%s status=%s executions=%d both resolved; connections reusable", a, b, ids[0], first.status, first.executions)
}

// Sandboxed Python prints its PID within its own PID namespace (often 2).
// Resolve the host PID by the unique test cwd and actual Python executable;
// neither the namespace PID nor unifiedExec processId is an OS host PID.
func realPythonHostPID(cwd string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	found := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		base := "/proc/" + entry.Name()
		comm, err := os.ReadFile(base + "/comm")
		if err != nil || strings.TrimSpace(string(comm)) != "python3" {
			continue
		}
		dir, err := os.Readlink(base + "/cwd")
		if err != nil || dir != cwd {
			continue
		}
		cmd, err := os.ReadFile(base + "/cmdline")
		if err != nil || !strings.Contains(string(cmd), "REAL-INTERRUPT-PID") {
			continue
		}
		if found != 0 {
			return 0, fmt.Errorf("multiple Python test processes in %s", cwd)
		}
		found = pid
	}
	if found == 0 {
		return 0, fmt.Errorf("no live Python process in test cwd %s", cwd)
	}
	return found, nil
}

func realProcessStopped(pid int, identity []byte, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		now, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		beforeFields, nowFields := strings.Fields(string(identity)), strings.Fields(string(now))
		if len(nowFields) > 21 && len(beforeFields) > 21 && (nowFields[21] != beforeFields[21] || nowFields[2] == "Z") {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, nil
}

func realCleanupTerminal(t *testing.T, r *realControlRun, processID string) {
	t.Helper()
	c, err := connect(r.ctx, r.socket)
	if err != nil {
		t.Errorf("connect for owned test terminal cleanup: %v", err)
		return
	}
	defer c.close()
	var result struct {
		Terminated bool `json:"terminated"`
	}
	if err := c.request(r.ctx, "thread/backgroundTerminals/terminate", map[string]any{"threadId": r.thread, "processId": processID}, &result); err != nil {
		t.Errorf("cleanup owned test terminal: %v", err)
	}
	r.record(t, map[string]any{"result": "cleanupOnExit", "processId": processID, "terminated": result.Terminated})
}

type realRaceOutcome struct {
	status     string
	commands   int
	executions int
	resolved   bool
	err        error
}

func (r *realControlRun) raceOutcome(c *client, tracker *approvalTracker, id json.RawMessage, turn, marker string, side int) realRaceOutcome {
	o := realRaceOutcome{}
	for {
		m, p, err := r.next(c, fmt.Sprintf("side-%d-after-race", side))
		if err != nil {
			o.err = err
			return o
		}
		if err := tracker.observe(m); err != nil {
			o.err = err
			return o
		}
		if p.ThreadID != r.thread {
			continue
		}
		if m.Method == "serverRequest/resolved" && string(p.RequestID) == string(id) {
			o.resolved = true
		}
		if m.Method == "item/completed" && p.Item.Type == "commandExecution" {
			o.commands++
			if p.Item.ExitCode != nil && *p.Item.ExitCode == 0 && strings.TrimSpace(p.Item.Output) == marker {
				o.executions++
			}
		}
		if m.Method == "turn/completed" {
			if p.Turn.ID != turn {
				o.err = fmt.Errorf("wrong turn completion")
			}
			o.status = p.Turn.Status
			return o
		}
	}
}

func (r *realControlRun) waitApproval(t *testing.T, c *client, tracker *approvalTracker, side int) json.RawMessage {
	t.Helper()
	for {
		m, p, err := r.next(c, fmt.Sprintf("side-%d-before-race", side))
		if err != nil {
			t.Fatal(err)
		}
		if err := tracker.observe(m); err != nil {
			t.Fatal(err)
		}
		if p.ThreadID == r.thread && m.Method == "item/commandExecution/requestApproval" {
			return append(json.RawMessage(nil), m.ID...)
		}
		if p.ThreadID == r.thread && m.Method == "turn/completed" {
			t.Fatal("real model did not request approval")
		}
	}
}
