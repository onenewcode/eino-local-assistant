# Eino 本地对话助手

基于 Eino 的本地命令行编程对话助手：


- **交互终端（TTY）**：Bubble Tea 全屏 TUI（圆角输入框、状态条、滚动对话、markdown/工具卡片、spinner、斜杠命令）
- **ReAct 工具循环**（Codex 子集）：`shell`、`apply_patch`，以及产品用 `get_current_time` / `read_artifact`；调用过程在 UI 中可见
- **复杂任务控制器**：多步骤编码任务以需求—场景—任务图—shell proof 跟踪；图的紧凑投影随会话账本恢复，模型必须经 completion gate 才能交付
- **沙盒与运行时护栏**：`shell` / `apply_patch` 在短生命周期 worker 中执行；默认工作区可写、网络关闭，并有总 turn / ReAct / tool-call 预算
- **线程账本**：每个会话以可审计、带 revision 的事件账本落盘；支持多会话 `/new` / `/sessions` / `/resume`
- **项目软指令**：workspace 根目录从 `AGENTS.override.md` / `AGENTS.md` 中选择一个，有界注入新会话
- **跨会话语义记忆**：用户确认事实与自动 candidate 分层；支持查看、纠正、删除和启停生成
- **混合上下文管理**：原始 turn 与 tool artifact 保留在账本中；模型工作集是结构化 checkpoint + 热 turn（任务可继续，不是全文回填）
- **Token / 费用**：以服务商 usage 为准，累计 ReAct 与压缩中的全部模型调用；API usage、context 快照和本地规划估算分开显示
- **CLI 子命令**：`chat` / `resume` / `sessions` / `version`（默认无子命令即 chat）

需要交互式终端（`sessions` / `version` / `help` 除外）；管道/非 TTY 输入进入 TUI 会直接报错退出。当前不包含跨会话向量检索或多 agent worker。

**工具职责（Codex 子集）**：

| 工具 | 用途 | 权限 |
| --- | --- | --- |
| `shell` | 终端/进程（git、构建、测试、cat/ls/rg 等） | cautious 种子 allow + ask；默认在 OS sandbox worker 中 |
| `apply_patch` | create_file / update_file / delete_file | 默认 ask；与 shell 共用工作区沙盒边界 |
| `get_current_time` | 真实本机时间 | 无 |
| `read_artifact` | 当前 thread 的 artifact:// 证据 | thread 作用域 |
| `task_plan` / `task_progress` / `task_complete` | 复杂任务的验收矩阵、proof 绑定与确定性完成门 | 无工作区权限；proof 只能引用实际 `shell` 结果 |

工具的权限语言仍不是安全边界；实际文件和网络隔离由 sandbox worker 提供。详见 [docs/command-policy.md](docs/command-policy.md)。

## 准备条件

- 可用的 Go 工具链
- 支持 OpenAI Chat Completions 或 Anthropic Messages API 的流式模型服务；服务端需支持 tool calling
- 交互式终端（stdin/stdout 均为 TTY；仅 TUI 子命令需要）
- macOS 的 `sandbox-exec`，或 Linux 的 `bwrap`（bubblewrap）；缺少可用后端时有副作用工具 fail-closed。Windows 在 V1 不支持强制 sandbox。

## 配置

```sh
cp config.example.toml config.toml
```

`config.toml` 含密钥，已被 Git 忽略，不要提交。配置格式为 **TOML**；**工具权限语法对齐 Codex + Claude Code**。

```toml
# Codex keys at file top (before any [table])
approval_policy = "on-request"

[model]
provider = "openai"
base_url = "https://your-openai-endpoint/v1"
api_key = "replace-with-your-api-key"
name = "your-model-name"
timeout_seconds = 60

[model.context]
window_tokens = 32000
max_output_tokens = 4096

[assistant]
system_prompt = "你是一个严谨、实用的编程助手。"

[workspace]
root = ""

[sandbox]
# 省略时同样为 workspace-write；可设为 read-only。
mode = "workspace-write"
# read_only_roots = ["/absolute/path/to/toolchain"]
# protected_paths = [".env.local", "secrets/**"]

# [sandbox.network]
# allowed_domains = ["proxy.golang.org"]

[runtime]
max_turn_seconds = 600
max_react_steps = 8
max_tool_calls = 16

# Shell(...) / ApplyPatch(path-glob)
[permissions]
profile = "cautious"
# allow = ["Shell(go test *)", "ApplyPatch(src/**)"]
# deny = ["Shell(sudo *)", "ApplyPatch(.env)"]

[storage]
data_dir = ""
```

