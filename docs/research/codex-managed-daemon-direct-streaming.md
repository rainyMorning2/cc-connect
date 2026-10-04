# 飞书直接展示流式正文

> 此文档保留 v7/v8 的历史设计。2026-10-03 按用户要求，当前源码已移除 direct 模式和
> `streaming_text_mode` 选项，保留 `print_frequency_ms`、`print_step`、`print_strategy`。
> 旧配置中遗留的 `streaming_text_mode` 不再生效；尚未重新打包，现有 v8 二进制仍支持该选项。

2026-10-03。已实现并打包到 v7；旧版 v6 没有此选项。

配置位于目标飞书平台的 `[projects.platforms.options]`：

```toml
streaming_text_mode = "direct"
```

默认 `typewriter` 保留原行为。新配置也适用于 Lark；只影响 rich 卡片正文。
`direct` 保留正文增量和同一卡片实时刷新，卡片 config.streaming_mode 为 false，
不调用原生流式文本元素 API，改由既有全卡更新接口展示收到的完整累计正文。
卡片实体仍走 CardKit 全卡替换；内联卡片仍走消息 Patch，不改变路由。
工作状态、工具/推理面板展开状态与最终全文保持原逻辑。

Engine 仍采用既有 rich 刷新条件：收到文本事件时，距上次刷新超过 200ms，
或累计增加超过 20 字节时刷新；最后一次完成事件强制补齐。这里不是固定逐字速度，
也不是每个 token 单独请求。实际刷新仍受事件到达、接口耗时和飞书客户端影响。

app-server stdio 与 managed 共用 Engine/平台卡片展示。stdio 初始化主动 opt out
item/agentMessage/delta，正文按完整消息块转换 EventText；managed 接收原生正文 delta。
旧 codex exec 也是按完整 agent_message item 处理。它们不会因平台选项而获得缺失的模型增量。
普通预览的 stream_preview 默认 interval_ms=1500、min_delta_chars=30、max_chars=2000；
rich 卡片正文使用上述独立刷新条件，改 stream_preview 不会调整打字机动画速度。

验证：选项默认值、合法/非法类型与值、直接/动画/最终卡片、面板状态、工具结果、
超长卡片压缩，以及 Engine 在结束前已全卡刷新多段正文；相关流式 CUJ/race。
未调用真实模型、真实 daemon 或飞书 API。日志：dist/direct-streaming-focused.log、dist/direct-streaming-race.log。
