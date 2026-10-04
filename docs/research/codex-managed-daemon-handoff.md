# CC Connect managed daemon 接续记录

更新：2026-10-01。换电脑、换 Codex 会话后，先读本文件。

**2026-10-02 阶段更新：** 已开始生产集成，新增的是 app_server 下的
`app_server_transport = "managed_daemon"`，默认仍 stdio。Codex adapter、
core 共享事件读取、审批/问答、attach/detach/steer/interrupt 已落代码，
最新源码已移除普通 turn 定位上下文，改为工具 CLI 经本地 API 按 thread 自动绑定；
该修正按用户要求尚未重新打包，旧 v2 二进制仍是先前实现。
真实 daemon + 模拟模型的生产 adapter/Engine 链路通过；飞书/真人 CLI UI 未验收。
随后已继续补齐 stdio 兼容性，含显示/TTS/hooks、cron/timer、relay、管理 API、
历史/额度、工具调用上下文及 thread/delete；全量测试、CUJ、build、vet 和定向 race 通过。后续以
[生产接入阶段记录](codex-managed-daemon-integration.md) 为准；下文“只有 probe”
和“尚未集成”是此前阶段的记录，已过时。

本次接续补齐 probe 的一次性 `cancel` 审批；命令/文件审批无 `other`，
Other 属于独立问答请求。真实 daemon 0.159.3 + 隔离 probe/模拟模型的
cancel 已通过（resolved + 原 turn interrupted），真实 CLI UI 未测。
详见 [审批决策核对与证据](codex-managed-daemon-approval.md)。真实 CLI
steer/interrupt 和 CC Connect 集成仍待进行。

随后补齐 probe 问答：pending-questions/answer/skip，稳定问题 ID、Other
文本、请求重放/重连、外部 resolved 和防重复。真实 daemon 0.159.3 +
双 probe/模拟模型已通过；真实 CLI/飞书 UI 尚未测。
详见 [问答接入验证](codex-managed-daemon-user-input.md)。

interactive 随后补齐 status/steer：显式 expectedTurnId、单 reader 响应匹配、
root/thread 隔离及失败不新建 turn。真实 daemon 0.159.3 + 双 probe/模拟
模型通过新入口验收，含服务端 mismatch 与 completed-turn 拒绝。
详见 [steer 验证](codex-managed-daemon-steer.md)。真实 CLI 行为仍待实测。

