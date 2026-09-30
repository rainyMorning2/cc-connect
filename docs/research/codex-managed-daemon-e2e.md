# Managed daemon 端到端验收

最新进度和换电脑继续入口：[接续记录](codex-managed-daemon-handoff.md)。

当前分支只有独立 probe，尚未把 daemon 接入 CC Connect agent 或消息平台。
`-self-test` 的成功不能算 Desktop/CLI → CC Connect → 消息平台的端到端成功。

## 2026-09-30 真实客户端实测：通过

用户创建了专用测试 thread `01a0f1a3-916d-78a0-9541-b00864547127`，
执行约三分钟的打印任务。用户确认启动路径为 Windows Terminal → WSL → Codex CLI。
daemon 的 resume 响应中 `source` 为 `vscode`，但此字段不能用于推断实际启动终端。
此前据此将测试客户端判定为 VS Code 是错误的，已更正。

Codex 0.159.2 的
[app-server 启动入口](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/cli/src/main.rs#L1188)
将 `SessionSource::VSCode` 直接传入服务端 runtime；ThreadManager 使用该 source，
thread 响应再从 runtime config 的 session_source 生成 `source` 字段。
因此 CLI 接入共享 daemon 后，也可能得到 `source: vscode`，并不表示它从 VS Code 启动。
本次记录为用户确认的 CLI → daemon → probe 实测，尚未验证 Desktop GUI。

- 第一次接入：快照中的 turn `01a0f1a3-c2f8-7eb1-9e96-0db64ee44c56`
  为 `inProgress`，随后实时收到 TICK 6、7、8、9。
- 第一次 probe 正常退出后重新连接：快照仍是同一 thread、同一活动 turn，
  收到 TICK 20 到 35，以及原客户端 agent 的等待进度消息。
- 收到 assistant 最终输出 `CLI-PROBE-E2E-DONE`，然后收到同一 turn
  的 `turn/completed`，status 为 `completed`，durationMs 为 `192209`。

期间 probe 只发送 initialize、initialized 和 thread/resume，未回复审批、
发送 steer/interrupt 或操作 daemon 生命周期。两次 probe 均已退出。
这是接入后的 live output，不只是恢复历史。重连期间未接收的 TICK 不被
声称为已重放；本次验证的是原任务继续、状态恢复及后续事件可见。

脱敏的结构化证据见
[codex-managed-daemon-real-client-result.json](codex-managed-daemon-real-client-result.json)。
真实客户端跨连接审批已完成下面的双端测试；真实客户端 steer、CC Connect
engine、实际消息平台的验收仍待进行。

## 第一层：真实 CLI/Desktop → probe

新建专用测试会话和空工作目录。不要使用正在承载开发工作的会话。
CLI 可以用 `codex --remote <endpoint>` 显式连接 daemon；本机 0.158.0 的 help
确认接受 `unix://PATH`。PATH 应来自 `codex app-server daemon version`
输出的 `socketPath`，不要手写固定路径。Desktop 则先确认其测试 thread
出现在这台机器同一 daemon 的 loaded thread 列表中。

在 worktree 根目录构建并查看已加载的 thread：

```sh
go build -o /tmp/cc-connect-daemon-probe ./tools/codex-daemon-probe
/tmp/cc-connect-daemon-probe -timeout 15s
```

如果只有工作目录而没有 thread ID，可用
`/tmp/cc-connect-daemon-probe -find-cwd <绝对工作目录> -timeout 30s`
查询匹配目录的 loaded thread IDs；此查询遍历所有页并只读取 metadata，不订阅 thread。

probe 当前只返回 loaded threads 第一页（最多 100 个）。超过一页时，
不能凭第一页没有目标 ID 就断定客户端连接到了不同 daemon。

在测试客户端启动持续约半分钟的任务，例如：

```text
这是独立验收任务。不要修改文件。
运行 python3 -u -c 'import time; [(print("E2E-TICK", i, flush=True), time.sleep(3)) for i in range(10)]'。
完成后回复 E2E-DONE。
```

只对该测试 thread 执行：

```sh
/tmp/cc-connect-daemon-probe -watch <测试-thread-id> -timeout 90s
```

验收必须观察到：resume 快照中的目标 thread ID、运行中的 turn ID，
以及接入之后的新事件。完成后要看到 `E2E-DONE` 的 assistant item 和
`turn/completed`。如果客户端配置将命令输出放在工具完成事件中而非逐段
outputDelta 中，记录实际事件类型，不把缺少逐秒输出误判成未订阅。

可以中途退出 watch，然后只重启 probe 并重新 watch。原始客户端的 turn
应继续运行；重连的快照应与它的实际活动状态一致。这个动作不重启 daemon。

默认 watch 只观察，不回复审批、不发送 steer/interrupt。self-test 已验证
这些 RPC 的路由机制，但原始客户端是 Go probe、模型是模拟端点。

### CLI 保持打开时的双客户端输出与审批（通过，CLI 展示行为已接受）

2026-09-30：专用 thread `01a0f1c3-8aa2-7b02-8459-70430367dc4d`、
turn `01a0f1c4-b748-72f0-86fc-368161bfdac4`，目录
`/tmp/cc-daemon-approval-e2e`。用户自行进入该目录启动 Codex CLI，并始终
保持 CLI 打开。probe 接入时快照为 inProgress/waitingOnApproval，收到
未解决审批 requestId 32；核对为专用打印命令后，从 probe 发送 accept。
随后收到同一请求的 `serverRequest/resolved`，用户确认“审批提示已消失，
任务继续执行”。probe 持续收到 `DUAL-E2E-TICK` 的 outputDelta 和 agent
进度消息，用户同时确认 CLI“能看到 TICK 标记”。

第一条任务最终收到 TICK 35、`DUAL-E2E-DONE` 和同一 turn 的 completed。
CLI 一直保持连接，没有退出再接入。

第二条 turn `01a0f1ca-04a8-7791-b26d-0da2cb6b4828`：probe 先收到
requestId 38，并通过 pending 查询确认该请求存在。用户随后在 CLI
选择允许一次，确认“已允许，任务继续执行”。probe 收到该请求的
`serverRequest/resolved`，以及命令完成事件（exitCode 0、输出
`DUAL-E2E-CLI-APPROVED`）和最终 `DUAL-E2E-SECOND-DONE`/completed。
再查询 pending 返回空列表，对旧 requestId 38 的 accept 被 probe 本地
拒绝，未发送到服务端。两条 turn 均成功结束后，probe 使用 detach 退出。
全程没有停止、重启 daemon，也没有控制其他 thread。

因此已实测确认 CLI 保持连接时可以双端观察，以及两端互相处理审批后
的状态同步。重复回复检查证明的是 probe 收到 resolved 后本地拒绝，
没有测试两端同时点击的服务端竞争行为，也未验证 Feishu 卡片。

用户随后提供更完整的 CLI 展示：Running 块及 show details 停在 TICK 11，
agent 进度消息继续提到 22、33，结束时又显示 Ran 块（末尾 33..35）。
此前“完整通过”的表述过宽：CLI 可见部分输出与最终结果已确认，但不能
声称原 Running 展示块一直同步刷新。

probe 记录中 TICK 1..35 都属于同一 command item
`exec-52a80605-f9a7-446c-8ba0-c747f191a2b3`，只有一次 command completed，
其 aggregatedOutput 包含 0..35。probe 只回复过一次该命令的审批，未发送
turn/start、turn/steer 或命令启动请求。没有重复执行的证据。

与 CLI 0.158.0 官方源码相吻合的解释：
[prepare_assistant_message](https://github.com/openai/codex/blob/rust-v0.158.0/codex-rs/tui/src/chatwidget/replay.rs#L93)
把当前活动块提交为历史；流式 assistant 消息会调用它。
[command_lifecycle](https://github.com/openai/codex/blob/rust-v0.158.0/codex-rs/tui/src/chatwidget/command_lifecycle.rs#L55)
只向当前 active ExecCell 追加输出；命令完成时若原块不再活动，则可用
aggregatedOutput 创建新块。第一条进度消息正好出现在 TICK 11 附近。
这属于源码支持的界面状态解释，没有采集 CLI 入站事件，因此尚未直接
证明 CLI 每条 delta 都已收到，也没有单客户端对照来排除多客户端影响。

用户随后确认该 CLI 展示现象可以接受，“不算缺口”，不作为本项目阻塞项；
上述观察仍保留，持续刷新原 Running 块不被列为已验证能力。

单纯重绘不会把已提交的命令块恢复为活动块。重新接入或重建快照能否
恢复持续展示仍待实测，不能当作已验证的绕过办法。CC Connect 正式实现
应按 threadId/itemId 保留命令状态，进度消息不结束命令，completed 事件
更新同一命令记录并以 aggregatedOutput 校对输出，避免复制这种展示问题。
脱敏记录见 [codex-managed-daemon-dual-client-result.json](codex-managed-daemon-dual-client-result.json)。

新增 `-watch <测试-thread-id> -interactive -timeout 15m` 模式。
stdin 每行接收一个 JSON 控制指令，不自动允许审批：

```json
{"action":"pending"}
{"action":"reply","requestId":42,"decision":"accept"}
{"action":"detach"}
```

requestId 必须取自该测试 thread 的实际 pending request，保留数字/字符串类型；
42 仅为示例。支持一次性 accept/decline，不能修改持久审批策略。
只处理目标 thread 的 commandExecution/fileChange 请求；收到
`serverRequest/resolved` 或对应 turn 完成后清除 pending，拒绝再次回复。
`approvalReplySent` 只表示发送成功，必须结合 resolved、命令结果和 CLI
界面确认审批已生效。detach 只断开 probe 的连接。

实际验收时，用户保持专用 CLI 打开：第一条审批从 probe 允许，确认 CLI
提示关闭且两边收到标记输出；第二条审批由 CLI 允许，确认 probe 收到
resolved 并拒绝旧 requestId。CLI 的实际界面状态由用户报告，不能仅凭
服务端事件声称界面已同步。此验收仍未覆盖 Feishu 卡片或完整 CC Connect 链路。

## 第二层：真实 daemon → CC Connect engine → 模拟平台

需要先实现显式 daemon 模式和 attach 能力。使用真实 agent 与 Engine，
只模拟平台发送边界；经 `ReceiveMessage` 驱动用户动作，从平台收到的
内容作断言。现有 `tests/blackbox/helper` 提供这种基础设施，但默认
Codex 测试没有 daemon/attach 配置，不能直接证明本功能。

CUJ 至少覆盖以下多步行为：

| 场景 | 用户动作和必须观察到的结果 |
| --- | --- |
| 中途接入 | 外部客户端启动专用 turn → 平台 attach → 平台看到后续输出，无需先发送新 prompt |
| 平台审批 | 外部客户端产生审批 → 平台收到卡片 → 平台拒绝/允许 → 原客户端同步更新，无重复有效卡片 |
| 外部审批 | 平台收到卡片 → 原客户端回答 → 平台清除已解决状态，不能重复回答 |
| steer | 外部 turn 运行 → 平台 steer → 后续行为按新指示执行，保持同一 thread 和 turn ID |
| detach/reconnect | 平台 detach → 外部任务继续 → 平台重新 attach → 恢复活动状态和未解决审批 |
| interrupt | 外部 turn 运行 → 平台 interrupt → 两端均显示 interrupted |
| 隔离 | 两个专用 thread 同时运行 → 只 attach 一个 → 输出和审批不串到另一 thread |

stdio 原有 CUJ 同时保持通过。daemon 不运行时必须报连接失败，不能静默
启动独立 app-server，从而给出虚假的“共享成功”。

## 第三层：CLI/Desktop → CC Connect → 实际消息平台

用独立测试聊天和隔离的 CC Connect 配置执行第二层中的所有用户动作。
确认卡片可操作、streaming/完成输出可见、其他客户端审批后 UI 不残留，
以及 detach 只断开 CC 客户端而没有停止原任务。实际平台与测试聊天
需要由用户指定；不要使用现有生产会话作控制测试。

每项验收记录：客户端/daemon/CC 版本、thread ID、turn ID、动作时间、
平台实际可见结果，以及对应 RPC 成功或失败。仅历史列表可见、仅 RPC
返回成功、或只有 probe 测试通过，都不能代替这个最终用户视角的验收。
