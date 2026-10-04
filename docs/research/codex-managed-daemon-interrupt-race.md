# 真实模型 interrupt 与审批竞争验收

2026-10-01，现有 managed daemon / CLI 0.159.3，默认真实模型
`gpt-6.1-sol` / `openai`。没有替换模型或 provider。每次使用新建的隔离
thread、独立 `/tmp` 工作目录；不启动或重启 daemon，不控制开发会话。
客户端为两个 Go probe，未模拟真人操作 CLI 的 Esc 或审批 UI。

## interrupt

probe interactive 新增：

```json
{"action":"interrupt","expectedTurnId":"<ATTACHED_ACTIVE_TURN_ID>"}
```

必须显式指定当前 attached thread 的 active turn；旧 turn ID、空 ID、其他
thread 被本地拒绝。发送 `turn/interrupt` 时参数为 `threadId`、`turnId`。
仍采用唯一事件 reader，主循环区分 steer/interrupt 的 RPC 响应与服务端审批
请求。`interruptAccepted` 是 RPC 的空对象确认，不代表任务已经停止；
最终依据匹配的 `turn/completed`、`status: interrupted`。
没有失败重试或自动创建新 turn，也没有把 detach 变成 interrupt。

真实模型启动一次 Python 计时打印命令，确认 live delta 后第二个客户端
resume 到原 active turn。旧 ID 被拒绝，正确 interrupt 获确认，两端均
收到原 turn interrupted。同 thread 随后成功完成新的无工具对话。

**边界：turn 中断并不终止已运行的 unifiedExec 后台终端。** 本次 Python
在 turn interrupted 后仍存活；测试随后显式调用
`thread/backgroundTerminals/terminate`，只传本次 thread 和它的 processId，
收到 `terminated: true`，核对实际 Python 进程停止。这一步是测试清理，
没有悄悄合并进 interactive interrupt。后续产品若需要“停止后台命令”，
应明确提供这项独立操作。

进程识别也区分三种 ID：unifiedExec `processId` 是 opaque handle；沙箱中
Python `os.getpid()` 是 namespace PID（此次为 2）；测试按唯一 cwd、Python
进程名和命令标记识别 host PID，并比较 `/proc/<pid>/stat` 的启动时间，
避免把 PID 复用或其他进程当成本次任务。

这一边界与固定版本源码吻合：

- [默认 Esc 映射](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/tui/src/keymap.rs)：interrupt_turn 默认按键为 Esc。
- [turn interrupt](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/app-server/src/request_processors/turn_processor.rs)：提交 `Op::Interrupt`。
- [后台进程保留](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/core/src/unified_exec/process_manager.rs)：在初始 yield 前保存进程，明确避免 turn interrupt 终止后台进程。
- [独立操作](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/core/src/session/handlers.rs)：Interrupt 与 CleanBackgroundTerminals 分开处理。

首次测试脚本只打印一次 namespace PID，启动输出未作为 live delta 发来，
因而未发出 interrupt；随后修正为每次 tick 带 PID。报告解码也改为每条
清空状态，避免上一条 rejection 残留。以上是验收脚本修正，不是 daemon
缺陷。最初“interrupt 应直接杀掉后台 Python”的断言不成立；最终验收
分别检查中断语义、后台进程边界、显式清理和恢复对话。

## 审批竞争

仲裁完全由 daemon 负责，客户端没有新增抢锁、投票或主端优先规则。
固定版本 [outgoing_message.rs](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/app-server/src/outgoing_message.rs)
在锁内 `remove_entry` 取走一次性审批回调；先处理的回复生效，后来的
回复找不到回调，只记录 warning，不再执行该决定。

新 thread 显式设置 `approvalPolicy: on-request`、`approvalsReviewer: user`、
`read-only` sandbox。真实模型请求 require_escalated，仅打印唯一标记。
第一端停在 pending approval，第二端 resume 收到同 ID 重放；两端都确认
本地 pending 后，通过同一 barrier 发回复，各连接保持一 reader、一 writer。
这是同一审批的并发响应，不是先看到 resolved 再发第二次响应。

两端都能报告发送成功，因为审批回复本身是 JSON-RPC response，服务端
不会再回复一个“你赢了/输了”的 RPC。用 resolved、最终 turn 状态和
命令结果判定实际采用的决定，不把 write success 当成 approval success。

测试覆盖：同时 accept/accept、同时 accept/cancel，以及两端仍持有旧
pending、仅延迟其中一端 100ms 的 accept-first / cancel-first。每端都
核对同审批 resolved、同 turn 完成、最多一次命令执行、本地旧 ID 再次
回复被拒绝；完成后 `thread/read` 成功，连接保持可用。

最初新 thread 继承了自动审批 reviewer，模型命令直接执行，没有产生
用户审批，不计入竞争验证。之后仅覆盖新测试 thread 的 reviewer 为
user；没有修改 daemon 配置或其他会话。

## 复现与证据

```sh
CC_CONNECT_REAL_CONTROLS=1 \
GOCACHE=/tmp/cc-connect-go-build GOMODCACHE=/tmp/cc-connect-go-mod \
go test ./tools/codex-daemon-probe -run '^TestRealDaemon(Interrupt|ApprovalRace)$' -count=1 -v
```

opt-in 测试使用真实模型，默认 `go test ./...` 跳过。完整事件日志保留在
各次输出的 `/tmp/cc-connect-real-controls-*/evidence.jsonl`；可迁移摘要见
[codex-managed-daemon-interrupt-race-result.json](codex-managed-daemon-interrupt-race-result.json)。
真人 CLI/Desktop UI、CC Connect/飞书入口仍待集成验收。
