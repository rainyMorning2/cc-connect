# Managed daemon interactive steer

日期：2026-10-01。范围：补齐独立 probe 的 steer 入口，为真实 CLI 验收与
后续 CC Connect daemon 集成准备。当前没有修改生产 agent/core。

最新：用户要求使用真实模型后，已完成 **真实 daemon + 真实模型 + 双 probe**
验收。后续进度和最终回复实际遵从 steer，见下方真实模型记录；原有 fixture
结果保留其原始范围。真人 CLI/Desktop UI 仍未在本轮测试。

## 实现

官方 [App Server steer 文档](https://learn.chatgpt.com/docs/app-server#steer-an-active-turn)
要求 expectedTurnId 匹配当前活动 turn，没有活动 turn 时请求失败；steer
不产生新的 turn/started，也不接受 model/cwd/sandbox 等 turn 覆盖项。
版本语义对照 `rust-v0.159.3` 的 `turn_processor.rs` 和 `thread_input.rs`。

- interactive 从 resume 快照恢复 active turn；target thread 的 turn/started
  和 turn/completed 更新状态。无关 thread 与旧 turn 完成不会清除当前状态。
- 第一版限 root thread；subagent source 或 canAcceptDirectInput=false 禁用
  steer。canAcceptDirectInput 缺省时仍由服务端做最终能力校验。
- 命令只能作用于 watch 指定的 thread；可选 threadId 不匹配时拒绝。
  必须显式给 expectedTurnId 和非空 text，不能静默选择其他 turn。
- 同一连接由原事件 goroutine 单独读取，主循环单独写入并匹配客户端 RPC
  响应。steer 不调用同步 request()，因此不会与后台 reader 争抢 WS 消息。
- 服务端 approval/requestUserInput 的 ID 可能与客户端 RPC 数字 ID 相同；
  匹配响应要求 method 为空，审批与问答继续正常跟踪。
- `steerRequestSent` 仅表示写入成功；`steerAccepted` 要求服务端响应明确
  返回原 expectedTurnId。RPC 错误或异常结果为 steerRejected，本地校验失败
  为 controlRejected，不自动重发或转成 turn/start。
- outstanding steer 有 16 条上限。断线/超时可能无法确认结果，重新接入后
  应核对状态，不能自动重发。detach 只关 probe，不撤回已经接受的 steer。

## 使用

```sh
go build -o /tmp/cc-connect-daemon-probe ./tools/codex-daemon-probe
/tmp/cc-connect-daemon-probe -watch <专用测试-thread-id> -interactive -timeout 15m
```

从 resume 或 status 取得真实活动 turn ID，每行输入一个 JSON：

```json
{"action":"status"}
{"action":"steer","expectedTurnId":"<活动-turn-id>","text":"后续进度回复以 STEER-SEEN 开头，最终回复 STEER-E2E-DONE；不要重跑当前命令。"}
{"action":"detach"}
```

status 返回 threadId、activeTurnId 和 canSteer。steer 并不修改/杀掉已经运行
的 OS 命令，验收应检查模型后续处理。interactive 尚未提供 interrupt action。

## 验证

扩展原 `-self-test`，通过新的 stdin/单 reader 路径发送 steer，而非只验证
底层 RPC。真实 daemon 版本 `0.159.3`，新建隔离 root thread，本机模拟
模型；未控制已有 thread、未操作 daemon 生命周期、未执行待审批命令。

| 检查 | 结果 |
| --- | --- |
| resume 恢复原活动 turn | 通过 |
| interactive 拒绝错误 expectedTurnId | 通过，本地拒绝且未发控制 RPC |
| 正确 steer 的 sent → accepted | 通过，服务端返回原 turn ID |
| 文本到达后续模型 input | 通过；被拒绝的 marker 未进入 input |
| 活动 turn 不匹配的服务端校验 | 通过，RPC -32600 expected active turn id mismatch |
| turn 完成后的服务端校验 | 通过，RPC -32600 no active turn to steer |
| 原有审批、重连、cancel、interrupt self-test | 通过 |

证据：[steer-result.json](codex-managed-daemon-steer-result.json)。本地 WS
测试另覆盖同 ID 审批插入、RPC 错误不触发重试/新 turn、thread/旧 turn 隔离、
能力限制、异常响应、取消的写入和有界 outstanding 请求。

这是 **真实 daemon + 双 probe + 模拟模型** 验证。daemon/runtime/transport
与用户所用环境相同，但不能把文本进入 fixture input 等同于真实模型遵从
指令或 CLI UI 同步。下一步真实 CLI 专用测试需要确认后续输出实际改变、
thread/turn 不变、两端均看见标记，然后再进入 CC Connect 集成。

检查通过：`go build ./...`、`go test ./...`、probe race/vet、真实 daemon
`-self-test -timeout 60s`。使用可写的 `/tmp` Go 缓存，没有设置
GOTOOLCHAIN=local，没有修改全局配置。

## 真实模型验收（通过）

2026-10-01：CLI/app-server `0.159.3`，新建隔离 thread
`01a0f7cc-70e8-7850-abdc-65975ee39cad`，原 turn
`01a0f7cc-7112-7e93-afbb-46591e8f7ea8`。

thread/start 不传 model、modelProvider、config 或本机模拟端点，使用 daemon
默认真实配置。响应确认 model=`gpt-6.1-sol`、modelProvider=`openai`。
新 thread 使用 read-only/never，不修改全局配置或原任务的审批/沙盒设置。

原客户端要求运行一次 Python 打印命令（25 次 TICK，间隔 2 秒），通过
write_stdin 轮询，原最终标记为 `REAL-STEER-BASELINE-DONE`。收到 live TICK
后，第二客户端 resume 同一个活动 turn，并通过 interactive 发送 steer：
下一条进度以新 nonce 标记开头，最终改用另一新标记，继续等待原命令，
不重跑或中断。

实测结果：

- steerAccepted 返回原 turn ID。
- 真实模型下一条进度为
  `STEER-SEEN-1790863775820182352 已收到调整，将继续等待原来的打印进程结束，不中断或重跑命令。`
- 后续继续报告 TICK 15；最终真实回复为
  `STEER-REAL-DONE-1790863775820182352`，替代原 baseline。
- 两个客户端均收到上述变化和同一个原 turn 的 completed，durationMs=60284。
- 原客户端事件中只有一次 turn/started、一次 commandExecution started/completed；
  同一个命令 item `exec-8c61833d-1596-4246-90dd-98321064fa7e`、同一个进程
  `66575`，exitCode=0、aggregatedOutput 包含 TICK 0..24。没有重跑命令或新建 turn。

证据：[real-model-steer-result.json](codex-managed-daemon-real-model-steer-result.json)。
这次验证了真实模型实际遵从指导，不只是 RPC 成功或 fixture input 含文本。
客户端仍是两个 Go probe；没有把真实 CLI/Desktop UI 或飞书界面算作已验收。

可复现的显式 opt-in 测试：

```sh
CC_CONNECT_REAL_STEER=1 go test ./tools/codex-daemon-probe -run '^TestRealDaemonSteer$' -count=1 -v
```

普通 go test 默认跳过该测试，不自动消耗真实推理额度。测试新建独立 thread，
运行一条打印/计时命令，并记录 `/tmp/cc-connect-real-steer-*/evidence.jsonl`。
只断开自身连接，不控制已有 thread，不操作 daemon 生命周期。测试失败时也
不能静默重新创建任务冒充成功；原任务可能继续到打印结束。
