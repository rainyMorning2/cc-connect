# Codex managed daemon：共享运行状态可行性验证

日期：2026-09-30。分支：`research/codex-managed-daemon`。基于 CC Connect
`dfad1941`；源码核对使用 Codex `rust-v0.159.2`，commit
`ff6aec96948b70d94983af2641a6b67c94faeff5`，而非浮动的 `main`。

换电脑继续请先读 [接续记录与验收清单](codex-managed-daemon-handoff.md)。

## 结论与边界

**Go：当前 daemon 支持独立客户端共享同一运行中 thread 的状态与控制。**
本机真实 daemon 的双客户端测试已经验证 live output、待审批请求重放与回复、
active turn 恢复、cross-client steer、观察者重连和 cross-client interrupt。
steer 不仅返回成功，其文本还实际出现在后续模型请求的 input 中。

这证明的是 daemon 的跨客户端运行机制。测试的原始客户端是独立 Go probe，
不是 Desktop GUI，因此不能把本次结果写成“已完成 Desktop → 飞书端到端测试”。
正式接入还要确认目标 Desktop/CLI 的 thread 出现在**同一个 daemon 的
`thread/loaded/list`** 中。共享 CODEX_HOME 或看到磁盘历史不能证明共享内存中的 turn。

随后用户创建真实客户端测试会话，probe 已验证中途接入、实时输出、断线重连和
最终完成事件。该 thread 的协议 source 是 `vscode`，具体证据见
[端到端验收记录](codex-managed-daemon-e2e.md)。尚未验证 CC Connect 或消息平台链路。

本阶段仅新增独立 probe 和测试；没有修改 `agent/codex`、`core`、默认配置或 stdio backend。
按照先验证再集成的实施顺序，生产 backend 和消息平台上的 attach 体验仍待实现。

## 本机实测

被动发现命令为 `codex app-server daemon version`。实际报告：

```json
{"status":"running","backend":"pid","cliVersion":"0.158.0","appServerVersion":"0.159.2"}
```

socket 使用该命令返回的 `socketPath`，没有硬编码。客户端在 Unix socket 上向
`ws://localhost/rpc` 发起 WebSocket handshake，然后发送 JSON-RPC text frames。
该 URL 仅作为握手 URI，没有 TCP listener 或 JSONL↔WS adapter。

| 验证项 | 实测结果 |
| --- | --- |
| discovery → UDS WebSocket → initialize | 通过；默认 probe 返回 loaded thread IDs |
| A 创建 turn，B 中途 resume | 通过；resume 快照中的 inProgress turn ID 与 A 一致 |
| B 收到 A 的 pending approval | 通过；request ID 保持一致 |
| B steer A 的活动 turn | 通过；返回原 turn ID，标记文本进入后续模型 input |
| B 断线，C 重新 resume | 通过；原 turn 和待审批请求保持原 ID |
| C 回复 approval | 通过；C 回复 decline，A 收到 serverRequest/resolved |
| C 观察后续 assistant output | 通过；收到指定 assistant item 和 turn/completed |
| C interrupt A 后续新建的活动 turn | 通过；收到 interrupted 的 turn/completed |

测试 thread：`01a0f17c-a91c-7c02-b063-38e2b7069625`。
工作目录：`/tmp/cc-connect-daemon-probe-2066695530`。
该诊断 thread 和空工作目录保留，方便检查记录。其模型端点只在 self-test
进程存活期间存在；不要将此 thread 用作后续真实对话。

所有控制 RPC 的 thread ID 只能来自 self-test 自己的 `thread/start` 响应。
没有对已有 thread 发送 resume、approval 回复、steer、interrupt 或 archive。
没有执行 daemon start、stop、restart、update、bootstrap 或修改 daemon 设置。
关闭客户端仅关闭该客户端的 WebSocket。

模拟模型是在本机 loopback 上运行的 Responses SSE fixture，作为新测试 thread
的配置覆盖，不写全局配置，不调用真实模型，不消耗推理额度。
fixture 请求的 shell approval 被拒绝，没有执行该命令。
第三次模拟模型请求保持打开，用于确定性验证 interrupt。

## 源码核对

1. [daemon README](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server-daemon/README.md)
   和 [lifecycle 实现](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server-daemon/src/lib.rs)：
   生命周期仍为 experimental；`version` 是被动探测，JSON 可用于发现和版本核对。
   shared clients 使用 daemon 启动时继承的 environment。
2. [remote client](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server-client/src/remote.rs)：
   Unix socket 连接随后执行 WebSocket handshake，URI 为 `ws://localhost/rpc`。
   [服务端 transport](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server-transport/src/transport/unix_socket.rs)
   同样进行 WebSocket upgrade。
3. [thread listener 生命周期](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server/src/request_processors/thread_lifecycle.rs)：
   running-thread resume 经 listener 返回活动 turn 快照、注册订阅，并重放 pending server requests。
   [官方 resume 测试](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server/tests/suite/v2/thread_resume.rs)
   覆盖 pending command/file approval replay；这些测试本身不等于跨客户端验证。
4. [turn processor](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server/src/request_processors/turn_processor.rs)：
   steer 加载 thread，校验 `expectedTurnId` 并调用 `steer_turn`。
   [直接输入限制](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server/src/request_processors/thread_input.rs)
   明确禁止直接控制 multi-agent v2 sub-agent，不应对所有 thread 都显示可 steer。
