# 飞书流式显示配置

Feishu/Lark 平台可以配置 Card 2.0 富文本卡片的打字机动画速度和策略。这些参数仅传给流式卡片，配置位置是对应平台的 options：

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

这些选项可单独设置，未设置的选项沿用飞书默认值。`print_strategy` 不区分大小写，并会去除首尾空格；数值必须是范围内的整数，不接受字符串或浮点数。无效值会在平台初始化时返回错误。

配置仅影响正在流式更新的富文本卡片；最终完成的卡片不携带 `streaming_config`。因卡片大小限制压缩工具面板时，会保留流式配置。
