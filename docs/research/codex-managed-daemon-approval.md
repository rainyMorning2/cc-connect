# Managed daemon 审批决策核对

日期：2026-10-01。范围：接入 CC Connect 前，核对协议并补齐 probe 的一次性审批。

随后独立补齐了 probe 的问答 Other/answers/skip 及跨客户端状态验证，见
[问答接入验证](codex-managed-daemon-user-input.md)。下文保留审批阶段的范围。

## Other 是否存在

命令与文件审批没有名为 `other` 的决策。官方 [App Server 文档](https://learn.chatgpt.com/docs/app-server#approvals)
说明了审批请求、决策响应及 `serverRequest/resolved` 生命周期。
具体选项以版本源码和请求携带的 `availableDecisions` 为准。

本次下载并核对 `rust-v0.159.2` 与 `rust-v0.159.3` 官方源码，以下两个文件
在两个版本中完全一致：

- `codex-rs/app-server-protocol/src/protocol/v2/item.rs`：审批决策枚举。
- `codex-rs/tui/src/bottom_pane/approval_overlay.rs`：CLI 选项与决策映射。

| 决策 | 含义 | probe 当前支持 |
| --- | --- | --- |
| `accept` | 允许本次操作 | 是 |
| `decline` | 拒绝本次操作，agent 继续当前 turn | 是；有 availableDecisions 时必须被提供 |
| `cancel` | 拒绝本次操作，并中断当前 turn | 是；本次新增 |
| `acceptForSession` | 缓存会话内的审批许可 | 否；保留一次性审批范围 |
| `acceptWithExecpolicyAmendment` 对象 | 允许并修改命令前缀规则，命令审批专用 | 否；不修改持久规则 |
| `applyNetworkPolicyAmendment` 对象 | 持久允许或拒绝目标 host，命令审批专用 | 否；不修改持久规则 |

CLI 的 “No, and tell Codex what to do differently” 对应 `cancel`，
不是附带文本的 approval decision。后续指导是独立用户输入；probe 目前不会
自动发送后续 prompt。CLI 中 “No, continue without running it” 对应 `decline`。
文件审批协议也有 decline，但该版本 CLI 文件审批菜单仅显示 accept、
acceptForSession、cancel；协议支持与 UI 展示不等同。

真正的 Other/“None of the above” 属于 `item/tool/requestUserInput` 问答，
不是命令或文件审批。`request_user_input/mod.rs` 中 `is_other` 控制该选项，
官方测试 `is_other_adds_none_of_the_above_and_submits_it` 验证答案包含选项标签
以及 `user_note: Custom answer`。它使用按 question ID 组织的 answers 响应，
不能发送 `{"decision":"other"}`。该问答能力仍在后续接入范围，本次未实现。

## 实现和回归测试

probe 一次性审批现在支持 accept、decline、cancel，保持数字/字符串 request ID
原类型。请求提供 availableDecisions 时，不能回复未提供的决策；未知决策、
会话授权和持久规则修改继续被拒绝。发送失败保留 pending，发送成功后本地
防重复，其他客户端的 resolved 或对应 turn 完成也会清除 pending。

新增 `TestApprovalCancelOfferedDecisionUsesOriginalRequestID`，在旧代码上
命令和文件两项均失败（cancel 被拒绝），修复后通过。UDS WebSocket fixture
覆盖两种请求的 accept/decline/cancel 六种组合、resolved 以及重复回复拒绝。
另有回归检查确认未提供的 cancel、other 和 acceptForSession 不会发出响应。

interactive 使用实际 pending ID，例如：

```json
{"action":"reply","requestId":42,"decision":"cancel"}
```

cancel 与 detach 不同：前者终止目标 turn，后者只断开 probe 连接。

## 真实 daemon 验证

本机被动 discovery 报告 CLI 与 app-server 均为 `0.159.3`。扩展 `-self-test`
后，在新建的隔离 thread、本机模拟模型上验证：

1. 原 probe 发起新 turn，观察 probe 收到真实 command approval。
2. availableDecisions 为 accept、命令规则修改对象、cancel；没有 other，
   也没有 decline。观察者通过与 interactive 相同的校验/发送路径回复 cancel。
3. 原 probe 收到同一个 request ID 的 serverRequest/resolved。
4. 观察者收到同一个 turn ID 的 turn/completed，status 为 interrupted。
5. cancel 后没有继续请求模型；同一隔离 thread 可以开始后续测试 turn。

证据：[approval-result.json](codex-managed-daemon-approval-result.json)。原有
decline、重连、模拟 steer、interrupt 检查也通过。旧 self-test 的 decline
是直接协议响应，不走 availableDecisions 校验；它证明 runtime 的 decline
行为，不能据此在未提供 decline 的真实审批 UI 中显示该操作。

本次实际运行的是 **真实 daemon + 两个 probe + 模拟模型**。没有操作已有
thread 或 daemon 生命周期；没有执行待审批命令。它不代表真实 CLI 取消后的
界面实测，也不代表文件取消的真实 daemon 实测；文件路径使用本地协议测试
与源码映射核对。既有真实 CLI 双向允许审批的证据仍有效。

命令/文件一次性审批在 probe 的遗留支持已补齐。会话授权、持久规则修改、
问答 Other、permissions 请求和 MCP elicitation 是明确的其他能力，不应
混称为同一个审批 decision，也不被本次结果声称已实现。

## 检查结果

- `go build ./...`：通过。
- `go test ./...`：通过。
- `go test -race ./tools/codex-daemon-probe -count=1`：通过。
- `go vet ./tools/codex-daemon-probe`：通过。
- 真实 daemon `-self-test -timeout 60s`：通过，含新增 cancel。

当前执行环境的默认 Go 缓存目录不可写，本次命令仅临时设置
`GOCACHE=/tmp/cc-connect-go-build`、`GOMODCACHE=/tmp/cc-connect-go-mod`，
没有修改 Go 全局配置，没有设置 GOTOOLCHAIN=local。
