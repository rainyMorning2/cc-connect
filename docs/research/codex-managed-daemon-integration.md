# CC Connect managed transport 阶段记录

更新：2026-10-02。已完成 stdio 兼容性补齐，未提交或推送。

## 配置边界

保留现有 `backend = "exec"` 和 `backend = "app_server"`；在 app_server 内选择传输：

```toml
[projects.agent.options]
work_dir = "/path/to/project"
backend = "app_server"
app_server_transport = "managed_daemon"
daemon_attach_only = true
daemon_reconnect_attempts = 3
daemon_enable_steer = true
daemon_enable_interrupt = true
daemon_enable_approvals = true
daemon_enable_questions = true # blocking requestUserInput
daemon_enable_async_questions = true # advisory questions answered through steer (v6+)
daemon_enable_delete = true
# daemon_socket = "/path/to/app-server-control.sock"
```

默认传输仍为 `stdio`，旧配置保持原路径和 optional interface 集合。
daemon 专属选项只用于 managed transport，错误类型、未知选项和混配均拒绝。
示例已加入 `config.example.toml`。`daemon_attach_only` 默认 false，可创建新 thread；
true 时需要先 `/attach` 或 `/switch`。空 socket 通过 CLI 的 daemon version 只读发现。

## 本轮实现

- 生产 Codex adapter：UDS WebSocket 连接、initialize、单 reader RPC 路由；
  attach/resume 仅传 thread ID，不覆盖原模型、provider、审批、sandbox 或提示词。
- 新建 thread 使用 CC Connect 配置；不写 daemon 的凭据、provider 文件或环境。
  不调用 daemon 启停、升级；Close 仅断开本客户端。
- core 通过 optional interfaces 识别共享 runtime，持续读取外部启动任务的事件，
  避免原 subprocess unsolicited reader 自动拒绝审批的路径。
- `/attach`、`/detach`、`/steer`、原 `/stop`、`/terminals` 及 scoped terminate；
  原 `/list`、`/switch`、`/history` 接入。失败恢复不会另建 thread，控制 RPC 不重试。
- 仅显示服务端提供的审批决策；处理外部 resolved、多请求排队、稳定问题 ID、
  Other 文本及 skip。卡片 token 绑定请求，旧卡片不能误答新请求。
- 新增五语言提示及共享 runtime 帮助；修复即时回复导致历史顺序倒置，
  移除共享审批中的旧 `allow all` 提示，均有回归测试。

## 验证证据

普通模拟 transport 单测和 core CUJ 覆盖了配置隔离、cwd/current thread guard、
typed RPC IDs、外部 resolved、旧按钮防误用、问答、失败不换会话、steer、interrupt、
后台终端操作和 interrupt 后恢复。新增路径 race 检查通过。

最终检查均通过：`go test ./...`、`go build ./...`、
`go test ./core -run TestCUJ -count=1`，以及
`go test -race ./agent/codex ./core -run '(Managed|Daemon|Shared)' -count=1`。
race 是新增路径的定向检查，并非全仓库 race。

生产链路 opt-in 验收使用真实 daemon **0.159.3**、本地模拟 Responses provider，
外部边界为测试 Platform。通过真实 Engine.ReceiveMessage 驱动生产 Codex adapter。
未调用真实模型，没有 shell 执行，控制仅针对新建隔离 thread：

- thread：`01a0fae0-d54c-7d02-81f4-6c7164e4b6ae`
- cwd：`/tmp/cc-connect-managed-integration-3493695870`
- 模拟模型请求次数：4
- 通过：运行中 attach → 问答卡片 → detach → pending 问答重放 → steer →
  选项及 Other 文本送入后续模型输入 → 最终回复 → 审批 cancel →
  同 thread 启动后续任务 → `/stop` interrupt → detach；最终无 active turn。

首两次验收失败来自测试 harness：首次未启动首个任务便 resume，新 thread
没有 rollout；第二次等待了卡片未展示的 justification。修正测试顺序和显示断言后通过。
本轮发现的实际产品文案问题（仍显示 allow all）已单独修复并加 CUJ 回归断言。

复现：

