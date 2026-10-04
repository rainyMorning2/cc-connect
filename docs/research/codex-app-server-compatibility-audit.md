# app_server stdio 与 managed_daemon 兼容性审查

日期：2026-10-02。只比较这两个 app_server transport，不以 exec 为基线。
初次审查不修改生产实现；以下原始发现保留作为修复依据。随后已完成本页所列兼容性修复，当前状态见下节。全过程不启动 daemon，不调用真实模型。

## 修复状态（2026-10-02）

已修复下文复现问题及列出的能力遗漏：

- 持续 reader 仍为唯一消费者；按 turn ID 转发事件到原 `processInteractiveEvents`。
  cron/timer 等待各自 turn 完成，new_per_run 使用传入 side Session，静音任务结束后恢复观察目标。
- 本地消息恢复 stdio 等待队列；外部 CLI turn 正在运行时仍提示 steer/stop。
  detach 仅关闭观察者并释放本地队列，不停止外部 turn；cancel/stop 失败保留状态。
- 复用流式预览、卡片、typing、NO_REPLY、TTS、hooks；审批/问答等待期间暂停 UI 空闲超时。
- 管理 API provider/model 修改新 thread 默认，保留已有 thread/历史；管理 API switch 先验证 attach，失败不换会话。
- relay 保留独立 thread，不因恢复失败另起 thread；审批/问答转发来源会话供人回答。
  无事件时超时也及时返回，后台继续等待结果并保留 pending 交互。
- 普通 turn 不附加 CC Connect 定位上下文；工具 CLI 利用 CODEX_THREAD_ID 经本地 API 自动绑定项目/会话，不修改 daemon 环境。
  服务端历史展示保留旧 invocation context 剥离兼容。daemon 历史优先，记录外部 userMessage。
- status 查询实际 thread 配置，usage 查询 daemon 的账户额度。
- 协议核对发现 pinned 0.159.3 支持 `thread/delete`，已接入旧 `/delete` 能力；
  拒绝运行中、越 cwd、subagent、隐式及开发者当前 thread。`daemon_enable_delete` 可独立关闭。
  删除 RPC 及隔离 guard 已由模拟 transport 回归测试验证，未对真实 daemon 执行删除。

验证：全量 `go test ./...`、显式 CUJ、`go build ./...`、`go vet ./...`，
以及 core/Codex shared/managed 路径定向 race 检查。新增回归覆盖定时任务隔离、队列、
流式卡片、语音、hooks、审批等待、relay 超时/问答、管理 API、历史及失败保留。
真实 daemon 0.159.3 + 本地模拟 provider 验证 attach、问答、Other、steer、默认配置、
审批 cancel、后续普通输入、interrupt、detach；4 次模拟请求，无真实模型或 shell 执行。
隔离 thread `01a0fb9d-8965-71e2-a6aa-aeaa2d3123e3`，
cwd `/tmp/cc-connect-managed-integration-4160918378`。

真人平台卡片操作、真实附件以及更完整 reconnect/长时运行验收仍待进行；
这些没有因本轮模拟边界验证而宣称完成。下文为修复前的审查记录。

## 已复现的冲突

### P1：定时任务没有等待 managed turn 完成

`core/engine.go:3943` 的 shared 分支调用 `processSharedMessage` 后立即返回。
该方法只等 turn/start，结果由持续 reader 异步处理。原 stdio 路径则由
foreground event loop 等待结果后返回。

- cron reuse：ExecuteCronJob 返回时还没有 assistant 历史，因而立即报
  `produced an empty response`；模拟结果 100ms 后正常发到用户。
- cron new_per_run：`processSharedMessage` 重新调用 GetOrCreateActive，忽略
  上层传入的 NewSideSession，用户消息进入主会话历史。上层随即 cleanup，
  观察连接提前关闭，稍后结果无法送达。本地测试同时复现这两点。
- timer 同样调用这条路径并在 new_per_run 后 cleanup；代码审查确认相同风险，
  本轮未单独执行 timer 复现。
- cron/timer mute 包装还会替换共享 state 的 platform/replyCtx，复用会话时
  后续外部事件可能继续被静音，直到用户消息重新绑定发送目标。代码审查发现，未单测复现。

应为任务保留明确的 Session、turn 和发送目标，等待对应 turn 的完成或错误；
持续 reader 仍是唯一事件消费者，不能引入第二个 reader 抢事件。

### P1：管理 API 切 provider 仍清空所有本地会话

`core/management.go:1374` 在 provider activate 后无条件 resetAllSessions。
模拟 API 调用返回 200，但原 thread ID 变成空串，KEEP HISTORY 被清空。
这与已经适配的 `/provider switch` 新 thread 默认配置语义冲突。当前观察连接
尚可能继续存在，后续输入则会遇到本地会话与观察连接不一致。