用户随后要求真实模型验收：新建隔离 thread，使用 daemon 默认
gpt-6.1-sol/openai（无 model/provider/config 覆盖），已通过双 probe steer。
真实进度与最终回复按新指示改变，两端收到原 turn completed，打印命令仅
执行一次。见 [真实模型记录](codex-managed-daemon-steer.md#真实模型验收通过)
及 [证据](codex-managed-daemon-real-model-steer-result.json)。这已验证模型行为，
但真人 CLI/Desktop UI 和 CC Connect/飞书仍待验收。

随后完成真实模型 interrupt 与审批竞争：interactive 新增 interrupt，
两端均收到原 turn interrupted，同 thread 恢复对话通过。注意 turn 中断
不终止 unifiedExec 后台终端；测试显式 terminate 本次进程并验证停止。
审批并发 accept/accept、accept/cancel、accept-first、cancel-first 全通过：
服务端单次仲裁，最多一次执行，两端 resolved、旧 ID 防重复、连接可复用。
详见 [interrupt 与竞争验收](codex-managed-daemon-interrupt-race.md) 和
[真实模型证据](codex-managed-daemon-interrupt-race-result.json)。

## 目标与分支

用户目标：电脑上的 Codex CLI/Desktop 启动任务，手机飞书通过 CC Connect
接入同一个 runtime，查看后续输出、处理审批、steer 和 interrupt。
重点是共享运行状态，不是共享磁盘历史或减少进程。

- origin：`git@github.com:rainyMorning2/cc-connect.git`。
- 工作分支：`research/codex-managed-daemon`，不推送到 master/main。
- 基础提交：`dfad19415a38b00b2c5c288610784d1a7eef337f`。
- 原机器 worktree：`/home/daizhen/workspace/github_repos/cc-connect/.worktrees/codex-managed-daemon`。

当前只有独立 probe、单元测试和研究文档。没有修改现有 `agent/codex`、
`core`、stdio backend、生产配置或 Go 依赖。CC Connect daemon attach 和
飞书端体验尚未实现。下一步先做真实 CLI cross-client steer 验证，再集成。

新电脑可以这样继续：

```sh
git clone --branch research/codex-managed-daemon git@github.com:rainyMorning2/cc-connect.git
cd cc-connect
go build -o /tmp/cc-connect-daemon-probe ./tools/codex-daemon-probe
go test -race ./tools/codex-daemon-probe -count=1
go vet ./tools/codex-daemon-probe
```

更多依据：[源码研究](codex-managed-daemon.md)、[详细验收](codex-managed-daemon-e2e.md)。
源代码、测试、脱敏 JSON 证据均在分支中。原机器 `/tmp` 的二进制、完整日志、
Codex 源码 checkout、前端 dist、Go 缓存不随 Git 迁移。旧 thread IDs 只是
证据，另一台机器不能凭这些 ID 接入原 daemon；需要新建专用测试会话。

## 验证清单

| 能力 | 结果 | 证据和边界 |
| --- | --- | --- |
| 被动 discovery、WS over UDS、initialize | 通过 | 真实已运行 daemon，不启动或停止它 |
| resume 同一个 active turn | 通过 | self-test 和真实 CLI 均验证活动 turn ID 一致 |
| 中途接入接收 live output | 通过 | 接入后 TICK，非仅读取历史 |
| CLI 退出后后台运行，probe 接入/重连 | 通过 | 同一 thread/turn，收到后续输出和 completed |
| CLI 保持打开，同时 probe 接入 | 通过 | 用户确认 CLI 输出可见；probe 收到 delta 和 agentMessage |
| 接入时重放已有审批 | 通过 | 真实 CLI 第一条 requestId 32 |
| probe 回答，CLI 同步继续 | 通过 | resolved；用户确认 CLI 提示消失并继续 |
| CLI 回答，probe 清除 pending | 通过 | 第二条 requestId 38；pending 空，命令输出和完成可见 |
| 已解决审批再次回复 | 通过（本地防重复） | probe 收到 resolved 后拒绝旧 ID，没有再发响应 |
| 一次性 cancel | 真实 daemon/模拟模型通过 | 原 request resolved、原 turn interrupted；真实 CLI UI 未测 |
| 问答、Other、skip、重连 | 真实 daemon/模拟模型通过 | 同一问答 ID 重放、答案进入后续 input、外部 resolved 清理；真实模型问答未测 |
| cross-client steer | 真实模型通过 | gpt-6.1-sol/openai，双 probe 进度/最终回复改变，原 turn、单次命令保持；真人 CLI UI 未测 |
| cross-client interrupt | 真实模型通过 | interactive 入口、两端原 turn interrupted、同 thread 恢复对话；后台终端须独立 terminate，已验证测试清理 |
| 两个真实 thread 的隔离 | 本地单元测试通过，双真实 thread 未测 | 过滤与控制边界已覆盖；后续真实多任务验收与 Engine CUJ 仍需补 |
| 两端同时点击审批的竞争行为 | 真实模型通过 | 双 probe 并发及先后到达四种组合，服务端只采用一个决定；最多一次执行，两端 resolved 和连接可复用 |
| Desktop GUI 接入同一 daemon | 未实测 | 当前真实客户端是 Windows Terminal → WSL → Codex CLI |
| CC Connect agent/Engine attach、审批、steer | 待实现和验证 | 后续真实 agent/Engine + 模拟平台边界，补 CUJ |
| CLI/Desktop → CC Connect → 飞书 | 待实现和验证 | 用户手机端最终体验验收 |
| dynamic tools、登录/身份验证 | 未验证 | 不可套用普通命令审批的跨客户端路由结论 |

原机器版本：CLI `0.158.0`，managed app-server `0.159.2`。
研究源码固定 `rust-v0.159.2` / `ff6aec96948b70d94983af2641a6b67c94faeff5`。
不要将浮动 main 的行为写成这个版本的实测结果。

### 证据索引

- 模拟 self-test thread：`01a0f17c-a91c-7c02-b063-38e2b7069625`。
  临时模型端点已退出，不要用它继续真实对话。
- 后台运行/重连 thread：`01a0f1a3-916d-78a0-9541-b00864547127`；
  turn：`01a0f1a3-c2f8-7eb1-9e96-0db64ee44c56`。
  [real-client-result.json](codex-managed-daemon-real-client-result.json)。
- 保持 CLI 打开/双向审批 thread：`01a0f1c3-8aa2-7b02-8459-70430367dc4d`；
  cwd：`/tmp/cc-daemon-approval-e2e`。
  第一条 turn：`01a0f1c4-b748-72f0-86fc-368161bfdac4`，ID 32，probe 允许；
  第二条 turn：`01a0f1ca-04a8-7791-b26d-0da2cb6b4828`，ID 38，CLI 允许。
  两条均 completed，probe 已 detach。
  [dual-client-result.json](codex-managed-daemon-dual-client-result.json)。

### 容易误解的观察和用户确认

thread `source: vscode` 不能推断用户在 VS Code 启动。用户确认启动路径为
Windows Terminal → WSL → Codex CLI；该版本 app-server 启动入口向 runtime
传入 `SessionSource::VSCode`。此前按 source 推断终端的说法已纠正。

CLI 的 Running 块和 show details 停在 TICK 11，agent 回复继续报告 22、33，
结束时另一个 Ran 块显示 33..35。probe 连续收到同一个 command item 的
TICK 1..35，最终结果含 0..35，只有一次执行完成。进度回复来自原任务 agent，
probe 没发 prompt/steer。CLI 源码显示 assistant 回复提交当前活动块，
后续 delta 仅更新当前 active ExecCell，完成时可另建结果块，与现象吻合。
用户接受这个展示行为，明确“不算缺口”，不作为本项目阻塞项。
保留观察，但没有 CLI 入站 trace、单客户端对照或刷新恢复实测，不声称已经
严格排除多客户端影响。CC 正式实现按 item ID 保留状态，进度消息不结束命令。

## probe 使用和实现位置

```sh
# loaded threads 第一页，最多 100 条，不订阅
/tmp/cc-connect-daemon-probe -timeout 15s
# exact cwd 查询遍历所有页，只读 metadata
/tmp/cc-connect-daemon-probe -find-cwd /tmp/cc-daemon-approval-e2e -timeout 30s
# 只观察专用测试会话
/tmp/cc-connect-daemon-probe -watch <TEST_THREAD_ID> -timeout 5m
# 接收 stdin 审批控制指令
/tmp/cc-connect-daemon-probe -watch <TEST_THREAD_ID> -interactive -timeout 15m
# 新隔离 thread + 本机模拟 Responses 模型，验证协议
/tmp/cc-connect-daemon-probe -self-test -timeout 60s
```

interactive 每行一个 JSON；42 只是示例，换成真实 pending ID 并保留其类型：

```json
{"action":"pending"}
{"action":"reply","requestId":42,"decision":"accept"}
{"action":"interrupt","expectedTurnId":"<ATTACHED_ACTIVE_TURN_ID>"}
{"action":"detach"}
```

只处理目标 thread 的 commandExecution/fileChange 审批，不自动审批。
支持 accept/decline/cancel，且实际 availableDecisions 必须提供所选决策。
此前真实 CLI 请求提供 accept/cancel，不提供 decline；cancel 现已补齐，
通过隔离真实 daemon/模拟模型验证，CLI UI 仍待实测。不修改持久策略。
approvalReplySent 只表示发送成功，要用
resolved + 结果确认。detach/超时只关自身连接。

| 文件（tools/codex-daemon-probe/ 下） | 用途 |
| --- | --- |
| client.go | passive discovery、WS over UDS、initialize、请求与事件队列 |
| main.go | flags、cwd 查询、watch、当前 CODEX_THREAD_ID 防误操作 |
| selftest.go | 隔离模拟模型，跨客户端 approval/steer/reconnect/interrupt |
| approval.go | pending、resolved 清理、thread 过滤、原类型 ID、防重复回复 |
| interactive.go | 单 reader、stdin 控制、单循环拥有状态和 writer |
| client_test.go、approval_test.go | 本地 UDS fixture、审批回归测试 |
| steer.go、interrupt_test.go | steer/interrupt 单 reader 响应匹配与 turn 绑定测试 |
| real_controls_test.go | opt-in 真实模型 interrupt、后台终端清理与审批竞争 |

interactive **已支持 status/steer/interrupt**。interrupt 必须显式 expectedTurnId，
RPC accepted 与 turn interrupted 分开确认，不自动清理后台终端。真实 steer 测试
可直接使用新入口；不要让 request() 与后台事件 reader 同时读一个 WS。
当前实现保持单 reader，主循环匹配 steer 响应；不是生产多 session dispatcher。

## 下一步：真实 CLI cross-client steer

1. 用户新建专用空目录及 CLI 会话，保持 CLI 打开，启动约 180 秒的打印任务；
   agent 定期回复进度。不改文件、不联网，不用承载开发工作的会话。
2. probe resume，获取明确 threadId 和 active turn ID；使用 interactive steer 入口，
   `turn/steer` 带 threadId、expectedTurnId、text input，只控制指定 root thread。
   例如 input：`[{"type":"text","text":"后续进度回复以 STEER-SEEN 开头，最终回复 STEER-E2E-DONE；不要重跑当前命令。"}]`。
3. 既检查 RPC 返回原 turn ID，也检查真实 agent 后续行为改变，两边看到
   STEER-SEEN/STEER-E2E-DONE，thread/turn 不变，没有暗中新建 turn。
   steer 不等于改变或杀掉已运行的 OS 命令，应检查模型随后行为。
4. 测 expectedTurnId 过期/turn 已结束：明确失败，不能自动变成 turn/start。
   保存实际版本、控制请求、两端可见结果和完成事件。
5. 真实模型双 probe interrupt 与审批竞争已完成；真人 CLI UI 与真实模型问答
   仍待验收。接入生产 agent/Engine 后补 CUJ 和飞书用户旅程。

## 集成约束

- 保留 backend="app_server"、app_server_url="stdio" 的原行为。
  app_server_mode="daemon" 目前只是方案，还不是已支持配置。
- daemon JSON-RPC 原生 WS over UDS；stdio 继续 JSONL。不加 JSONL↔WS adapter，
  不依赖 proxy，不硬编码 socketPath。
- `codex app-server daemon version` 被动 discovery；失败报错，不隐式启动、
  重启或回退独立 app-server 冒充共享成功。
- attach 要 resume、订阅、恢复 active turn/pending；不传 cwd/model/approval/
  sandbox 覆盖外部任务。只切换 session ID 不会自动接入事件流。
- thread ID 过滤；resolved 清理卡片和本地状态；活动 turn 明确 steer，
  带 expectedTurnId；Close 是 detach，interrupt 单独操作，不能 Process.Kill。
- 重连不自动重发可能已执行的控制请求。daemon 没有 per-client environment
  isolation，继续保留独立 stdio；dynamic tool/verification 另行验证。
- core 不硬编码 agent/platform，通过可选能力接口；按 AGENTS.md 补测试、
  i18n、CUJ。分三层验收：真实 CLI → probe；真实 daemon/agent/Engine →
  模拟平台；最后 CLI/Desktop → CC Connect → 实际飞书。

## 环境、检查和操作边界

原机器 Go 为 go1.25.14 linux/amd64，go.mod 要求 go 1.25.0。
用户最终要求：不要显式带 GOTOOLCHAIN=local。使用默认 GOTOOLCHAIN=auto；
本轮最后 build/test/vet 未再出现下载 1.26 的提示。依赖曾含 toolchain 1.26.2
提示，下载的准确触发来源没有确认，不要写成定论。

GOMODCACHE 已用 Go 用户配置固定为 `/home/daizhen/.cache/go-mod`，用户也把
.bashrc export 移到提前 return 前。旧 `/home/daizhen/go/pkg/mod` 与新缓存
逐文件确认完全重复后按授权清理；GOPATH `/home/daizhen/go` 保留。
这些是原机器环境操作，不是 Git 修改；新电脑按自身环境选择缓存。

已通过 go build ./...、go test ./...、probe race 和 probe vet。
interactive 新增后完整测试也通过，之后只改研究文档/证据。
新 checkout 完整检查若缺 web/dist，先在 web/ 按仓库 pnpm lock 安装依赖
（本次用 Node 24），运行 npm run build，回根目录检查。dist 不提交，
不要为复现升级前端 lockfile。当前没有 core 修改；正式集成后补 CUJ。

本次开发 agent 自己也在同一个 daemon。严禁 stop/restart/update/bootstrap
daemon，严禁对当前 agent thread 回复 approval、steer 或 interrupt。
主动控制只对 self-test 新建 thread 或用户指定的专用测试 thread。
用户曾中断自动启动 CLI 的尝试，后续由用户自己启动并提供 ID/目录。
`codex --remote unix:// ...` 示例未在用户端启动成功；用户自行 cd 到测试
目录用普通 codex 启动后，已确认目标任务处于同一 daemon。

交给新 Codex 会话的提示：

> 阅读 docs/research/codex-managed-daemon-handoff.md，继续 research/codex-managed-daemon 分支。先验证真实 CLI cross-client steer，再实现 CC Connect daemon attach。不要操作当前会话所在 daemon 的生命周期或当前 thread；保留 stdio，Go 命令不要带 GOTOOLCHAIN=local。