```sh
CC_CONNECT_DAEMON_SIMULATED=1 go test ./tools/codex-daemon-probe \
  -run '^TestManagedBackendWithDaemonAndSimulatedModel$' -count=1 -v
```

该测试默认 skip；需现有 daemon。它只新建隔离 thread，保留诊断 workspace，
不操作运行本开发任务的 CODEX_THREAD_ID，不管理 daemon 生命周期。

## 后续事项

app_server stdio 与 managed_daemon 的专项兼容性审查见
[兼容性审查记录](codex-app-server-compatibility-audit.md)。已修复 cron/timer 会话隔离与等待、
管理 API provider 清会话和 NO_REPLY 外发，以及该记录列出的显示/TTS/hooks、
relay、调用上下文、status/usage/history 能力遗漏。原始审查记录保留，最新状态在其顶部。

- 真人 CLI/Desktop + 飞书消息和卡片操作尚未验收；本轮只模拟 Platform 边界。
- 生产 reconnect、分页 list/history、能力开关关闭后的实际 daemon 行为需要更完整测试。
- 多客户端控制仍由服务器仲裁；需要生产链路审批竞争验收。
- 模型/reasoning/mode/provider 命令已补齐新 thread 默认配置语义，见下方补充；
  不提供对已有共享 thread 的隐式配置覆盖。
- 共享 runtime 不注入每会话进程环境；现通过每次输入的显式 CLI 参数上下文保留工具定位能力。
  原有 streaming card/TTS/hooks 已复用，仍需真人平台呈现验收。
- 并发 attach/detach 的生命周期、长时事件缓存和 reconnect 重放边界需继续审查。
- `/delete` 已接入服务端 thread/delete，支持 daemon_enable_delete 开关及 inactive/root/cwd guard；
  删除用模拟 transport 验证，未删除真实 daemon thread。daemon credentials/provider/environment 由其自身配置。

当前结果是可运行的首阶段集成，尚未达到完整发布验收。

## 后续补充：批量后台终端清理

用户随后要求增加 `/terminals stop all`。此命令对当前接入 thread 的
后台终端列表取一次快照，逐个调用已有 scoped terminate；不影响当前 turn
或客户端连接。之后新启动的后台终端不包含在本次操作里。
单项失败会继续处理其他项，最后显示成功/失败数量和具体失败 ID；列表查询失败
则不发出 terminate。没有后台终端时返回现有空列表提示。
`/stop` 仍仅表示 interrupt，`/terminals stop <id>` 保持原行为。
添加了空列表、查询失败、部分失败后继续处理、重复 ID 防重复的单测和
用户旅程测试（批量清理后仍可 steer、interrupt、继续对话）。

## 后续补充：配置命令兼容

`/model`、`/reasoning`、`/mode`、`/provider` 的 managed 路径已独立接入，
通过 optional capability 选择，不影响旧 exec/stdio。文本命令、纯文本/inline
button 显示、卡片查询和 in-place action 使用同一验证与修改逻辑。

- 修改的是新 thread 默认配置，反馈明确生效范围；不关闭当前观察连接，不清空
  已接入 thread ID、历史或 pending 审批/问答，不启动新 turn。
- 查询将“服务端返回的已接入 thread 配置”和“CC Connect 的新 thread 默认配置”
  分开显示。通过独立连接、metadata-only `thread/resume`（仅 threadId 和 excludeTurns）
  读取权威配置，不传任何配置覆盖，不修改观察者的当前 turn。
- 运行配置读取失败明确显示失败，不拿默认值冒充运行值。provider 显示为服务端
  实际值，mode 展示实际 approvalPolicy/sandbox，不强行映射为 CC Connect 模式。
- provider switch/clear 保存默认选择，不将新默认绑定到旧 thread。恢复旧 session
  时不再用旧 provider 字段覆盖新的创建默认；provider 目录编辑按名称保留选择，
  防止删除条目后的下标错位。新增/选择 provider 提示必须已在 daemon 中配置，
  CC Connect 不安装 daemon 凭据或环境。
- 单测覆盖文本/卡片修改、错误输入、保存失败、读取失败、历史及连接保留、
  authoritative settings RPC、外部模型变更刷新、仅新 thread 应用默认值及不写 auth。
  新增 CUJ 验证设置后仍收到实时事件、能查看历史及 steer。

