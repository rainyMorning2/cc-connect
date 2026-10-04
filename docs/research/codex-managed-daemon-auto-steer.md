# 普通消息自动 steer

日期：2026-10-02。已实现并打包到 `dist/cc-connect-managed-linux-amd64-v4`；v3 不含此修改。

## 配置

```toml
[projects.agent.options]
backend = "app_server"
app_server_transport = "managed_daemon"
daemon_busy_message_mode = "steer"
```

`daemon_busy_message_mode` 仅适用于 managed daemon：

- `steer`：已连接 thread 有 active turn 时，将普通文本通过 `turn/steer`
  插入该 turn。既适用于 CLI 发起的任务，也适用于 CC Connect 发起的任务。
  没有 active turn 时仍按普通消息开始下一轮。
- `queue`（默认）：普通消息排队，active turn 结束后发送。不区分任务是
  CC Connect 还是 CLI 发起；排队期间仍可显式 `/steer` 插入当前任务。
  `/stop`、`/detach`、切换会话按原队列清理机制丢弃尚未发送的消息。

非法值、非字符串以及 `steer` 与 `daemon_enable_steer=false` 同时配置会
在 agent 创建时被拒绝。工作区 agent 克隆保留该设置。stdio/exec 不接受
该 daemon 配置，不改变其运行行为。

## 输入规则

审批和阻塞式问答的回答优先处理，不转为 steer。显式命令保留原行为。
steer 携带收到的消息文本，没有附加模型上下文。

现有 steer 接口只支持文本，运行中的附件消息会明确提示用户改发文本，
或等 turn 结束后重发附件。不会只发送文字并悄悄丢弃附件。

steer 固定 thread ID 和 expectedTurnId；服务端拒绝、连接失败、turn 在
发送时刚结束等错误会反馈给用户，不把失败的 steer 转成下一轮消息。
成功响应表示 daemon 已接受输入，其消费时机仍由 Codex 的 steer 机制决定。

这项功能让异步提问可以用普通回复插入当前任务，但没有实现异步提问卡片
和历史异步问题恢复；该缺口见 `codex-managed-daemon-async-questions.md`。

## 验证

- 配置默认值、合法/非法值、禁用 steer 冲突、工作区克隆、stdio 隔离测试。
- CUJ：外部运行中任务普通回复插入；阻塞问答优先；turn 结束后新任务；
  CC Connect 发起的任务插入后不会再排队成下一轮；默认行为与显式 steer。
- 回归：steer 失败不排队，不开始新 turn；附件不静默丢失。
- queue 补齐回归：CLI 发起任务完成后按 FIFO 发送，保留附件，遵守队列上限；
  `/stop`、`/detach`、切换 thread 清理旧队列。该补齐轮按用户要求仅运行
  相关单测/CUJ 与对应竞态检查，未再次运行全量测试或启动模型验证。
- 真实既有 daemon + 新建独立测试 thread + 本地 Responses 模拟模型：
  运行中普通回复获得 steer 成功响应；审批仅取消，未调用真实模型。

验证日志位于 `dist/auto-steer-{focused,full,cuj,race,vet,build}.log` 和
`dist/auto-steer-daemon-check.log`。

queue 补齐轮验证日志：`dist/shared-queue-focused.log` 与 `dist/shared-queue-race.log`，均通过。