5. [审批响应路由](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/app-server/src/outgoing_message.rs)：
   普通 approval 可以由另一连接回复；user verification 有专门的 connection ownership 校验，
   不能把普通审批的结论推广到登录或身份验证请求。

官方 [App Server 文档](https://learn.chatgpt.com/docs/app-server) 可用于了解 RPC 生命周期。
特定版本的运行语义以以上 pinned 源码及本机实测为依据。

## 正式集成方案的必要补充

保留 `backend = "app_server"` 和 `app_server_url = "stdio"`。
新增显式 `app_server_mode = "daemon"`，默认仍为 stdio。
daemon 模式只做被动 discovery 和连接；daemon 不运行时明确报错，不自动启动，
也不静默回退到独立 app-server，否则用户会误以为已接入原任务。

传输接入可以保持现有 request/response dispatcher；只让 JSON message 在 stdio
模式写入 JSONL，在 daemon 模式写入 WS text frame。关闭和超时处理必须区分连接
与子进程所有权，daemon 模式不能进入现有的 Process.Kill 路径。

**仅替换 transport 不足以完成用户体验：**

- `ensureThread` 需要 attach 分支：对外部 running thread 不传 cwd、model、sandbox、
  approval policy 等覆盖项；从 resume 快照恢复 active turn ID。现有响应结构只读取 thread ID。
- `handleNotification` 和 server-request handler 需要 thread ID 过滤；共享连接上的无关
  status/notification 不能清空当前 turn 或污染输出。
- 处理 `serverRequest/resolved`，及时清除其他客户端已经解决的审批卡片和本地等待状态。
  用户不能对已经解决的请求再次操作。
- `Send` 现为 `turn/start`；attached active turn 应显式提供 steer，使用 `expectedTurnId`。
  失配/turn 已结束时反馈实际结果，不能偷偷改成新 turn。
- `Close` 表示 detach，interrupt 是单独的 turn RPC；重连应重新 initialize + resume
  并恢复快照，不应自动重发可能已经执行过的控制请求。
- 当前 `/switch` 只设置 session ID，尚未创建 session 或开始订阅。只加 daemon 配置
  不会让用户在 `/switch` 后立即看见输出。需要通过可选能力接口实现 attach，
  并把接入后的事件交给 engine reader，不向 core 硬编码 Codex。
- 附加 session 的 `EventResult`、pending approval、idle timer 和 unsolicited reader
  的交接需要 CUJ：attach → 外部输出 → 审批/steer → detach/reconnect。
  不能在 foreground Send 前 drain 掉刚重放的有效审批或活动事件。

建议每个 CC session 第一版使用独立 daemon WS 连接，减少跨 thread dispatcher 改动。
先支持 root thread 和基本 approval；dynamic tool、attestation/verification、跨系统
附件路径和 full pagination 作为明确的后续兼容项目。Windows UDS 不属于本次验证。

## 复现

在该 worktree 根目录执行：

```sh
go env GOMODCACHE
go build -o /tmp/cc-connect-daemon-probe ./tools/codex-daemon-probe
/tmp/cc-connect-daemon-probe -timeout 15s
/tmp/cc-connect-daemon-probe -self-test -timeout 60s
go test -race ./tools/codex-daemon-probe -count=1
go vet ./tools/codex-daemon-probe
```

默认模式只列出 loaded thread IDs（单页，最多 100 条），不订阅任何 thread。
`-self-test` 会创建独立诊断 thread，执行上表中的跨客户端验证。
`-watch <thread-id>` 显式 resume 并打印快照和后续事件，不自动回答任何审批，
同时拒绝订阅环境变量 `CODEX_THREAD_ID` 指向的当前 agent thread。
不要对当前工作 thread 使用 watch。

probe 是顺序读写的验证工具，带有有界事件队列、消息大小限制及总超时，
不具备生产 backend 的并发、多 session 路由或自动重连机制。

Go 缓存最初没有自动生效的原因：本机 `.bashrc` 在非交互 shell 提前 return，
而 `export GOMODCACHE=...` 位于 return 后。用户将 export 移到 return 之前后，
已经用新的非交互 shell 验证：不显式传入变量时，`go env GOMODCACHE` 自动返回
`/home/daizhen/.cache/go-mod`。后续检查使用该路径；没有修改 Go 全局配置或删除旧缓存。

之后按用户的缓存清理请求，另行将 Go 用户配置的 GOMODCACHE 固定到该路径，
逐文件确认旧模块缓存完全重复后清理了 `/home/daizhen/go/pkg/mod`。
早期 probe 检查曾使用 `GOTOOLCHAIN=local`。用户后来明确要求去掉这个设置；
最新 build/test/vet 使用默认 `GOTOOLCHAIN=auto`，不显式传入该变量。

## 检查结果

- `go build ./...`：通过。
- `go test ./...`：通过，包括原有 Codex agent 和 core 测试。
- `go test -race ./tools/codex-daemon-probe -count=1`：通过。
- `go vet ./tools/codex-daemon-probe`：通过。
- 本机 daemon self-test：上表所有检查通过。

新 worktree 初次全仓检查因缺少 `web/dist` 失败；安装锁定的前端依赖并通过
仓库现有的 `npm run build` 生成资源后，构建及全仓测试通过。前端源码和 lockfile
没有变更。早期另一次并行全仓测试遇到原有 `TestCmdCronExec_TriggersJob/exec`
临时目录清理竞态（directory not empty）；最终全仓检查通过，未修改该测试或 core。