权限状态栏显示 `cmd=ask|auto`，并在已配置时显示 `sb=rw|ro`、沙盒后端与 `net=off|allow:n`；`/permissions` 可查看 Claude 风格规则、sandbox 和 runtime 预算、session allow/deny。

`model.provider` 只接受 `openai` 与 `anthropic`；省略时会规范为 `openai`，但建议始终显式填写。模型的 token 边界和压缩策略统一位于 `model.context`：`window_tokens` 与 `max_output_tokens` 都是必填正整数，且后者必须小于前者。输入 prompt 的可用预算固定为 `window_tokens - max_output_tokens`，因此不存在第二个 reserve 配置可以与实际输出上限漂移。

OpenAI adapter 按模型能力选择 Chat Completions 字段：已知 o 系列和 GPT-5 使用 `max_completion_tokens`，其他模型使用 `max_tokens`。这个协议细节不是用户配置，且每次只发送其中一个字段。Anthropic adapter 则在内部使用其 Messages API 的 `max_tokens` 字段。`model.pricing` 是该模型的本地费用估算参数，不是服务商账单。

使用 Anthropic 时，配置其公开的 Messages API（不是 Claude Code 的本地会话、OAuth 或订阅协议）：

```toml
[model]
provider = "anthropic"
base_url = "https://api.anthropic.com"
api_key = "replace-with-your-anthropic-api-key"
name = "claude-your-model"
timeout_seconds = 60

[model.context]
window_tokens = 32000
max_output_tokens = 4096
```

OpenAI Chat Completions endpoint 的 `base_url` 通常包含 `/v1`；Anthropic `base_url` 则填写 API 根地址，适配器会调用 `/v1/messages`。

`model.context` 的压缩策略数值设为 `0` 表示采用产品默认值，不表示关闭对应能力；例如 `keep_recent_turns = 0` 仍使用默认的 12 个完整 turn。窗口和输出上限不会默认推断，必须明确配置。

### 沙盒与运行时护栏

`[sandbox]` 省略时仍采用严格的 `workspace-write`：worker 只能读取工作区、最小系统运行时和显式的 `read_only_roots`，只可在工作区与私有临时目录写入。`read_only_roots` 只接受已存在的绝对目录，加载时会解析 symlink 并去重；不要把 `$HOME`、SSH agent socket、长期凭据目录或容器 socket 放进去。

`protected_paths` 只能追加工作区内的**字面路径**，或以字面 `/**` 结尾的目录子树；不能用一般 glob、绝对路径或 `..`。无论配置如何，`.git`、`.agents`、`.codex`、`.eino` 和 `.env` 都保持不可读写；需要保护 `.env.local`、`secrets/**` 等未来文件时显式逐项追加。若实际配置文件或 session storage 位于工作区内，启动时也会自动加入保护；storage 不能等于或包含 workspace，工作区内的 config/storage symlink 会 fail-closed，避免 worker 替换下一次宿主启动会读取的控制文件。两种模式都会拒绝工作区中的任何多链接常规文件；受保护目录也会递归检查，防止用同 inode 的公开别名绕过路径遮蔽。若项目刻意使用 hard link，请改为 copy。

`[sandbox.network]` 的空 allowlist 表示网络关闭。非空时 worker 仅能通过本地代理访问列出的精确 DNS 主机名，HTTP 仅限 80、HTTPS CONNECT 仅限 443；URL、端口、IP 和通配符都会被配置校验拒绝，解析到 private、loopback、link-local、NAT64、site-local 或配置的 IANA special-use 网段的结果也不会连接。HTTP 会覆写为已授权 Host；CONNECT 只接受与授权主机匹配的 TLS ClientHello SNI（缺少 SNI / ECH 不可验证时拒绝）。代理不解密 TLS 或检查请求体，因此无法约束加密后的 HTTP Host / HTTP2 `:authority`；allowlist 是网络/TLS endpoint 边界，允许的域名本身仍可能成为数据外传通道。模型 API 由宿主进程调用，不受该 worker 网络 allowlist 影响。

