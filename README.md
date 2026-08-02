# Eino 本地对话助手

基于 Eino 的本地命令行编程对话助手：


- **交互终端（TTY）**：Bubble Tea 全屏 TUI（圆角输入框、状态条、滚动对话、markdown/工具卡片、spinner、斜杠命令）
- **ReAct 工具循环**：内置 `get_current_time`、`run_command`（本地 shell）与受当前 thread 限制的 `read_artifact`，调用过程在 UI 中可见
- **线程账本**：每个会话以可恢复、带 revision 的事件账本落盘；支持多会话 `/new` / `/sessions` / `/resume`
- **混合上下文管理**：保留完整原始 turn 和 tool artifact；模型只接收结构化 checkpoint + 热 turn 工作集
- **Token / 费用**：优先使用服务商 usage；否则本地估算；`/status`、状态栏与轮末摘要展示
- **CLI 子命令**：`chat` / `resume` / `sessions` / `version`（默认无子命令即 chat）

需要交互式终端（`sessions` / `version` / `help` 除外）；管道/非 TTY 输入进入 TUI 会直接报错退出。当前不包含专用文件修改工具、跨会话向量检索、长期记忆或多 agent worker。`run_command` 以当前用户权限执行本机 shell（默认开启、无交互审批）；可在配置中关闭。

## 准备条件

- 可用的 Go 工具链
- 支持 OpenAI Chat Completions 流式 SSE **且支持 function/tool calling** 的模型服务
- 交互式终端（stdin/stdout 均为 TTY；仅 TUI 子命令需要）

## 配置

```sh
cp config.example.yml config.yml
```

`config.yml` 含密钥，已被 Git 忽略，不要提交。

```yaml
model:
  base_url: "https://your-compatible-endpoint/v1"
  api_key: "replace-with-your-api-key"
  name: "your-model-name"
  timeout_seconds: 60

assistant:
  system_prompt: "你是一个严谨、实用的编程助手。"

storage:
  data_dir: ""   # 默认 ~/.eino-assistant

context:
  model_context_tokens: 32000
  output_reserve_tokens: 4096
  keep_recent_turns: 12
  auto_compact_trigger_percent: 75
  post_compact_target_percent: 45
  summary_max_tokens: 2048
  max_low_gain_attempts: 2
  low_gain_threshold_percent: 15

pricing:
  input_per_million: 0      # USD / 1M tokens；用于费用展示
  output_per_million: 0

# 可选。本地 shell 工具（默认开启）
tools:
  run_command:
    disabled: false
    timeout_seconds: 60      # 0 = 默认 60；上限 300
    max_output_bytes: 65536  # 0 = 默认 64KiB；stdout/stderr 各自上限
    working_dir: ""          # 空 = 进程 cwd
```

`context` 中的数值设为 `0` 表示采用产品默认值，不表示关闭对应能力；例如 `keep_recent_turns: 0` 仍使用默认的 12 个完整 turn。

## 运行

CLI 支持多子命令（`go run ./cmd/eino-assistant -h` 可查看）：

```sh
# 新建交互会话（默认命令，可省略 chat）
go run ./cmd/eino-assistant --config config.yml
go run ./cmd/eino-assistant chat --config config.yml
go run ./cmd/eino-assistant chat -title "debug flaky test"

# 恢复已保存会话
go run ./cmd/eino-assistant resume 20260715-120000-abc123
# 仅在确认旧进程已退出后，显式终止未完成 turn 并恢复
go run ./cmd/eino-assistant resume 20260715-120000-abc123 --recover

# 列出会话（无需 TTY）
go run ./cmd/eino-assistant sessions

# 版本 / 帮助
go run ./cmd/eino-assistant version
go run ./cmd/eino-assistant help resume
```

| 子命令 | 说明 |
|--------|------|
| `chat` / `new` | 新建交互会话（默认） |
| `resume <id>` | 恢复已保存会话并进入 TUI；活动 turn 必须等待完成或显式使用 `--recover` 接管 |
| `sessions` / `ls` | 列出本地会话 |
| `version` | 打印版本 |
| `help [command]` | 帮助 |

### TUI 内快捷键与斜杠命令

| 操作 | 说明 |
|------|------|
| Enter | 发送；生成中则入队，当前轮结束后自动发送 |
| Ctrl+J | 换行 |
| ↑ / ↓ | 输入历史（光标在首行/末行时；多行编辑时仍移动光标） |
| PgUp / PgDn | 滚动 transcript（在底部时 stick-to-bottom；上滚后流式不强制回底） |
| Home / End | 跳到 transcript 顶部 / 底部（End 恢复 stick-to-bottom） |
| 鼠标滚轮 | 滚动 transcript |
| Esc | 中断当前轮（已排队消息保留并会自动继续） |
| Ctrl+C | busy 时中断；idle 时退出 |
| Ctrl+D | idle 且输入为空时退出 |
| `/help` | 帮助 |
| `/status` | 模型 / 会话 / tokens / cost / context / max_step |
| `/context` | 输入预算、活动 checkpoint、热 turn、fallback 与自动压缩熔断状态 |
| `/compact [focus]` | 在稳定 turn 边界生成带来源的结构化 checkpoint；原始 turn 不删除 |
| `/sessions` | 列出已保存会话（含 tokens/cost） |
| `/new [title]` | 新建会话 |
| `/resume <id>` | 恢复 checkpoint + 最近 50 个可见 turn；向上滚动时按页加载更早 transcript |
| `/delete <id>` | 删除已保存会话（不能删当前活动会话） |
| `/title <text>` | 重命名当前会话 |
| `/queue` | 列出排队中的 follow-up |
| `/queue clear` | 清空队列 |
| `/clear` | 清空屏幕并创建新会话；旧 thread、原始 turn 和 artifact 保留、可 `/resume` |
| `/exit` | 退出 |