真实 daemon 0.159.3 + 本地模拟 Responses provider 的生产链路已通过：
等待问答时修改这四组默认配置，实际运行模型/provider/审批策略保持原样，
问答、steer、cancel、后续同 thread 任务和 interrupt 全部继续通过。
隔离 thread `01a0fafe-a83d-7661-8f70-2ebf2111f86f`，
cwd `/tmp/cc-connect-managed-integration-1129911055`，模拟请求次数 4，
未调用真实模型，未执行 shell，未修改 daemon 生命周期。

## 后续补充：stdio 体验兼容

本地消息、cron/timer、外部 CLI turn 的 UI 复用原事件处理器，daemon 持续 reader
仍是唯一事件消费者。按服务端确认的 turn ID 分发，避免抢事件及其它客户端 turn 混入；
本地普通消息恢复等待队列，外部活动 turn 使用 steer/stop。审批等待暂停 UI 空闲计时。
定时任务遵循传入 Session、等待实际完成；静音发送目标在任务后恢复。

补齐管理 API 默认配置/会话切换、relay 人工交互及超时、显式工具调用上下文、
daemon 账户额度/权威历史、外部输入记录、status 实际配置以及 scoped thread 删除。
这些能力通过 optional interfaces 选择，exec/stdio 继续使用原路径。
详见上方兼容性审查记录的修复状态及新增回归测试。

最新生产链路验证：真实 daemon 0.159.3 + 本地模拟 provider，隔离 thread
`01a0fb9d-8965-71e2-a6aa-aeaa2d3123e3`，cwd
`/tmp/cc-connect-managed-integration-4160918378`，4 次模拟模型请求；
attach、问答/Other、steer、默认配置、审批 cancel、同 thread 普通输入、interrupt、
detach 均通过。没有真实模型调用、shell 执行、daemon 生命周期操作或真实 thread 删除。

最终检查：全量测试、CUJ、build、vet、shared/managed 定向 race 均通过。
日志位于 `/tmp/cc-connect-managed-compat-{full,cuj,vet,daemon}.log` 及
`/tmp/cc-connect-shared-compat-race.log`；真实平台边界仍需用户验收。

## 真实飞书反馈后的修复：用户消息与审批展示

2026-10-02：用户实测发现普通消息包含完整 invocation context，审批缺少
本会话/相似请求选项及 reason。确认原实现确实把上下文作为用户文本发送，
权限请求也只显示 allow/deny，命令审批过滤了结构化规则决策。

- managed SendTurnWithContext 通过协议的 `turn/start.additionalContext` 发送
  `kind=application` 的精简工具定位上下文。它仍进入模型上下文并占用 token，
  但不拼入 userMessage；不修改已有 thread 的 developer instructions 或其它设置。
  未实现此 optional capability 的适配器沿用旧文本上下文回退。
- permissions/requestApproval 补齐 scope=session 与 scope=turn 的区别。
  command approval 保留服务端 offered acceptForSession、execpolicy amendment、
  network policy amendment；按钮仅引用本请求保存的原始决策，不能由客户端扩大规则。
  explicit availableDecisions 存在时不根据 proposed rule 擅自增加选项。
- 飞书卡片/inline/纯文本补齐 reason、cwd、相似命令前缀或网络主机详情，新增标签五语言齐全。
  单次授权、会话授权、相似命令规则是不同语义，没有恢复客户端无条件 allow-all。
- 增加 transport 回归、用户消息队列上下文分离和审批卡片 CUJ。
  真实 daemon 0.159.3 + 模拟 provider 验证独立上下文进入模型且 userMessage
  只含原始用户输入；同时原有问答/Other/steer/cancel/interrupt 主链路通过。
  隔离 thread `01a0fc02-e22d-7160-aa7f-157ea6e0f6fb`，
  cwd `/tmp/cc-connect-managed-integration-4092860316`，4 次模拟请求，无真实模型/shell。
  新增会话/规则审批响应目前由模拟 transport 和 CUJ 验证，尚待用户真实飞书点击验收。

