# 第二轮 review：重连、队列交接和补发顺序

2026-10-03。三项 review 经代码核对和模拟回归确认成立，已修复源码，尚未打包。

## 1. 恢复旧结果时，新 CLI turn 已开始

旧实现先将下一条消息移出队列，发送流程发现运行中 turn 后提示忙碌并返回，
消息不再被执行。现在先等待本地 runtime 空闲，再出队；若出队后运行状态再次变化，
保留当前待发送输入重试，不继续消耗后面的队列。

共享发送返回可识别的 `ErrAgentTurnBusy` 表示输入未被接受。
只有明确的 busy 拒绝可重试；其他失败沿用错误处理，不自动重试结果未知的 RPC。
用户历史在有 turn ID 的发送成功后、释放缓存事件之前写入，避免 busy 重试产生重复历史。
没有 turn ID 的旧 Send 接口仍先写历史，保留即时回复的用户/助手顺序。

等待只检查本地 RuntimeState，不新增 RPC 或 daemon 主动轮询。
队列仍保持 FIFO、媒体附件、停止/断开清理和旧会话锁。

## 2. 已完成 turn 的补发不能恢复任务计时器

共享 reader 在 turn 完成后，拒绝其后续开始、正文和重复完成通知，
迟到工具事件仍走独立工具展示。SendTurn 绑定期间缓存的终止事件可以经统一路由
完成原任务，但缓存的旧开始/正文不能重启已经完成的展示。

另加超时保护：展示层中断前检查其 turn ID 是否仍是当前 turn。
managed adapter 实现 `AgentTurnCanceller.CancelExpectedTurn`，中断请求携带指定 ID，
不会因为检查和 RPC 之间出现新 turn 就改为中断新 turn。
没有指定 turn 中断能力的共享 adapter 不通过 CancelTurn 兜底中断。

## 3. 运行中快照也恢复已完成正文 item

对 inProgress 快照中的 agentMessage，复用 item ID 去重和正文补齐/替换逻辑，
恢复断线期间完成的正文；随后的 turn 完成仍包含完整最终答案。
重复快照、后续 item/completed 不重复累积文本；同一 turn 的重复 started 通知
不再清空已恢复答案。历史异步问题不会因此重新生成问题卡片。

## 验证

- 新 CUJ：本地任务 → 消息排队 → 旧完成补发与新 CLI turn 交错 → 新 turn 完成 → 队列执行 → history。
- 新 CUJ：完成通知先到 → 旧 started/正文后补发 → steer 仍成功 → detach。
- 新 CUJ：SendTurn 明确 busy 拒绝 → 等待并重试 → 用户历史只写一次。
- 超时保护覆盖 idle 和 max-turn 两条路径；指定 ID 中断跳过 replacement turn。
- 运行中快照正文恢复覆盖缺失通知、部分正文、错误前缀、重复通知与重复快照。

相关 core/codex 单测、共享 CUJ 和定向 race 检查通过。
临时 overlay 移除三项保护后，对应回归分别复现消息丢失、旧展示超时和最终正文为空。
日志：`dist/review-round2-related.log`、`dist/review-round2-race.log`、
`dist/review-round2-pre-fix.log`。
未运行全仓库测试、真实模型或飞书端到端验证。修复已打包进 v9；v8 不包含这些修复。
