# Managed daemon 异步提问

更新：2026-10-03。源代码已接入独立异步问答卡片；已打包到 v5，旧版 v4 不包含此改动。

## 协议与展示

Codex 0.159.3 本机 schema 的 `agentMessage.delivery = "async"`、
`questions: [{title, options?}]` 区分异步提问与普通文本。
managed adapter 保留 delivery、结构化问题及 turn/item 标识。
Engine 的共享 reader 在转发正文到前台或外部任务展示之前，立即发送问答卡片；
每道问题一张卡，标题为“异步提问”，明确说明任务仍继续执行。
原有 agentMessage 文本仍保留；首张卡也包含相关文本，避免正文缓冲导致提问说明迟到。
普通正文、工具输出增量和工具结果仍沿各自原有链路展示。
缺失/无效 questions 的异步消息仍按普通文本处理，不猜测问题或伪造审批。

此设计参考上游 [PR #1942](https://github.com/chenhg5/cc-connect/pull/1942)
的结构化异步问题解析和 `turn/steer` 回答路径。
此分支的 managed daemon 会绑定发卡时的 thread/turn，并显式标记异步语义。

## 回答语义

- 有选项时可直接点按钮；答案带问题标题，通过 `turn/steer` 插入原 turn。
- 无选项或自定义回答时，按卡片提示发送 `/async-answer <token> text <答案>`。
  命令保留多词答案，不把数字自由文本强制解释成选项。
- 多道问题可以分别作答；不会等待全部答完才继续执行工具。
- 普通回复仍遵循 `daemon_busy_message_mode = "steer" | "queue"`。
  异步卡片不会截获普通回复，也不占用阻塞式问答/审批的 pending 状态。
- 回答成功后同一张问题卡的重复点击拒绝；提交失败允许重试。
- turn 结束、后续 turn、detach、切换会话后旧卡片不能向新的任务发送答案。
  不自动排队、不创建新 turn、不调用阻塞问答的 JSON-RPC answer。
- 不支持卡片的平台使用按钮或纯文本提示。

卡片无倒计时，也不会基于本地计时器自动回答或跳过。
当前只处理 attach 后实时收到的异步问题；晚 attach 不重放历史异步问题。
协议没有独立的“仍待回答/已回答”状态，不能把历史提问当作服务端 pending 请求。
阻塞式 `item/tool/requestUserInput` 和审批的服务端重放继续保持原有行为。

## 验证

仅运行相关模拟单测、CUJ 和 race；未启动真实模型、未重启 daemon。
覆盖协议元数据与工具输出区分、实时卡片、前台不等待工具边界、自由回答、
多问题展示、失败重试、重复点击、后续 turn 和切换失效、工具继续执行。

日志：`dist/async-question-focused.log`、`dist/async-question-race.log`。


打包记录（2026-10-03）：本篇实现已包含于 `dist/cc-connect-managed-linux-amd64-v5`；版本/帮助检查通过。配置和构建清单见 `dist/managed-feishu-setup.md`。旧版 v4 不包含本篇新增行为。
