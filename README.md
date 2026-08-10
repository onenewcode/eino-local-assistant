# Eino 本地对话助手

基于 Eino 的本地命令行编程对话助手：


- **交互终端（TTY）**：Bubble Tea 全屏 TUI（圆角输入框、状态条、滚动对话、markdown/工具卡片、spinner、斜杠命令）
- **ReAct 工具循环**（Codex 子集）：`shell`、`apply_patch`，以及产品用 `get_current_time` / `read_artifact`；调用过程在 UI 中可见
- **任务清单（Codex 风格）**：多步骤工作可用 `update_plan` 维护 checklist；仅用于进度展示，不挡交付，不授予写权限
- **沙盒与运行时护栏**：`shell` / `apply_patch` 可按需在短生命周期 worker 中执行；默认关闭 OS sandbox，网络在所有模式下开放，并有总 turn / 模型决策 / tool-call 预算；显式 `--yolo`/`/permissions yolo` 会旁路 approval 与 OS sandbox，只保留尽力而为的命令和路径检查，不能作为安全边界
- **线程账本**：每个会话以可审计、带 revision 的事件账本落盘；支持多会话 `/new` / `/sessions` / `/resume`，并可通过 `internal/chat.Session.Fork`、TUI `/fork` 或 idle 两阶段 `Esc` backtrack 从 committed 前缀创建 source-preserving child
- **用户与项目软指令**：从 `~/.eino-assistant/AGENTS.override.md` / `AGENTS.md` 选择用户指令，再加载 workspace 项目指令，有界注入新会话；TUI `/rules` 只查看当前 session 创建时捕获的 source metadata，不 reload 文件
- **旁路问题（安全子集）**：TUI `/btw <question>`（`/side` 别名）使用当前 frozen session context 做 reference；不打断、不排队主 turn，不写主 ledger、usage 或 journal，不调用工具/子 agent
- **跨会话语义记忆**：用户确认事实与自动 candidate 分层；支持查看、纠正、删除和启停生成
- **混合上下文管理**：原始 turn 与 tool artifact 保留在账本中；模型工作集是结构化 checkpoint + 热 turn（任务可继续，不是全文回填）
- **Token / 费用**：以服务商 usage 为准，累计 ReAct 与压缩中的全部模型调用；API usage、context 快照和本地规划估算分开显示
- **进程可观测性**：标准库 `log/slog` 结构化日志，默认持久化到 `~/.eino-assistant/logs/eino-YYYY-MM-DD.log`（见 [docs/logging.md](docs/logging.md)）；会话账本仍是 resume 真源
- **CLI 子命令**：`chat` / `resume` / `sessions` / `version`（默认无子命令即 chat）

需要交互式终端（`sessions` / `version` / `help` 除外）；管道/非 TTY 输入进入 TUI 会直接报错退出。当前不包含跨会话向量检索或多 agent worker。

## 安装

在 macOS 或 Linux 上，从仓库根目录执行：

```sh
./scripts/install.sh
```

脚本会为当前系统编译 `cmd/eino-assistant` 并安装为 `/usr/local/bin/eino`；该目录不可写时会请求
`sudo` 权限。首次安装还会创建用户级配置 `~/.eino-assistant/config.toml`（权限为 `0600`），已有
配置绝不覆盖。先填写其中的 `[model]`，再直接运行：

```sh
eino version
eino
```

若要安装到用户目录而不使用 `sudo`，请指定一个已在 `PATH` 中的目录，例如：

```sh
./scripts/install.sh --install-dir "$HOME/.local/bin"
```

若目标目录不在 `PATH` 中，脚本会输出需添加到 shell profile 的命令。

## 内置工具

| 工具 | 用途 | 权限 |
| --- | --- | --- |
| `shell` | 终端/进程（git、构建、测试、cat/ls/rg 等） | Codex `.rules` + 保守已知只读回退 + approval；默认在 OS sandbox worker 中 |
| `apply_patch` | create_file / update_file / delete_file | 默认 ask；与 shell 共用工作区沙盒边界 |
| `get_current_time` | 真实本机时间 | 无 |
| `read_artifact` | 当前 thread 的 artifact:// 证据 | thread 作用域 |
| `update_plan` | 多步骤 checklist（pending / in_progress / completed，至多一个 in_progress） | 进度 UI only；不挡交付；写入仍由 permissions/sandbox 管 |

工具的权限语言仍不是安全边界；实际文件和网络隔离由 sandbox worker 提供。


## 配置

运行配置始终从 `~/.eino-assistant/config.toml` 加载，和当前工作目录无关；项目内的
`config.toml` 不会被自动读取，也不能覆盖模型或 API key。安装脚本会在该文件不存在时复制
`config.example.toml` 作为模板。未使用安装脚本时，可手动执行：

```sh
mkdir -p ~/.eino-assistant
install -m 600 config.example.toml ~/.eino-assistant/config.toml
```

全局 `config.toml` 含密钥，不要提交或复制到项目中。它也保存项目规则信任记录，例如：

```toml
[projects."/absolute/workspace"]
trust_level = "trusted"
```

配置格式为 **TOML**；shell 授权使用
Codex-compatible Starlark `.rules` 前缀规则子集，不再支持 TOML `[permissions]`。首次运行
会在 `~/.eino-assistant/rules/default.rules` 初始化 Eino 自有的零授权说明模板，已有用户
文件绝不覆盖；它不复制 Codex 用户规则或个人批准历史。项目规则须由用户目录配置中的
`[projects."/absolute/workspace"]` 显式标记为 `trusted` 才会读取。规则、命令影响分类和
sandbox 是独立层，详见 `docs/tool-policy.md`。


