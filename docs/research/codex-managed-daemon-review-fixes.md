# Managed daemon review 修复

2026-10-03。已修复并打包到 v6；现有 v5 不包含这些修复。

1. 断线后记录原 turn ID。resume 后对照该 turn 的服务端状态；已完成、失败、被中断时补发 EventResult 和完整答案，与正常 turn/completed 共用去重逻辑。快照没有旧 turn 时只补做一次 thread/read(includeTurns=true)，不是周期轮询。无法对账时明确报重连失败，不把未知任务当成成功恢复。旧 turn 的恢复不会清除另一个正在运行的 turn。
2. 共享会话集中保存待处理请求。新展示流程继承当前 turn 的审批/阻塞问答状态；空闲和最大执行时间检查也核对统一状态。等待用户期间暂停这两种超时，解决后恢复剩余执行预算及新的空闲预算。异步提问不暂停执行超时。
3. 工具完成状态不再依赖 rich 展示分支。legacy、rich 都以最终结果替换增量，包括空结果；后续工具输出不会把已完成工具重新作为 inProgress 展示。
4. daemon_enable_questions 保持只控制阻塞问答。新增 daemon_enable_async_questions，默认 true，可独立关闭异步问答卡片，保留问题正文及普通文本回复/steer 行为。配置复制到新工作区时保留两者。

验证使用本地 UDS 模拟 transport 和真实 Engine/SessionManager 加外部边界替身：

- 断线期间完成/失败/中断，迟到的完成通知去重，新 turn 不被旧 turn 清除，缺失 turn 的一次性历史查询。
- 先 attach、先显示审批、后出现并行工具输出，等待超过两种超时时间后仍能 steer，解决请求后继续完成、查看 history 和 detach。
- legacy 模式下 A 增量、A 完成、B 增量，A 不再以进行中复现，覆盖最终结果为空。
- 仅补发 EventResult 仍能释放排队任务，后续任务与历史正常。
- 两个问答开关的四种组合及配置复制、类型检查、默认值。

相关旧回归与 CUJ、定向 race 检查一并运行。未跑全仓库测试，未调用真实模型或操作真实 daemon。
使用临时 Go overlay 撤回四项修复进行反证，四项新增回归均失败；工作区源码保持修复状态。

日志：dist/review-fixes-focused.log、dist/review-fixes-related.log、dist/review-fixes-race.log、dist/review-fixes-legacy.log；反证日志为 dist/review-fixes-prefixed-regressions.log。