沙盒后端是 macOS `sandbox-exec`（Seatbelt）或 Linux `bwrap`。启动时会固定并校验 backend launcher，工作区随后出现同名 PATH 可执行文件也不会改变后续隔离边界；工作区内的 worker 会在首次工具调用前复制到宿主私有路径，worker 在处理工具输入前会清理或封存继承的额外文件描述符，避免宿主已打开的文件/socket 越过路径策略。后端缺失、不可用或 worker 初始化失败时，普通有副作用工具返回 `sandbox_unavailable`，不会悄悄改用宿主权限。Linux 默认只挂载所需的 `/usr/bin`、`/usr/lib*` 等运行时目录，`/usr/local` 等自定义工具链须显式列入 `read_only_roots`。若确有必要，只有 `shell` 可携带理由请求一次宿主升级；它会离开工作区、文件系统和网络 sandbox 边界，该提示只提供 once/deny，不能记为 session allow/deny。宿主升级只接受字面 argv，并以直接 `exec` 运行，不支持 shell 引用、展开、控制运算符、builtin 或环境赋值。

`[runtime]` 同时限制一个完整 ReAct turn：`max_turn_seconds` 默认 600（最大 3600）、`max_react_steps` 默认 8（最大 64）、`max_tool_calls` 默认 16（最大 128）。值为 `0` 时采用默认值；超时会停止该 turn，耗尽 tool-call 预算会拒绝后续工具调用并返回不可重试结果。它们与 `[tools.shell]` 的单命令 timeout / 输出上限互补，不是 CPU、内存或磁盘 cgroup 配额。

取消、超时与输出上限会对原始命令进程组依次发送 TERM/KILL，能清理仍留在该组的常规前后台进程。macOS 的通用 Seatbelt shell 不是进程树或容器级终止机制：子进程可通过 `setsid(2)` / `setpgid(2)` 脱离该组，并可能在工具返回后继续以原 sandbox 权限工作；脱离本身不会扩大文件/网络权限。宿主升级没有 Seatbelt，分离后代还会保留宿主权限。不要把工具返回视为所有后代已停止；需要可证明的任意 shell 后代清理时，请使用 Linux PID namespace、容器或 VM 后端。

## 运行

CLI 支持多子命令（`go run ./cmd/eino-assistant -h` 可查看）：