状态栏默认以紧凑的 `ask|auto|plan|yolo` 显示权限模式；启用 OS sandbox 时 `/permissions` 会追加 `sb=rw|ro`，yolo 会显示 `YOLO=UNSAFE`。沙盒后端、有效路径、保护路径和 runtime 预算放在 `/permissions`，网络不再作为状态栏或配置参数。`plan` 与 yolo 都只属于当前 TUI 进程的临时模式，不写配置或 session ledger；yolo 还会在启动/TUI transcript、`/permissions` 和工具结果中持续显示危险旁路。

### 显式 YOLO 权限模式

`--yolo` 是只允许交互 TUI 的危险启动参数，可用于裸命令、`chat`/`new` 和 `resume`；运行中的 idle TUI 也可显式执行 `/permissions yolo`。`Shift+Tab` 按 `ask -> auto -> plan -> yolo -> ask` 循环；每次进入 yolo 都会显示危险警告。headless `exec` 和 informational 子命令拒绝 `--yolo`，不会静默忽略。

yolo 同时绕过普通 approval/approver/session allow-deny 和 `shell`/`apply_patch` 的 OS sandbox worker，直接使用当前 host 的读写、进程执行和网络能力；这不表示 root、管理员或虚拟机外特权。approval bypass 与 sandbox bypass 是两个独立概念，本文实现明确同时开启二者。yolo 中仍会运行规则匹配、shell 工作目录与 `apply_patch` workspace/path/symlink 检查；但 `hardShellSafetyDeny` 只是字符串级的尽力而为防护，不是 shell 解析或宿主隔离，不能据此把 yolo 当作安全模式。普通模式 sandbox 不可用时仍返回 `sandbox_unavailable` 并 fail-closed，不会静默变成 yolo。

危险警告会写入启动 stderr，并显示在 TUI transcript、状态栏和 `/permissions` 报告中。成功的 shell/apply_patch yolo 结果带有 `SandboxOutcome` 元数据（`mode=yolo`、`backend=host`、`bypassed=true`），便于 tool lifecycle/artifact 审计。yolo 只存在当前进程，不写 TOML、session ledger 或 resume 数据。

`model.provider` 只接受 `openai` 与 `anthropic`；省略时会规范为 `openai`，但建议始终显式填写。`model.reasoning_effort` 省略时固定为 `medium`：启动、恢复会话和模型选择都会把该值作为实际请求传给 provider，并写入会话账本，而不是只在状态栏显示。`model.context` 只有一个设置：`window_tokens`，即模型的完整物理上下文窗口。它必须与实际部署相符；状态栏和 `/context` 都以这个完整窗口为分母，绝不会显示“窗口减输出预留”这一误导性的容量。

底部状态栏通过 `/statusline` 持久化选择字段和顺序：`model-with-reasoning`、`context-used`、`used-tokens`、`task-progress`、`activity` 与默认开启的 `mode`。其中 `activity` 独立显示 `thinking`、工具执行、流式输出、压缩和等待授权等即时状态；`mode` 仅显示当前 `ask|auto|plan|yolo`，两者都可按需关闭。状态栏颜色不提供单独配置。`/status` 只显示模型、推理强度和当前会话，API usage 在每轮 footer 或 `/sessions`，上下文与压缩诊断在 `/context`。

使用 Anthropic 时，配置其公开的 Messages API（不是 Claude Code 的本地会话、OAuth 或订阅协议）：

为什么没有 `max_output_tokens`、Anthropic 的必填 `max_tokens` 如何处理，以及固定的上下文/压缩与恢复合同，见 [上下文管理](docs/context-management.md)。`model.pricing` 只是本地费用估算，不是服务商账单。

### 沙盒与运行时护栏

沙盒、工具链可见性、保护路径、升级和 YOLO 边界见
[沙盒与运行时护栏](docs/sandbox.md)。

`[runtime]` 同时限制一个完整 agent turn：`max_turn_seconds` 默认 600（最大 3600）、`max_model_steps` 默认 8（最大 32）、`max_tool_calls` 默认 16（最大 128），以及 `max_consecutive_equivalent_tool_calls` 默认 3。一次 tool-enabled 模型响应只消耗一个 model step；其中的 0..N 条工具调用仍各自计入 tool-call 预算，并可并行执行。达到模型决策预算后，系统还会发起一次已解绑工具的最终答复请求；它不能再执行工具，也不消耗 `max_model_steps`。重复调用按工具名与规范化 JSON 参数识别；同一参数在实际工作区或外部状态变更前最多允许阈值次数执行，下一次重试会被拒绝并带 `stop_retrying`。重复调用阈值的非零值不得超过 `max_tool_calls`；省略或设为 `0` 时使用默认 3，但当 `max_tool_calls` 小于 3 时自动收紧到该总预算。每个完成工具结果都会持久化为会话 artifact；普通模型视图只接收有界预览和 artifact 引用，并行批次共享预览额度。artifact 本身不是工具调用，只有模型主动调用 `read_artifact` 才消耗一次 tool-call 预算。超时会停止该 turn，耗尽 tool-call 预算会拒绝后续工具调用并返回不可重试结果。它们与 `[tools.shell]` 的单命令 timeout / 输出上限互补，不是 CPU、内存或磁盘 cgroup 配额。