修复版独立二进制：`dist/cc-connect-managed-linux-amd64-v2`，旧文件保留。
既有消息中的历史前缀不回写删除；更新后新发送的消息不再带该前缀。

协议依据：pinned rust-v0.159.3 的
[turn](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/app-server-protocol/src/protocol/v2/turn.rs)、
[item](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/app-server-protocol/src/protocol/v2/item.rs)、
[permissions](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/app-server-protocol/src/protocol/v2/permissions.rs)。

## 后续修正：移除模型定位上下文，工具调用自动绑定

按用户要求，停止向普通 turn 添加 invocation context 或 additionalContext；
移除其 optional sender 和文本回退，普通消息、排队消息、relay 都不再额外附上这些信息。
此前 v2 的改法仅分离展示，仍有模型上下文开销；现已被本节方案替代。
旧 thread 已写入的历史不回写删除，干净验收可使用 /new。

新机制只在工具调用时运行：

- Codex 原生 shell 环境提供 CODEX_THREAD_ID，新版 CC Connect CLI 将其作为
  agent_session_id 传给本地 Unix API；这是本地路由元数据，不进入模型输入。
- API 根据活跃 SharedAgentSession 连接绑定 project/session_key；不依赖全局
  最近会话，不修改 daemon 环境。detach/连接不可用即不再支持自动路由。
- send、cron/timer add、cron/timer list、relay send 接入自动定位；relay 的目标项目仍由工具参数指定。
  cron/timer 的后续按任务 ID 操作保持原行为。
- 显式 session 参数优先；stdio 会话继续使用其带作用域标记的 CC_SESSION_KEY/CC_PROJECT，
  managed daemon 不读取继承的旧路由变量。
  未绑定、跨 project 或多聊天歧义报错，不回退到唯一项目/最近聊天误发。
- --data-dir 保留优先级；CC_DATA_DIR 只用于 stdio 的会话环境及非 Codex 调用，
  managed 工具环境读取默认可发现配置的 data_dir。
  使用不在默认配置查找范围内的独立配置时，仍需 CLI --data-dir 指定对应实例。
- 自动绑定要求实际执行的工具 CLI 使用新版代码；旧 PATH 中的 CC Connect 可执行文件
  不会因桥接服务更新而自动获得新逻辑。统一打包时同时更新 CLI 入口。

验证：新增 CLI/API 回归、审批回归、队列纯消息、relay、跨聊天/显式参数/未知 thread/
detach 的路由隔离及 CUJ。全量测试、CUJ、vet 和新增路径定向 race 通过。
既有 CUJ H2 首次并行验证出现一次超时，独立连续 3 次重测及完整 CUJ 重跑通过。
真实 daemon 0.159.3 + 本地模拟 provider 验证模型输入没有 CC Connect 定位上下文、
userMessage 原样，以及通过本地 API 自动发送到绑定测试 Platform；4 次模拟请求。
隔离 thread `01a0fc44-737d-7dd2-bb7e-fcaf2ec3d994`，
cwd `/tmp/cc-connect-managed-integration-4018251234`。没有真实模型或 shell 执行。

按用户要求本轮未重新编译交付可执行文件、未替换已运行 v2，也未改用户配置。
Codex 环境变量依据：
[0.159.3 shell_environment.rs](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/protocol/src/shell_environment.rs)。


### 2026-10-02：工具路由环境隔离与飞书断线排查

managed daemon 不注入 `CC_PROJECT` / `CC_SESSION_KEY` / `CC_DATA_DIR`，工具调用也不再优先使用 daemon 启动时可能继承的这些变量。没有显式目标时，使用 Codex 原生 `CODEX_THREAD_ID` 向本地 API 查找当前连接绑定；未知或多义绑定仍报错。显式 CLI 参数保留优先级。

由 cc-connect 启动的 stdio 子进程使用 `CC_CONNECT_SESSION_ENV=1` 标记其会话环境，继续使用原有 `CC_*` 自动路由和自定义 data_dir。其他没有 Codex thread ID 的调用也保留原有环境变量行为。标记只属于桥接器拥有的子进程，不给共享 daemon 设置。

