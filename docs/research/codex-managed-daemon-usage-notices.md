# Managed daemon 用量及运行提醒

2026-10-03。已接入源码并打包到 v5；旧版 v4 不包含本轮改动。

## 已实现

- `account/rateLimits/updated`：合并每个 limitId 的稀疏更新，不把 null 字段当成恢复。
  读取模型/配额名称、primary/secondary 用量、重置时间，以及
  `rateLimitReachedType`、`spendControlReached`、`individualLimit`。
- 使用率跨越 50/75/90/95% 时发提醒；达到 100% 或服务端明确限制时发限额提醒。
  阈值来自 CLI 0.159.3 的 `chatwidget/rate_limits.rs`。
  同一窗口/阈值不重复发送；恢复、新窗口或更高阈值可再次提醒。
- `warning`：转发全局或当前 thread 的服务端警告，不显示其他 thread 的警告。
- `error` 的 `willRetry=true`：作为可重试提醒显示，保持任务继续运行。
  非重试错误继续走原有终止错误链路。
- `model/rerouted`：显示服务端已经发生的模型调整，并更新本地展示缓存；
  该事件并不代表服务端提供了可选的模型回退请求。

Engine 以独立提醒卡片或纯文本展示，前台/CLI 发起的任务和空闲观察期都可接收。
提醒不是模型正文、不计入模型对话或用户历史，也不阻塞审批或问答。

## CLI 的模型选项与当前边界

核对源码版本 `rust-v0.159.3`：

- `codex-rs/tui/src/chatwidget/rate_limits.rs` 的切换提示阈值是 90%。
  CLI 本地生成选择框；“保留当前模型”不执行模型变更。
  “切换”提交 `AppCommand::override_turn_context`、`UpdateModel`、
  `UpdateReasoningEffort`。
- `codex-rs/tui/src/app_server_session.rs` 的新 turn 提交携带
  `TurnStartParams.model`、`effort`。
- 本机 `TurnStartParams.json` 明确允许模型覆盖当前请求开始的 turn 及后续 turn；
  `TurnSteerParams.json` 没有 model 字段。
- daemon 没有将上述 CLI 选择框作为可回答的审批/问答 server request 对外发送。

因此本轮提醒卡片不提供无效的“切换/保留”回传按钮。现有 `/model` 保持
新建 thread 默认配置语义。若要复现 CLI 的选择行为，需要单独增加
对已 attach thread 的后续 turn 模型选择，并在下一次 `turn/start` 携带覆盖，
绑定源 thread，处理 detach/switch/reconnect 和与 CLI 的设置竞争。
不能把改变未来创建默认值宣称为当前任务即时换模，也不能假定可用模型必有剩余额度。

## 验证

只运行相关模拟单测/CUJ/race；无真实模型、无 daemon 生命周期操作。
覆盖稀疏字段、明确额度限制、重置时间、恢复后再次限制、阈值去重、其他 thread
过滤、重试不结束 turn、服务端模型调整、实时通知后继续 steer 与 detach。

日志：`dist/usage-notice-focused.log`、`dist/usage-notice-race.log`。


打包记录（2026-10-03）：本篇实现已包含于 `dist/cc-connect-managed-linux-amd64-v5`；版本/帮助检查通过。配置和构建清单见 `dist/managed-feishu-setup.md`。旧版 v4 不包含本篇新增行为。
