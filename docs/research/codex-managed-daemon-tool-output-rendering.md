# 工具 stdout 被发送为正文的修复

日期：2026-10-03。源码已修复并打包到 v5，旧版 v4 不包含此修复。

用户在 rich 模式下收到配置打印、rg 行号等独立正文。当前配置确认
`[projects.display] card_mode = "rich"`。

daemon 的 `item/commandExecution/outputDelta` 被转成 `EventToolOutput`。
新增共享运行时路径在 `processInteractiveEvents` 中直接发送 delta 文本；
共享 reader 的无 turn 路径也会缓存并直接发送这些文本。这两处绕过了
rich 工具卡片和已有工具结果展示，并可能与完成后的结果重复。

最终修复保留实时增量。`turnId` 隔离任务，`itemId` 标识具体工具调用，
`EventToolOutput` 与助手的 `EventText` 分开。rich 模式把增量写入同一
工具卡片的 Result 区域，标记 inProgress，不写入助手正文或会话历史。
首段及时展示，后续最多每 500ms 合并刷新一次；没有新事件时仍刷新末段。
没有 rich 卡片能力时使用带工具名称/状态的进度展示，不发送裸 stdout。

命令完成后 `commandExecution.aggregatedOutput` 通过 `EventToolResult`
替换同一 item 的实时预览，更新状态/退出码。并行的同名 Bash 调用按
itemId 分开；完成后的迟到 delta 不覆盖最终结果。显示受 tool_max_len
限制，实时预览保留最新内容；关闭显示截断时 live 缓冲上限为 8192 字符。
daemon 的 stdout 和模型输入不变。

异步提问仍作为 agentMessage/EventText 处理；本轮未改变其展示，不把它
当工具输出过滤。异步问答独立卡片和晚 attach 恢复仍未实现。

回归验证：

- `TestSharedToolOutputDoesNotLeakAsAssistantText`：rich/legacy 路由都不发送
  独立 stdout 正文，rich 卡片仍包含完成后的输出。
- `TestSharedUnscopedToolOutputDoesNotLeakAsAssistantText`：无 turn 信息的
  reader 不再将缓存的 stdout 当正文发送。
- `TestCUJ_C7_SharedExternalToolOutputStaysInToolResult`：attach → 外部工具
  调用完成 → history → detach，stdout 使用工具进度/结果展示，正常回复保留。
- `TestLiveToolOutputUsesItemIDsAndFinalResult`：并行工具隔离、Unicode
  截断、最终结果替换、迟到 delta 不复活已完成调用。
- `TestSharedRichToolOutputIsVisibleBeforeCompletionAndKeepsAsyncText`：
  暂不发 completion，仍能看到合并后的 live 输出和异步提问。
- `TestCUJ_C7_SharedLiveToolOutputPreservesAsyncQuestion`：真实 Engine
  ReceiveMessage/SessionManager 路径，平台侧看到任务完成前的工具增量及
  异步提问，完成结果替换预览，history/detach 正常。
- `TestManagedAsyncTextAndToolOutputRemainDistinct`：模拟 daemon 协议事件
  保持 async agentMessage、outputDelta、完成结果的类型和调用 ID。

仅运行相关单测/CUJ及竞态检查，日志：`dist/live-tool-output-focused.log`、
`dist/live-tool-output-race.log`。未运行全量测试、未启动模型；后续已打包到 v5。

同轮用户已确认 `/detach` 未被即时处理是飞书 WebSocket 断线；重连后
收到并处理，与 detach 命令实现无关。`/history` 展示统一的临时修改
已按用户要求撤销，源码恢复为无参数导航卡片、带参数内容检测。


打包记录（2026-10-03）：本篇实现已包含于 `dist/cc-connect-managed-linux-amd64-v5`；版本/帮助检查通过。配置和构建清单见 `dist/managed-feishu-setup.md`。旧版 v4 不包含本篇新增行为。
