# CC-Connect Development Guide

## Project Overview

CC-Connect is a bridge that connects AI coding agents (Claude Code, Codex, Gemini CLI, Cursor, etc.) with messaging platforms (Feishu/Lark, Telegram, Discord, Slack, DingTalk, WeChat Work, QQ, LINE). Users interact with their coding agent through their preferred messaging app.

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   cmd/cc-connect                │  ← entry point, CLI, daemon
├─────────────────────────────────────────────────┤
│                     config/                     │  ← TOML config parsing
├─────────────────────────────────────────────────┤
│                      core/                      │  ← engine, interfaces, i18n,
│                                                 │     cards, sessions, registry
├──────────────────────┬──────────────────────────┤
│     agent/           │      platform/           │
│  ├── claudecode/     │  ├── feishu/             │
│  ├── codex/          │  ├── telegram/           │
│  ├── cursor/         │  ├── discord/            │
│  ├── gemini/         │  ├── slack/              │
│  ├── iflow/          │  ├── dingtalk/           │
│  ├── opencode/       │  ├── wecom/              │
│  ├── acp/            │  ├── qq/                 │
│  └── qoder/          │  ├── qqbot/              │
│                      │  ├── line/               │
│                      │  ├── weibo/              │
│                      │  └── cloud-web/          │
├──────────────────────┴──────────────────────────┤
│                     daemon/                     │  ← systemd/launchd service
└─────────────────────────────────────────────────┘
```

### Key Design Principles

**`core/` is the nucleus.** It defines all interfaces (`Platform`, `Agent`, `AgentSession`, etc.) and contains the `Engine` that orchestrates message flow. The core package must **never** import from `agent/` or `platform/`.

**Plugin architecture via registries.** Agents and platforms register themselves through `core.RegisterAgent()` and `core.RegisterPlatform()` in their `init()` functions. The engine creates instances via `core.CreateAgent()` / `core.CreatePlatform()` using string names from config.

**Dependency direction:**
```
cmd/ → config/, core/, agent/*, platform/*
agent/*   → core/   (never other agents or platforms)
platform/* → core/  (never other platforms or agents)
core/     → stdlib only (never agent/ or platform/)
```

### Core Interfaces

- **`Platform`** — messaging platform adapter (Start, Reply, Send, Stop)
- **`Agent`** — AI coding agent adapter (StartSession, ListSessions, Stop)
- **`AgentSession`** — a running bidirectional session (Send, RespondPermission, Events)
- **`Engine`** — the central orchestrator that routes messages between platforms and agents

Optional capability interfaces (implement only when needed):
- `CardSender` — rich card messages
- `InlineButtonSender` — inline keyboard buttons
- `ProviderSwitcher` — multi-model switching
- `DoctorChecker` — agent-specific health checks
- `AgentDoctorInfo` — CLI binary metadata for diagnostics

## Development Rules

### 1. No Hardcoding Platform or Agent Names in Core

The `core/` package must remain agnostic. Never write `if p.Name() == "feishu"` or `CreateAgent("claudecode", ...)` in core. Use interfaces and capability checks instead:

```go
// BAD — hardcodes platform knowledge in core
if p.Name() == "feishu" && supportsCards(p) {

// GOOD — capability-based check
if supportsCards(p) {
```

```go
// BAD — hardcodes agent type
agent, _ := CreateAgent("claudecode", opts)

// GOOD — derives from current agent
agent, _ := CreateAgent(e.agent.Name(), opts)
```

### 2. Prefer Interfaces Over Type Switches

When behavior differs across platforms/agents, define an optional interface in core and let implementations opt in:

```go
// In core/
type AgentDoctorInfo interface {
    CLIBinaryName() string
    CLIDisplayName() string
}

// In agent/claudecode/
func (a *Agent) CLIBinaryName() string  { return "claude" }
func (a *Agent) CLIDisplayName() string { return "Claude" }

// In core/ — query via interface, fallback gracefully
if info, ok := agent.(AgentDoctorInfo); ok {
    bin = info.CLIBinaryName()
}
```

### 3. Configuration Over Code

- Features that may vary per deployment should be configurable in `config.toml`
- Use `map[string]any` options for agent/platform factories to stay flexible
- Add new config fields with sensible defaults so existing configs don't break

### 4. High Cohesion, Low Coupling

- Each `agent/X/` package is self-contained: it handles process lifecycle, output parsing, and session management for agent X
- Each `platform/X/` package is self-contained: it handles API connection, message receiving/sending, and card rendering for platform X
- Cross-cutting concerns (i18n, cards, streaming, rate limiting) live in `core/`

### 5. Error Handling

- Always wrap errors with context: `fmt.Errorf("feishu: reply card: %w", err)`
- Never silently swallow errors; at minimum log them with `slog.Error` / `slog.Warn`
- Use `slog` (structured logging) consistently; never `log.Printf` or `fmt.Printf` for runtime logs
- Redact tokens/secrets in error messages using `core.RedactToken()`

### 6. Concurrency Safety

- Agent sessions are accessed from multiple goroutines; protect shared state with `sync.Mutex` or `atomic` types
- Use `context.Context` for cancellation propagation
- Channels should have clear ownership; document who closes them
- Prefer `sync.Once` for one-time teardown (`pendingPermission.resolve()`)

### 7. i18n

All user-facing strings must go through `core/i18n.go`:
- Define a `MsgKey` constant
- Add translations for all supported languages (EN, ZH, ZH-TW, JA, ES)
- Use `e.i18n.T(MsgKey)` or `e.i18n.Tf(MsgKey, args...)`

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `strings.EqualFold` for case-insensitive comparisons
- Avoid `init()` for anything other than platform/agent registration
- Keep functions focused; extract helpers when a function exceeds ~80 lines
- Naming: `New()` for constructors, `Get/Set` for accessors, avoid stuttering (`feishu.FeishuPlatform` → `feishu.Platform`)

## Testing

### Requirements

- All new features must include unit tests.
- **All bug fixes MUST include a regression test in the same PR.** A bug
  fix PR without a test that fails on the pre-fix code and passes on the
  fixed code will not be merged. Name regression tests so the bug is
  searchable later, e.g. `TestSwitchToAgentSession_PreservesHistory` for
  the cmdSwitch history-loss bug.
- Tests must pass before committing: `go test ./...`.
- Changes that touch a Critical User Journey (CUJ) — see
  `core/cuj_test.go` — should explicitly run `go test ./core/ -run TestCUJ`
  before opening the PR.

### Running Tests

```bash
# Full test suite
go test ./...

# Specific package
go test ./core/ -v

# Run specific test
go test ./core/ -run TestHandlePendingPermission -v

# Run Critical User Journey tests (recommended for any core/engine.go or
# core/session.go change)
go test ./core/ -run TestCUJ -v

# With race detector (CI)
go test -race ./...
```

### Test Patterns

- Use stub types for `Platform` and `Agent` in core tests (see `core/engine_test.go`).
- Test card rendering by inspecting the returned `*Card` struct, not JSON.
- For agent session tests, simulate event streams via channels.
- **For multi-step user behavior, add a CUJ test in `core/cuj_test.go`.**
  CUJ tests assert what a USER sees on the platform side across multiple
  actions (e.g. "create s1 → chat → /new s2 → /switch s1 → /history
  must show s1's content"). They exist because per-function unit tests
  can all pass while a user journey is still broken — the `/switch
  loses history` bug shipped in exactly that scenario despite full
  unit coverage of every individual function involved.

### Critical User Journeys (CUJ)

A CUJ test is a USER-perspective end-to-end scenario, not a developer-
perspective unit test. The current inventory of CUJs and their coverage
status lives in:

`projects/cc-connect/agents/qa-cursor/release-gate/CUJ-INVENTORY.md`
(in the spaceship agency workspace; the registered authoritative copy).

Rules for adding/updating CUJ tests in `core/cuj_test.go`:

1. Name: `TestCUJ_<group><id>_<short_camel_case>` (e.g. `TestCUJ_B3_SwitchPreservesHistory`).
2. Use real `SessionManager` + real `Engine`; mock only external boundaries (`Platform` sender, `Agent` process).
3. Drive the engine via `ReceiveMessage` — the same entrypoint platforms use, so engine/platform wiring is also covered.
4. Assert what the USER sees via `p.getSent()`, not internal struct fields.
5. ≥3 user actions per CUJ. A single-action assertion belongs in a unit test, not a CUJ.

When a user-reported bug maps to an existing CUJ, add a sub-case to that
CUJ rather than creating a new one.

## Selective Compilation

Each agent and platform is imported via a separate `plugin_*.go` file with a
build tag (e.g. `//go:build !no_feishu`). By default **all** agents and
platforms are compiled in.

### Include only specific agents/platforms

```bash
# Only Claude Code agent + Feishu and Telegram platforms
make build AGENTS=claudecode PLATFORMS_INCLUDE=feishu,telegram

# Multiple agents
make build AGENTS=claudecode,codex PLATFORMS_INCLUDE=feishu,telegram,discord
```

### Exclude specific agents/platforms

```bash
# Exclude some platforms you don't need
make build EXCLUDE=discord,dingtalk,qq,qqbot,line
```

### Direct build tag usage (without Make)

```bash
go build -tags 'no_discord no_dingtalk no_qq no_qqbot no_line' ./cmd/cc-connect
```

Available tags: `no_acp`, `no_claudecode`, `no_codex`, `no_copilot`, `no_cursor`, `no_gemini`,
`no_iflow`, `no_opencode`, `no_qoder`, `no_feishu`, `no_telegram`,
`no_discord`, `no_slack`, `no_dingtalk`, `no_wecom`, `no_weixin`, `no_qq`, `no_qqbot`,
`no_line`, `no_weibo`, `no_tuitui`.

## 本地打包与运行入口（local 分支）

本机本地实验的打包产物统一放在 `/home/daizhen/.local/share/cc-connect/builds`，
按版本和提交保存独立二进制。命名格式为：

```text
cc-connect-<基础版本>-local-<8 位 commit ID>
```

已有产物示例：`cc-connect-v1.5.1-beta.1-local-0383316d`、
`cc-connect-v1.5.1-beta.2-local-0562d48c`。用户明确指定完整版本时按指定值打包；
未指定时，以 Makefile 的 `VERSION` 确定版本系列（例如 `v1.5.1`），
扫描 builds 目录中同系列的 `beta.N` 本地产物，使用最大 `N + 1`。
没有同系列产物时，使用 Makefile 的 `VERSION` 作为首次版本。
**每次新的本地 beta 打包都要递增 beta 序号，不要因为 Makefile 仍是 beta.1
就反复生成 beta.1。** 例如已有 `v1.5.1-beta.4-local-...`，下一次为
`v1.5.1-beta.5-local-<本次提交号>`；只递增 beta 序号，不擅自改变主、次、修订版本。
非 beta 版本没有可递增序号时，按用户指定值或 Makefile 的 `VERSION` 使用。
构建时传入的 `VERSION` 为 `<基础版本>-local-<8 位 commit ID>`，确保
`--version` 第一行也包含提交号；`COMMIT` 使用同一个实际构建提交号，
产物文件名为 `cc-connect-${VERSION}`。不要覆盖已有产物；有未提交源码修改时先说明，避免把不同源码
打包成同一版本、同一提交号的文件。源码修复和打包说明的修改应先提交，
再使用最后的实际提交号打包，避免产物不包含说明中描述的改动。

在仓库根目录执行的打包示例（先扫描同系列产物；示例假定最大 beta 序号为 4）：

```bash
local_base_version='v1.5.1-beta.5'
local_build_commit=$(git rev-parse --short=8 HEAD)
local_build_version="${local_base_version}-local-${local_build_commit}"
local_build_dir='/home/daizhen/.local/share/cc-connect/builds'
local_build_binary="${local_build_dir}/cc-connect-${local_build_version}"

mkdir -p "$local_build_dir"
test ! -e "$local_build_binary" && make build \
  VERSION="$local_build_version" COMMIT="$local_build_commit" APP="$local_build_binary"
"$local_build_binary" --version
```

构建完成后确认 `--version` 的版本和提交号，并报告产物完整路径。
该目录在工作区之外，需要遵守当前环境的写入权限要求。

### cc-connect-local 软链接

`/home/daizhen/.local/bin/cc-connect-local` 是本地运行入口，指向上述 builds
目录中某一个明确选择的二进制。调用 `cc-connect-local` 会执行它当前指向的版本；
新产物放入 builds 目录不会自动启用。

- **打包后不要主动创建、更新或重定向这个软链接。** “打包”“构建”请求不代表授权切换运行版本。
- 每次交付打包产物时，都要读取并报告当前软链接目标，并提供切换到新产物的命令，提醒用户该操作尚未执行。
- **只有用户明确要求更新软链接后才执行修改。** 如未明确目标版本且有多个候选产物，先确认要启用哪个产物。
- 更新时使用产物的绝对路径，并保留旧二进制，便于按相同方式切回旧版本。
- 更新后用 `readlink -f` 和入口的 `--version` 验证目标、版本及提交号。

仅在用户明确授权后执行的切换示例：

```bash
ln -sfn -- "$local_build_binary" /home/daizhen/.local/bin/cc-connect-local
readlink -f /home/daizhen/.local/bin/cc-connect-local
/home/daizhen/.local/bin/cc-connect-local --version
```

其中 `local_build_binary` 必须是本次明确选择且已验证的产物路径；后续会话或新终端中
应重新设置这个变量，不能假定此前打包时的 shell 变量仍然存在。

### 重启本地服务

切换软链接后，用户明确要求重启时，统一使用本地入口：

```bash
cc-connect-local daemon restart
cc-connect-local daemon status
```

重启后确认服务处于运行状态，并核对进程实际使用的二进制为本次选定的产物。

## Pre-Commit Checklist

1. **Build passes**: `go build ./...`
2. **Tests pass**: `go test ./...`
3. **CUJ tests pass** (for any change in `core/engine.go`, `core/session.go`, `core/cron.go`, `core/timer.go`, or command handlers): `go test ./core/ -run TestCUJ`
4. **Bug fix has a regression test**: a new test in this PR that fails on the pre-fix code and passes on the fix.
5. **No new hardcoded platform/agent names in core**: grep for platform names in `core/*.go`.
6. **i18n complete**: all new user-facing strings have translations for all languages.
7. **No secrets in code**: no API keys, tokens, or credentials in source files.

## Adding a New Platform

1. Create `platform/newplatform/newplatform.go`
2. Implement `core.Platform` interface (and optional interfaces as needed)
3. Register in `init()`: `core.RegisterPlatform("newplatform", factory)`
4. Create `cmd/cc-connect/plugin_platform_newplatform.go` with `//go:build !no_newplatform` tag
5. Add `newplatform` to `ALL_PLATFORMS` in `Makefile`
6. Add config example in `config.example.toml`
7. Add unit tests

## Adding a New Agent

1. Create `agent/newagent/newagent.go`
2. Implement `core.Agent` and `core.AgentSession` interfaces
3. Register in `init()`: `core.RegisterAgent("newagent", factory)`
4. Create `cmd/cc-connect/plugin_agent_newagent.go` with `//go:build !no_newagent` tag
5. Add `newagent` to `ALL_AGENTS` in `Makefile`
6. Optionally implement `AgentDoctorInfo` for `cc-connect doctor` support
7. Add config example in `config.example.toml`
8. Add unit tests
