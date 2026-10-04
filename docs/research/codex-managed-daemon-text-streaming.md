# Managed daemon 正文流式输出修复

2026-10-03。已修复并打包到 v6；已交付 v5 不包含本修复。

## 原因

v5 的 managed adapter 只处理 `item/completed` 中的整段 agentMessage，
漏接原生 `item/agentMessage/delta`。工具的 outputDelta 已接入，所以实际表现为
工具内容持续更新，长正文在生成结束后才一次性更新。
Engine 虽已复用 rich/streaming card 展示，缺少上游正文增量仍无法流式展示。
stdio adapter 的既有正文策略不在本次修改范围内。

## 修复

- managed adapter 实时转发 agentMessage delta，保留 thread/turn/item 隔离以及
  item/started 提供的 phase、delivery。中途 attach 未收到 started 也接受后续 delta。
- 每个正文 item 单独累计已展示文本；完成时只发送缺失尾段，避免整段再次追加。
  完成文本与流式前缀不一致时，用权威全文替换此 item 的预览，保留其他 item。
- 完整文本即使已全部流式输出，也继续汇入最终 EventResult，保证最终卡片与历史完整。
  拒绝已完成 item/turn 的迟到 delta，turn 完成后清理未完成流式缓存。
- 异步提问完成事件保留 questions 和完整问题说明。没有剩余正文也能触发独立卡片，
  不重复添加已输出文字，不影响阻塞式问答与审批。
- 正文增量的标点/省略号保留；继续使用现有卡片节流、静默回复保护与最终刷新逻辑。

## 验证

仅模拟单测、CUJ、相关 rich-card 回归和 race；未调用真实模型或管理 daemon。

- 本地 UDS mock daemon 刻意阻止 item/turn 完成，先验证收到多段中文正文和省略号。
- CUJ 使用真实 Engine/SessionManager，attach 后先检查平台已收到两次正文预览，
  然后才允许完成，再验证 steer、最终卡片、history 与 detach。
- 覆盖完成尾段、全量已流式输出、权威修正、其他 thread 过滤、重复完成及迟到 delta，
  异步提问完整说明、工具增量和既有 rich-card 静默行为。

日志：`dist/text-stream-race.log`。
