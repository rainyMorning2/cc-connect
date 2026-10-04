# 飞书流式显示配置

Feishu/Lark 平台新增流式文本显示参数，可控制卡片或消息逐步上屏的速度和策略。配置位置是对应 Feishu 平台的 options：

```toml
[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
print_frequency_ms = 20
print_step = 2
print_strategy = "fast"
```

| 配置项 | 有效范围/默认行为 | 说明 |
|---|---|---|
| `print_frequency_ms` | `20–1000` ms；未配置使用 Feishu 默认值 | 两次上屏之间的间隔。 |
| `print_step` | `1–1000`；未配置使用 Feishu 默认值 | 每次上屏的字符数。 |
| `print_strategy` | `fast` 或 `delay`；未配置使用 Feishu 默认值 | `fast` 立即刷新待显示文本；`delay` 将文本排队后再显示。 |