应让管理 API 遵循同一个 DefaultSettingsAgent capability 和配置保存规则。
model API 已通过 switchModel 修改默认值，但返回 `model updated`，未解释
对已接入共享 thread 不生效；这属于反馈遗漏，不是本轮复现的历史删除问题。

### P2：NO_REPLY 静默协议失效

`core/shared_session.go:146` 直接发送 EventResult.Content，没有复用原 stdio
event loop 的 isSilentReply / stripTrailingSilent 处理。本地模拟证明裸
NO_REPLY 会作为消息发到用户；带尾部标记的文本同样没有剥离逻辑。
影响群聊静默约定，也影响 heartbeat 提示词产生的静默结果。

## 代码审查确认的能力遗漏

| 能力 | stdio 路径 | managed 路径 / 影响 |
|---|---|---|
| 流式预览、rich/streaming card、typing | 使用原 processInteractiveEvents | 最终回复普通文本；工具/评论另发消息，未接入原显示生命周期 |
| `/tts` 语音回复 | 结果后调用 sendTTSReply，判断 always / voice_only | shared 结果分支没有调用；也未记录 msg.FromVoice。入站音频转文本不因此失效 |
| hooks | message.sent、permission.requested、error 等在旧处理分支触发 | shared 的结果/审批/错误分支未触发；直接 attach 也绕过原 session.started 入口。message.received 仍在公共入口 |
| `CC_PROJECT` / `CC_SESSION_KEY` / `CC_DATA_DIR` | 创建自有 app-server 进程时注入 | managed SetSessionEnv 是 no-op，模型执行的 CC Connect CLI 自动定位会话不再有这项保证 |
| relay 恢复与交互请求 | 旧逻辑失败后可另起会话，自动 allow | managed 仍复用旧逻辑：失败会清旧 ID 并尝试新 thread；问答也传 allow，未提供答案，响应错误被忽略 |
| `/status` mode | 默认值基本代表自有进程模式 | 仍读 Agent.GetMode，可能显示新 thread 默认而非已接入 thread 实际策略 |
| `/usage` | Agent 从本地 auth.json 读取 token 查询 HTTP | managed 仍继承这个 Agent 方法；session 的 daemon rateLimits RPC 未接到命令，daemon 与本地登录不一致时可显示错误账户或失败 |
| `/history` 外部输入 | 原本主要记录 CC Connect 自己的输入 | managed item handler 未记录 CLI/Desktop userMessage；本地 history 非空后不再走服务端 history fallback，可能只见外部回复而缺少问题 |

环境能力应通过显式项目/会话参数或受控工具桥接补齐，不能更改共享 daemon 的
全局进程环境。relay 的超时等待方式也需要单独验证；当前 range Events 中
检查 ctx 的旧逻辑不保证无事件时及时返回，这不是本轮认定的新增回归。

## 需要保留或明确说明的行为差异

- 活跃 turn 收到普通消息会提示使用 `/steer` 或 `/stop`，不走 stdio 的
  等待队列。这是当前共享控制设计，不应无意恢复旧 foreground/unsolicited 切换。
- 模型、reasoning、mode、provider 只修改新 thread 默认；已有 thread 保留
  daemon 设置。这一文本/卡片命令路径已有测试，本轮没有发现新的冲突。
- Close/detach 只关闭本观察者；不会停 daemon、interrupt turn 或拒绝审批。
- `/delete` 不删除共享 thread；attach-only 下 `/new` 不会隐式启动新 thread。
- 审批只提供服务端允许的单次决策，不复用客户端持久 allow-all。
- `/compress` 在 stdio 基线中也不支持（Agent.CompressCommand 返回空串），
  不应把它算作 managed 新遗漏。
- 图片/文件输入沿用 stdio staging 和 localImage 构造，已存在实现；本轮
  未做新的真实附件验收。`/shell`、`/diff` 属于公共本地工作目录能力。

## 验证与建议顺序

临时本地诊断测试通过真实 Engine/SessionManager、mock Agent/Platform 和
httptest API，复现 provider 重置、NO_REPLY 外发、cron reuse 提前失败和
cron new_per_run 错会话/漏回复。测试断言用于确认当前错误行为，审查后删除，
避免将错误行为写成长期要求；正式修复应添加相反预期的回归测试。

本轮执行诊断 + 已有 shared CUJ/settings 测试：

```sh
go test ./core -run 'TestAuditManagedCompatibilityObservations|TestCUJ_.*Shared|TestSharedSettings' -count=1 -v
```

结果通过，耗时 0.490s；本机日志 `/tmp/cc-connect-managed-compat-audit-tests.log`。
未重跑全量测试，没有生产代码改动。

建议先处理 cron/timer 的 Session/turn 生命周期、管理 API 清会话以及静默协议；
随后接入 TTS/显示/hooks，并明确环境桥接、relay、状态/额度/历史的 daemon 语义。
已通过的 attach/审批/问答/steer/interrupt 主链路测试不能替代这些能力的验收。