工具调用显示为 Claude/Codex 风格卡片（`⚙ name` + 缩进 `⎿` 结果，JSON 会 pretty-print）；assistant 回复在完成后对 markdown/代码块做终端渲染（流式阶段为纯文本）。idle 状态栏显示 model / 短 session id / tokens / cost / 可选 `ctx=%`；busy 状态栏显示当前工具名与 `queued:N`。上滚离开底部时状态栏提示 `↑ End to follow`。每轮成功后会显示 `tokens in/out`、费用与可选 `ctx=%`。

生成或压缩中可立即运行 `/help`、`/context`、`/status`、`/sessions`、`/queue`；`/queue clear` 会立刻丢弃未开始的 follow-up。自然语言仍按 FIFO 排队；`/compact`、`/clear`、`/new`、`/resume`、`/title`、`/delete`、`/exit` 等变更性命令须等 idle。

## 会话存储与压缩

- 默认目录：`~/.eino-assistant/threads/<id>/`。
- 每个 thread 含 `journal.jsonl`（权威事件链）、`state.json` / `meta.json`（可重建投影）、`checkpoints/`、`artifacts/` 和 `locks/write.lock`。
- 每个 turn 会记录 `turn.started`、完整 tool 生命周期、`turn.committed` / `turn.cancelled` / `turn.failed`。取消和失败不会进入后续模型 prompt，但保留审计记录。
- tool 原文写入内容寻址 artifact：默认单项至 4 MiB、每 thread 至 64 MiB；后续 agent 可通过 `read_artifact` 按范围读取同一 thread 的证据。超限保留 SHA-256、原始大小、head/tail 摘要和 `truncated=true`。
- 手动 `/compact [focus]` 与成功 turn 后的自动压缩都通过独立、无工具的同模型 compactor 创建 checkpoint。checkpoint 是带有界证据锚点的派生工作视图，不是普通 assistant 消息；完整来源只保存在父 checkpoint + 本次新增事件组成的冷路径 lineage 中，不会随每次 compact 反复进入 prompt。
- 结构化规划器永远保留系统指令、当前输入、完整 tool-call/result 组和最近 12 个完整 turn；过大的稳定 turn 前缀先 artifact 外置、再递归摘要。无法装下不可变指令和当前输入时会明确失败，不会静默丢弃。
- 连续两次自动压缩低于 15% 释放量会暂停该 thread 的自动压缩；手动 `/compact` 仍可使用。`/context` 会显示状态。
- 详细格式与恢复协议见 [docs/session-persistence.md](docs/session-persistence.md)。

## Token 与费用

- 优先读取最终 assistant 消息的 `ResponseMeta.Usage`
- 服务商未返回 usage 时，按**实际发送的 view** 粗估（状态中标记 `~est`）
- `pricing.input_per_million` / `output_per_million` 换算 USD；未配置则为 `$0`
- 累计 usage 写入 thread `meta.json`，`/resume` 后可继续累加

## 内置工具

| 工具 | 作用 |
|------|------|
| `get_current_time` | 本机当前日期时间与时区；可选 IANA 时区参数 |
| `run_command` | 通过 `sh -c` 执行本机 shell 命令；返回 exit code / stdout / stderr；默认超时 60s、输出各 64 KiB；非零退出为软结果；可用 `tools.run_command.disabled` 关闭 |
| `read_artifact` | 按 byte range 读取当前 thread 的 `artifact://sha256-...` 证据；默认 16 KiB、最多 64 KiB，不能跨 thread 读取 |

`run_command` v1 **无沙箱、无交互审批**，权限等同启动助手的本机用户。超长 stdout/stderr 会在工具结果中截断（`truncated=true`）；截掉的尾部不会进入 artifact，不要指望 `read_artifact` 恢复 cap 之外的输出，应缩小命令范围或提高 `max_output_bytes` 后重跑。

## 开发与测试

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```

测试不读取真实 API Key。真实联调至少验证：

1. TTY 下 TUI 启动与流式显示
2. 「今天几号」出现工具行并答对日期
2b. 「在当前目录执行 `pwd`」出现 `run_command` 工具卡并回显真实路径；Esc 可中断长时间 `sleep`
3. 生成中 Esc 可中断并回到输入；Enter 可排队 follow-up；`/new` 排队被拒且草稿保留
4. 聊两轮后退出，再启动 `sessions` + `resume <id>` 应**立刻看到**历史 transcript（无需再 `/resume`）
5. `/status` 与 idle 状态栏显示 tokens / context；`/sessions` 含 tokens/cost；`/delete` 拒删当前会话
6. 长对话后执行 `/compact`，确认 `/context` 有 checkpoint；退出再 resume，原始 thread 仍可恢复且模型工作集不回填全文
7. PgUp 后流式输出不强制回底，状态栏出现 `↑ End to follow`；End / 滚回底部后 stick-to-bottom 恢复
8. `/queue` 可列出队列，`/queue clear` 可清空
9. 发送几条消息后 ↑/↓ 可浏览输入历史；长会话 `/resume` 后用 PgUp/Home 分页加载更早 user 消息