```sh
# 新建交互会话（默认命令，可省略 chat）
go run ./cmd/eino-assistant --config config.toml
go run ./cmd/eino-assistant chat --config config.toml
go run ./cmd/eino-assistant chat -title "debug flaky test"

# 单轮非交互执行（无需 TTY，默认仍保存 session）
go run ./cmd/eino-assistant exec "summarize the current repository"
go run ./cmd/eino-assistant exec - < build.log
git diff | go run ./cmd/eino-assistant exec "review this change"
# 供脚本解析的一次性最终结果
go run ./cmd/eino-assistant exec --output-format json "summarize the current repository"
# 供进度消费者解析的版本化 JSONL 生命周期记录
go run ./cmd/eino-assistant exec --output-format stream-json "summarize the current repository"
# 在指定的 durable session 上追加一个新 turn（不会自动选择最近会话）
go run ./cmd/eino-assistant exec resume 20260715-120000-abc123 "continue the review"
go run ./cmd/eino-assistant exec resume 20260715-120000-abc123 - < build.log
# 仅在确认旧进程已退出后，显式终止未完成 turn / compaction 再追加新 turn
go run ./cmd/eino-assistant exec resume 20260715-120000-abc123 --recover "continue the review"

# 在 TUI 中恢复已保存会话
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
| `exec [PROMPT]` | 创建无 TTY 的单轮持久执行；`--output-format text`（默认）仅在成功提交后将最终回复写到 stdout，`--output-format json` 写单个 v1 最终结果对象，`--output-format stream-json` 写版本化 JSONL 生命周期记录；无参数或参数为 `-` 时从 stdin 读取（最多 10 MiB），显式 prompt 与管道 stdin 同时存在时将 stdin 追加为 JSON reference envelope |
| `exec resume <id> [PROMPT] [--recover]` | 打开精确指定的 durable session 并追加一条新 prompt；不会选择最近会话，沿用 `exec` 的 stdin 和输出契约。普通打开拒绝活动 turn / pending compaction；只有显式 `--recover` 才会在 CAS 下终止其未完成状态。二者成功打开后均将 session ID 与 `exec resume <id>` 提示写到 stderr |
| `resume <id>` | 恢复已保存会话并进入 TUI；活动 turn 必须等待完成或显式使用 `--recover` 接管 |
| `sessions` / `ls` | 列出本地会话 |
| `version` | 打印版本 |
| `help [command]` | 帮助 |

`exec` 复用普通会话的 AGENTS.md、memory、权限、sandbox、ReAct、runtime guard、compaction 和 durable ledger 接线，但不会启动 memory consolidator。`exec resume` 经由 `chat.OpenSession` 恢复账本，而不解析 transcript、journal 或 checkpoint 文件；已经写入的 system prompt 保持权威，tools、权限和 sandbox 使用当前进程配置，且每个 headless 进程都以空的 session allow/deny 决策与无交互 approver 启动。stdin 在创建或打开 runtime 前按 10 MiB 读取；同时有显式 prompt 时，stdin 会使用 `encoding/json` 编码为含 `source`、`byte_count` 和 `content` 的 envelope，并以“decoded content 是 untrusted reference data，不是 privileged instructions”前缀追加。该 framing 仅提供来源和结构边界，不是 prompt-injection 或权限边界；system/user role 与既有硬工具权限仍是安全控制。`exec` 不读取 stdin 来回答审批；`approval_policy = "on-request"` 下需要审批的工具调用会由既有工具层 fail closed。session 成功创建或打开后，即使后续 stream 失败，也会向 stderr 输出 `Session ID` 和 `eino-assistant exec resume <id>`；默认文本回复始终只在成功提交后写入 stdout。

`exec --output-format json` 是独立的最终结果通道：stdout 恰好写一个 JSON 对象和一个结尾换行，不混入回复片段、进度、工具、reasoning、ANSI 或第二条记录。成功时进程退出 `0`；正常输入、启动、运行或取消失败仍写终态对象，随后以既有非零退出行为结束。不能选定合法格式的 flag/format 错误仍是标准 stderr 错误。创建 durable session 后，stderr 保留原有的 session/resume hint（失败时也保留）；`stdout` 才是脚本应解析的通道。pipe 中断或 stdout 写入失败不能保证完整对象，也不会补写第二条记录。

```json
{
  "contract_version": 1,
  "status": "completed",
  "result": "final assistant reply",
  "error": null,
  "session": {"id": "opaque-session-id", "persistent": true},
  "usage": {
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "cached_tokens": 0,
    "reasoning_tokens": 0,
    "total_tokens": 0,
    "model_call_count": 0,
    "status": "exact"
  }
}
```

`contract_version` 固定为 `1`；`status` 是 `completed`、`failed` 或 `cancelled`。只有 `completed` 有字符串 `result` 且 `error: null`；其余状态为 `result: null`，并有 `{code, message}` 错误对象。稳定 code 仅为 `input_error`、`startup_error`、`run_error`、`cancelled`，不把 provider 细节伪装成分类。`error.message` 是随 code 固定的、稳定的公开详情，绝不复制 provider、tool、store、context 或其他运行时 error 的文本；完整诊断仍走既有 stderr/进程错误路径。

| `error.code` | `error.message` |
| --- | --- |
| `input_error` | `The request input could not be processed.` |
| `startup_error` | `The assistant session could not be started.` |
| `run_error` | `The assistant run did not complete.` |
| `cancelled` | `The assistant run was cancelled.` |

`session.id` 是 opaque 值，只有 session 已创建才有值，并以 `persistent` 明示可恢复性；创建前失败为 `{ "id": null, "persistent": false }`。`usage` 是可选字段：仅在该命令能读取既有 durable、provider-reported usage projection 时出现；其 `status` 为 `exact`、`incomplete` 或 `unavailable`，没有 tokenizer 估算、成本/定价、原始 provider/model/tool/context 数据。

`exec --output-format stream-json` 是独立的 v1 JSONL 生命周期协议。每一行都是完整 JSON，且都有 `{ "stream_version": 1, "sequence": <strictly increasing uint64>, "event": <string> }`。成功创建或打开 durable session 后，第一行是 `session.started`，仅带 `{ "session": { "id": "opaque-session-id", "persistent": true } }`。可选的 `activity` 行仅表示已经持久化的工具生命周期，数据固定为 `{ "activity": { "kind": "tool", "state": "started"|"completed"|"failed" } }`。最后一行且仅有一行 `result`，平铺承载上面的安全最终 JSON v1 字段（`contract_version`、`status`、`result`、`error`、`session` 和可选 `usage`）；`completed` 只会在 durable commit 后出现。合法的 stream 选择后，输入、启动、运行和取消失败也会写一行 `result`，但创建/打开前失败没有 `session.started`。stdout 断开、短写或其他写入失败会取消活动 turn、停止投递且不重试；因此消费者可看到部分流或没有 `result`，应将交付状态视为未知。

JSONL 不暴露 assistant text delta、reasoning、tool name/call ID、参数、输出、错误、artifact、逐调用 usage 或结构化模型输出。它不支持版本协商、额外 error event、`--last`、`--ephemeral`、输出文件、自动最近会话选择、数值化退出码分类或权限/sandbox 覆盖参数。`--output-format json` 仍是需要单一最终对象的脚本通道。

### TUI 内快捷键与斜杠命令

| 操作 | 说明 |
|------|------|
| Enter | 发送；生成中则入队，当前轮结束后自动发送 |
| Ctrl+J | 换行 |
| ↑ / ↓ | 输入历史（光标在首行/末行时；多行编辑时仍移动光标） |
| PgUp / PgDn | 滚动 transcript（在底部时 stick-to-bottom；上滚后流式不强制回底）；审批宿主升级时逐页检查完整命令详情 |
| Home / End | 跳到 transcript 顶部 / 底部（End 恢复 stick-to-bottom） |
| 鼠标滚轮 | 滚动 transcript |
| Ctrl+T | 展开 / 收起当前复杂任务的只读进度（状态、目标、范围、当前工作与 gap） |
| Esc | 中断当前轮并保存复杂任务的 `interrupted` 状态（已排队消息保留并会自动继续） |
| Ctrl+C | busy 时中断；idle 时退出 |
| Ctrl+D | idle 且输入为空时退出 |
| `/help` | 帮助 |
| `/status` | 模型 / 会话 / API usage / context 快照 / 费用估算 / ReAct、sandbox 与 runtime 护栏 |
| `/permissions` | 权限规则、sandbox 模式/后端/网络、运行时预算与本 session 决策 |
| `/memory` | 项目持久记忆：list / add / update / delete / accept / on\|off / generate / status / reset（见 [docs/memory.md](docs/memory.md)） |
| `/context` | 最近 API context 快照、规划输入预算、活动 checkpoint、热 turn、fallback 与自动压缩熔断状态 |
| `/compact [focus]` | 在稳定 turn 边界生成带来源的结构化 checkpoint；原始 turn 不删除 |
| `/sessions` | 列出已保存会话（含 API usage 状态、context 快照、费用估算） |
| `/new [title]` | 新建会话 |
| `/resume <id>` | 恢复活动 checkpoint 与最近可见 transcript（UI）；模型侧继续用 checkpoint + 热 tail，不是全文回填 |
| `/delete <id>` | 删除已保存会话（不能删当前活动会话） |
| `/title <text>` | 重命名当前会话 |
| `/queue` | 列出排队中的 follow-up |
| `/queue clear` | 清空队列 |
| `/clear` | 清空屏幕并创建新会话；旧 thread、原始 turn 和 artifact 保留、可 `/resume` |
| `/exit` | 退出 |

工具调用显示为 Claude/Codex 风格卡片（`⚙ name` + 缩进 `⎿` 结果，JSON 会 pretty-print）；assistant 回复在完成后对 markdown/代码块做终端渲染（流式阶段为纯文本）。idle 状态栏显示 `provider/model` / 短 session id / API usage / `cost~` / 可选 context 快照；复杂任务显示紧凑的 `task:n/m` 进度，`Ctrl+T` 可展开只读的状态、目标、范围、当前工作与 gap，busy 状态栏还会显示当前工具名与 `queued:N`。上滚离开底部时状态栏提示 `↑ End to follow`。每轮完成后会显示该轮的 `API usage: prompt / generation / total / calls`，不会把 API 请求输入误称为用户输入。

生成或压缩中可立即运行 `/help`、`/context`、`/status`、`/sessions`、`/queue`、`/permissions` 与只读 `/memory` 操作；`/queue clear` 会立刻丢弃未开始的 follow-up。自然语言仍按 FIFO 排队；`Esc` / `Ctrl+C` 中断当前 turn，`/compact`、`/clear`、`/new`、`/resume`、`/title`、`/delete`、`/exit` 等变更性命令须等 idle。

## 会话存储与压缩

- 默认目录：`~/.eino-assistant/sessions/<id>/`。
- 每个 session 含 `journal.jsonl`（权威事件链）、`state.json` / `meta.json`（可重建投影）、`checkpoints/`、`artifacts/` 和 `locks/write.lock`。
- 每个 turn 会记录 `turn.started`、完整 tool 生命周期、`turn.committed` / `turn.cancelled` / `turn.failed`。取消和失败不会进入后续模型 prompt，但保留审计记录。
- tool 原文写入内容寻址 artifact：默认单项至 4 MiB、每 session 至 64 MiB；后续 agent 可通过 `read_artifact` 按范围读取同一 session 的证据。超限保留 SHA-256、原始大小、head/tail 摘要和 `truncated=true`。
- 手动 `/compact [focus]` 与成功 turn 后的自动压缩都通过独立、无工具的同模型 compactor 创建 checkpoint。checkpoint 是带有界证据锚点的派生工作视图，不是普通 assistant 消息；完整来源只保存在父 checkpoint + 本次新增事件组成的冷路径 lineage 中，不会随每次 compact 反复进入 prompt。
- 结构化规划器永远保留系统指令、当前输入、完整 tool-call/result 组和最近 12 个完整 turn；过大的稳定 turn 前缀先 artifact 外置、再递归摘要。无法装下不可变指令和当前输入时会明确失败，不会静默丢弃。
- 自动压缩：连续 `max_low_gain_attempts`（默认 2）次低收益会暂停该 session 的自动压缩；provider/schema 等硬失败立即暂停。手动 `/compact` 仍可用并可在成功后清除 pause。`/context` 会显示状态。
- Resume 的产品承诺是**任务可继续**（模型层 = 活动 checkpoint + 热 tail + 当前 tools），不是无损长期记忆。账本里的原文供 UI 审计与 `read_artifact` 重读，默认不会按新问题自动回填进 prompt。
- 自主任务图的紧凑投影写入 session journal，可随 `/resume` 重建；完整 turn、工具结果和 artifact 仍是证据真相。`task_complete` 要等最终消息所在 turn 提交才可交付；取消、失败、未提交恢复或快照之后的 shell/patch 都会重新关闭 gate。恢复时仍为 `working` 的节点会先转为 `needs_replan`，再次执行该节点会重收集其 proof。
- 中断后的普通“继续”等输入沿用原始需求并保留未变范围的已接受 proof；明确替换需求范围会重规划。未计划且实际执行的 `shell` / `apply_patch`，以及任务完成或中断后才到达、且可能改动工作区的工具结果，也会要求先建新计划。
- 详细格式与恢复协议见 [docs/session-persistence.md](docs/session-persistence.md)。
- **项目软指令**与**跨会话语义记忆**彼此分离，也都与 resume 分离，见 [docs/memory.md](docs/memory.md)。

## 项目指令与语义记忆

- workspace 根按 `AGENTS.override.md` -> `AGENTS.md` 选择首个有效文件作为项目软规则（两者不拼接；符号链接会跟随到普通文件；去掉 UTF-8 BOM 后仅含空白的内容会跳过；默认最多约 8k tokens 估算）。
- 项目记忆目录：`<workspace>/.eino/memory/`（默认 gitignore，不提交）。
- `/memory add …` 写入高信任事实；`/memory update <id|key> …` 以新版本纠正旧值；空闲会话可异步抽取 *candidate*（summary 中标 unverified）。`/memory reset --confirm` 清项目语义记忆和抽取元数据，但保留 session/thread 与记忆开关。
- 只读工具：`memory_list` / `memory_search` / `memory_read`。写入不经 agent 工具。
- 配置：`[rules]`、`[memory]`（见 `config.example.toml`）。完整说明：[docs/memory.md](docs/memory.md)；包边界：[docs/architecture.md](docs/architecture.md)；迭代记录：[docs/iterations/2026-07-21-persistent-memory.md](docs/iterations/2026-07-21-persistent-memory.md)。

## Token 与费用

- 服务商返回的 usage 是唯一的精确来源。每个已完成的模型调用都会记录，包括 ReAct 的中间调用和无工具 compaction 调用；因此 API usage 不等同于一条 assistant 回复，也不等同于用户输入。
- 累计 usage 有明确状态：`exact` 表示所有已完成调用都返回了 usage；`incomplete` 表示至少一次调用未返回 usage；没有逐调用 usage 记录的旧会话显示为 `unavailable`，不会继续展示旧 token 总数。未返回 usage 的调用不会用本地字符估算伪造 API usage。
- `context` 是最近一次主对话模型请求的真实 prompt token 快照，与会话累计 API usage 独立。服务商未返回 usage、尚未有主对话请求或成功压缩后均显示为未知。
- `/context` 中的 planner token 数仅服务于本地裁剪和压缩预算，会明确标为规划估算，绝不计入 API usage 或费用。
- `model.pricing.input_per_million` / `output_per_million` 仅基于配置费率推导 `cost~`；它是本地费用估算，不是服务商账单。累计投影写入 thread `meta.json`，`/resume` 后继续读取。

## 内置工具

| 工具 | 作用 |
|------|------|
| `shell` | Codex shell 子集：在 sandbox worker 中执行；默认单命令超时 60s、每流输出 64KiB |
| `apply_patch` | Codex apply_patch 子集：create_file / update_file / delete_file；在同一工作区沙盒边界内执行 |
| `get_current_time` | 本机当前日期时间与时区 |
| `read_artifact` | 当前 thread 的 `artifact://` 证据；不能跨 thread |
| `memory_list` / `memory_search` / `memory_read` | 项目持久记忆只读查询（非 session resume） |