用户提供的飞书日志出现两次 TCP 读取超时（18:37、19:04），第一次 11 秒后重连成功；第二次日志未包含恢复记录。可能与机器黑屏、休眠或网络中断相关，尚未确定最终原因。进程存在不能证明 WebSocket 在线。按用户要求暂不升级飞书 SDK，已撤回本轮依赖及恢复逻辑改动，继续固定 v3.5.3。保持网络和机器运行可避免部分触发条件，但不能保证长期连接不会断开。没有重新生成启动二进制。


### 中断通知显示后台命令数量

收到 turn/completed 的 interrupted 状态后，通过 AgentBackgroundTerminals 查询当前 thread 的后台列表，并在飞书等平台的中断通知中显示按 process ID 去重后的数量；只读查询不调用模型。普通 shared reader 和共用 foreground 展示链路均覆盖。查询超时上限 5 秒；失败记录日志并回退到“可能仍在运行，使用 /terminals 查看”，不误报零个。保留 /stop 的 interrupt 语义，不自动终止后台进程。新增五种语言文案、数量/失败回归测试，并更新 C8 CUJ 覆盖仍有后台命令和批量停止后零个的提示。未重新生成启动二进制。


### 会话列表卡片按钮立即 attach

用户操作 `/session`（`/list` 别名）后点击右侧会话按钮，走的是 `act:/switch` 卡片回调，而不是文字命令 `/switch`。原卡片路径仅切换 SessionManager 记录并关闭旧 reader，没有调用共享 daemon 的 attach，因此直到后续普通输入才有机会建连接。现通过现有 ReplyContextReconstructor/活跃回复目标解析，复用 cmdSwitch 的 attach 和校验路径，成功后立即启动 reader，并更新列表；失败保留旧连接及当前选择。移除旧卡片切换的 ClearHistory，使 owned stdio 和 shared daemon 都保留原会话历史。

B13 CUJ 新增用户路径：ReceiveMessage(`/session`) → 从实际生成的列表取按钮值 → 执行同一卡片回调 → 平台收到实时消息 → `/steer` → `/history` → 切换失败后仍收到原 thread 后续结果 → `/detach`。另有非共享会话卡片切换保留历史的回归测试。验证仅使用模拟边界，不连接真实模型或真实飞书。未重新打包。

卡片切换最终验证：定向回归、全量 `go test -p 2 ./...`、全量 CUJ、定向 race 和 core vet 均通过。首次全量/race 因 /tmp 空间不足失败，清理可再生测试缓存后重跑通过；按用户要求，在测试结束后清理 /tmp/cc-connect-go-build、cc-connect-go-mod 等本任务缓存及遗留测试目录，保留验证日志与运行时目录。


### /history 全部显示 00:00:00 的修复

根因是 managed GetSessionHistory 构造 HistoryEntry 时只设置 Role/Content，未传递时间，核心将零值 time.Time 格式化成午夜。当前 daemon 0.159.3 的 ThreadReadResponse schema 提供 Turn.startedAt / completedAt（nullable，Unix 秒）；只读核对之前模拟 thread `01a0fc44-737d-7dd2-bb7e-fcaf2ec3d994`，三轮均有正常开始/结束秒数，未启动模型、未 resume 或控制任何 thread。

现用户消息使用轮次开始时间，助手消息优先使用轮次结束时间，仍在运行或无结束时间时回退开始时间。协议没有逐消息时间，因此这是轮次时间；缺失时保留零值，并在卡片/文字历史显示本地化“时间未知”，不伪造当前时间、thread.updatedAt 或午夜。历史展示统一转换到服务机器本地时区。

新增 daemon 历史时间/缺失/分页限制回归测试、本地时区/未知时间测试，以及 B3 CUJ：attach → 卡片 history → 文字 history → steer，验证历史读取不破坏实时连接。仅使用模拟模型边界和一次已有模拟 thread 的只读核对。尚未重新打包，当前 v3 启动文件仍包含修复前逻辑。

历史时间修复最终检查：定向回归、全量 CUJ、定向 race、core/codex vet 和全量 go test 均通过。首次全量在未改动的 TestMgmt_CronExecByID 临时目录清理时遇到目录非空；该测试单独连续 5 次通过，全量复跑通过。日志位于 dist/history-time-*.log。
