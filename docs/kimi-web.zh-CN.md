# Kimi Web 共享会话（Kimi Code 2.2.0）

CC-Connect 可以作为 Kimi Web 服务的另一个客户端，与浏览器订阅同一个会话。
两端交替发送消息时，输出会同步到已接入该会话的聊天平台。
CC-Connect 不启动、停止、重启 Kimi 服务；`/detach`、关闭 CC-Connect 只断开观察连接。

## 启动与配置

先独立启动新版 Kimi Code，并在浏览器中完成模型配置：

```bash
kimi web --no-open
```

使用启动时显示的地址。默认端口是 58627；端口被占用时 Kimi 会选择其他端口，
需要相应修改 `web_url`。本功能面向新版 Kimi Code，不兼容旧 Python kimi-cli 的 Web API。

```toml
[[projects]]
name = "kimi-shared"

[projects.agent]
type = "kimi"

[projects.agent.options]
backend = "web"
work_dir = "/absolute/path/to/project"
web_url = "http://127.0.0.1:58627"
web_token_file = "/home/your-user/.kimi-code/server.token"
web_attach_only = true

# 按现有配置方式添加飞书、Telegram 等 platform。
```

`web_token_file` 默认读取 `$KIMI_CODE_HOME/server.token`；未设置 `KIMI_CODE_HOME`
时使用 `~/.kimi-code/server.token`。文件里只放服务访问 token，不是模型 API key。
每次 REST 请求和 WebSocket 重连都会重新读取文件，以便 token 轮换后重新接入。
连接另一台机器的服务时，需要在 CC-Connect 所在机器提供对应 token 文件。

省略 `backend` 或使用 `backend = "exec"` 时继续使用原来的 CLI 接入。
`web` 后端无需通过 PATH 找到 `kimi`，也不会调用 CLI。
模型、权限、Provider 在 Kimi 服务或浏览器中配置；此后端不接受
`model`、`mode`、`provider`、`env`、`timeout_mins` 覆盖。


## 操作

1. 在 Kimi Web 中打开工作目录对应的会话。
2. 在聊天平台发送 `/list`，再发送 `/attach <会话ID或序号>`。
3. 在浏览器发送消息，聊天平台会收到流式输出和最终回答。
4. 在聊天平台发送消息，浏览器也能看到同一会话中的执行和回答。
5. `/detach` 退出观察；浏览器会话继续运行。

| 功能 | 行为 |
|---|---|
| `/list` | 从服务读取会话，仅列出配置的 `work_dir` |
| `/attach`、`/switch` | 接入已有会话，校验工作目录，不修改其设置 |
| 普通消息 | 忙碌时排队，等待当前轮次结束 |
| `/new` 后发送消息 | `web_attach_only = false` 时可新建服务端会话；默认值为 false |
| `/history` | 读取服务端历史，包含其他客户端发起的对话 |
| `/stop` | 按当前 prompt ID 停止执行；旧轮次的停止请求不会取消新轮次 |
| 审批和问答 | 支持批准、会话级批准、拒绝、取消，以及结构化问题回答 |
| 图片 | 以 base64 图片内容发送给服务 |

另一客户端处理审批后，聊天端会收到已解决事件，旧审批不能变成普通聊天输入。
如果两端同时发送，Kimi 服务可能将其中一个 prompt 排队；CC-Connect 按 prompt ID
跟踪结果，不会因为浏览器先开始新轮次而重复提交本端消息。

### Steer 的原生语义

`/steer` 先提交一条 prompt，再尝试将这条 prompt 插入当前轮次。
两步之间发生轮次切换时，按 Kimi Web 的原生行为，指令可能插入新的轮次；
普通消息默认排队，不自动 steer。空闲时照常开始新轮次。

聊天端分别反馈已插入、仍排队、已独立开始执行、已处理或结果未确认。
同一条指令不会因为注入失败而自动重新提交；收到结果未确认提示时，先查看会话。
`/steer` 仅接受文本；文件和图片请通过普通消息发送，带附件的 `/steer` 会明确提示。

## 恢复与范围

连接断开后最多重试三次，读取完整 snapshot 恢复当前轮次与待处理交互，
再从 snapshot 的 `seq` / `epoch` 订阅后续事件。正在观察的轮次如果在断线期间完成，
会读取该 prompt 的历史恢复最终回答。流式增量可能不持久化，因此最终回答以服务历史为准。
长时间离线期间其他客户端新发起并完成的历史可通过 `/history` 查看。

本版暂未提供普通文件上传、后台终端控制与聊天端模型/权限切换。

官方协议仍是实验性接口；升级 Kimi 后以该服务的 `/openapi.json`、`/asyncapi.json` 为准。
参考：[Kimi Server API](https://www.kimi.com/code/docs/en/kimi-code-cli/reference/server-api.html)。

## 验证

HTTP/WebSocket 测试覆盖双客户端、审批问答、断线恢复、历史和退出。
通过 TestWebLiveTwoObservers 可对独立测试服务验证真实 Kimi 2.2.0 的连接协议。
