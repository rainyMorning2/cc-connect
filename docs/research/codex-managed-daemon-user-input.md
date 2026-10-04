# Managed daemon 问答接入验证

日期：2026-10-01。当前接入的是独立 probe；CC Connect daemon backend 和飞书
链路仍待实现。现有 `agent/codex/appserver_session.go` 的 stdio backend 已有
`requestUserInput` → `AskUserQuestion` → answers 响应映射，正式集成时复用
这套交互，但必须补跨客户端状态同步与 thread 隔离。

## 协议和处理范围

官方 [App Server 文档](https://learn.chatgpt.com/docs/app-server#toolrequestuserinput)
说明问答通过 `item/tool/requestUserInput` 请求，回应或清理时发送
`serverRequest/resolved`。本次核对 `rust-v0.159.3` 的
`app-server-protocol/src/protocol/v2/item.rs`、`bespoke_event_handling.rs` 和
`tui/src/bottom_pane/request_user_input/mod.rs`。

- 请求含 threadId、turnId、itemId，以及每道题的稳定 ID、题目、选项、
  isOther、isSecret。probe 保留这些元数据，不按题目文本匹配答案。
- 响应为 `{"answers":{"问题ID":{"answers":["选项或文本"]}}}`，
  不发送 approval decision。回答可包含普通选项、自由文本或多个答案字符串。
- CLI Other 的线格式是 `None of the above` 标签和 `user_note: ...` 文本。
  probe 允许按原结构发送，不把字符串 `other` 转成审批决策。
- `isBlocking` 在该版本区分阻塞/非阻塞问答，缺省为 true；
  autoResolutionMs 已弃用，但仍保留。观察端不根据旧超时自动回答或跳过。
- 显式 skip 发送 `{"answers":{}}`。这只是提交无答案的问答结果，
  不调用 turn/interrupt；模型随后如何处理无答案由原任务决定。
- 每次完整 answer 必须包含请求中的所有问题 ID；错误 ID、空答案、
  approval decision 混用和已经 resolved 的请求被本地拒绝。
- 写入失败保留 pending；成功后本地防重复。其他客户端的 resolved、
  对应 turn 完成也清除 pending；无关 thread 和旧 turn 不污染当前状态。

当前 probe 没有分题草稿、附件或新 UI；用户显式提供协议答案。`isSecret`
被保留，回复成功标记不回显答案。正式平台展示和持久化仍需要考虑这一字段。

## 使用

```sh
go build -o /tmp/cc-connect-daemon-probe ./tools/codex-daemon-probe
/tmp/cc-connect-daemon-probe -watch <专用测试-thread-id> -interactive -timeout 15m
```

stdin 一行一个 JSON；替换 requestId 和问题 ID，保留 requestId 的原类型：

```json
{"action":"pending-questions"}
{"action":"answer","requestId":42,"answers":{"choice":{"answers":["A"]},"detail":{"answers":["None of the above","user_note: 希望使用另一种方案"]}}}
{"action":"skip","requestId":43}
{"action":"detach"}
```

`pending` 仍只列命令/文件审批，`pending-questions` 单独列问答。
detach 不回答问答、不终止原任务。

## 验证

新增 `-user-input-self-test`，被动 discovery 后创建独立 root thread 和
本机 Responses SSE 模拟模型；Plan 模式用于确定性产生阻塞问答。不调用
真实推理，不运行 shell，不修改文件，不操作已有 thread 或 daemon 生命周期。

真实 daemon 和 CLI 版本均为 `0.159.3`，以下检查通过：

1. 原 probe 发起 turn，产生包含两道题及 isOther 的请求。
2. 观察者中途 resume，恢复原活动 turn，并重放同一个问答请求 ID。
3. 观察者 detach/reconnect 后仍恢复同一个 turn 和请求。
4. 观察者回复普通选项及 Other 文本；收到 resolved，原 turn completed。
5. 模拟模型的紧接下一次请求确实含 Other 文本，而非仅 RPC 发送成功。
6. 第二个 turn 由原 probe skip；观察端通过 resolved 清除 pending，
   拒绝旧请求的再次回复；该 turn 正常完成，没有被中断。

结构化证据：[user-input-result.json](codex-managed-daemon-user-input-result.json)。
这是 **真实 daemon + 双 probe + 模拟模型** 的协议验证，尚未验证真实
CLI/Desktop 问答界面、非阻塞问答的真实运行行为或飞书问答卡片。

单元测试覆盖稳定 question ID、Other/中文文本、无选项自由文本、isSecret、
非阻塞/旧超时元数据、原 request ID 类型、发送失败、重复回复、外部 resolved、
thread 隔离、旧 turn 完成隔离，以及 interactive skip 的实际 WS 响应。

正式接入时需要把问答重放立即交给 Engine，收到外部 resolved 后撤销本地
等待状态和旧卡片；不能让现有 stdio 等待 goroutine 在超时后再次回复已解决
的共享请求。正式平台再补多题选择、Other 文本、外部回答与重连的 CUJ。

检查通过：`go build ./...`、`go test ./...`、probe race/vet 和真实 daemon
`-user-input-self-test -timeout 60s`。使用当前环境可写的 `/tmp` Go 缓存，
没有设置 GOTOOLCHAIN=local，没有修改全局配置。当前没有 agent/core 改动。