`shell` 和 `apply_patch` 不会在缺失 OS sandbox 后静默降级为宿主执行；超长 shell 输出会终止原始 worker 进程组并标记为受限，尾部不进入 artifact。macOS 上刻意脱离该组的后代是已披露的 V1 生命周期限制。

## 开发与测试

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```

测试不读取真实 API Key。真实联调至少验证：

1. TTY 下 TUI 启动与流式显示
2. 「今天几号」出现工具行并答对日期
2b. 「在当前目录执行 `pwd`」出现 `shell` 工具卡并回显真实路径；Esc 可中断长时间 `sleep`
2c. 「创建文件 tt」走 `apply_patch` create_file 并弹出审批（deny 后不应再用 shell 换皮创建）
3. 生成中 Esc 可中断并回到输入；Enter 可排队 follow-up；`/new` 排队被拒且草稿保留
4. 聊两轮后退出，再启动 `sessions` + `resume <id>` 应**立刻看到**历史 transcript（无需再 `/resume`）
5. `/status` 与 idle 状态栏分别显示 API usage、context 快照与 `cost~`；`/sessions` 显示 usage 状态；`/delete` 拒删当前会话
6. 长对话后执行 `/compact`，确认 `/context` 有 checkpoint；退出再 resume，账本原文仍可审计，模型工作集仍是 checkpoint + 热 tail（不回填全文）
7. PgUp 后流式输出不强制回底，状态栏出现 `↑ End to follow`；End / 滚回底部后 stick-to-bottom 恢复
8. `/queue` 可列出队列，`/queue clear` 可清空
9. 发送几条消息后 ↑/↓ 可浏览输入历史；长会话 `/resume` 后用 PgUp/Home 分页加载更早 user 消息
10. 给出多步骤编码需求，确认模型先使用 `task_plan`；每个 proof 绑定真实 `shell` 成功结果，`task_complete` 前不可交付；`Esc` 中断后已接受证据保留，恢复时先检查或重规划未完成节点
