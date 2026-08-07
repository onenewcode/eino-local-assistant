# Eino 本地对话助手

基于 Eino 的本地命令行编程对话助手：


- **交互终端（TTY）**：Bubble Tea 全屏 TUI（圆角输入框、状态条、滚动对话、markdown/工具卡片、spinner、斜杠命令）
- **ReAct 工具循环**（Codex 子集）：`shell`、`apply_patch`，以及产品用 `get_current_time` / `read_artifact`；调用过程在 UI 中可见
- **复杂任务控制器**：多步骤编码任务以需求—场景—任务图—shell proof 跟踪；图的紧凑投影随会话账本恢复，模型必须经 completion gate 才能交付
- **沙盒与运行时护栏**：`shell` / `apply_patch` 在短生命周期 worker 中执行；默认工作区可写、网络关闭，并有总 turn / 模型决策 / tool-call 预算；显式 `--yolo`/`/permissions yolo` 会旁路 approval 与 OS sandbox，只保留尽力而为的命令和路径检查，不能作为安全边界
- **线程账本**：每个会话以可审计、带 revision 的事件账本落盘；支持多会话 `/new` / `/sessions` / `/resume`，并可通过 `internal/chat.Session.Fork`、TUI `/fork` 或 idle 两阶段 `Esc` backtrack 从 committed 前缀创建 source-preserving child
- **用户与项目软指令**：从 `~/.eino-assistant/AGENTS.override.md` / `AGENTS.md` 选择用户指令，再加载 workspace 项目指令，有界注入新会话；TUI `/rules` 只查看当前 session 创建时捕获的 source metadata，不 reload 文件
- **旁路问题（安全子集）**：TUI `/btw <question>`（`/side` 别名）使用当前 frozen session context 做 reference；不打断、不排队主 turn，不写主 ledger、usage 或 journal，不调用工具/子 agent
- **跨会话语义记忆**：用户确认事实与自动 candidate 分层；支持查看、纠正、删除和启停生成
- **混合上下文管理**：原始 turn 与 tool artifact 保留在账本中；模型工作集是结构化 checkpoint + 热 turn（任务可继续，不是全文回填）
- **Token / 费用**：以服务商 usage 为准，累计 ReAct 与压缩中的全部模型调用；API usage、context 快照和本地规划估算分开显示
- **CLI 子命令**：`chat` / `resume` / `sessions` / `version`（默认无子命令即 chat）

需要交互式终端（`sessions` / `version` / `help` 除外）；管道/非 TTY 输入进入 TUI 会直接报错退出。当前不包含跨会话向量检索或多 agent worker。

## 内置工具

| 工具 | 用途 | 权限 |
| --- | --- | --- |
| `shell` | 终端/进程（git、构建、测试、cat/ls/rg 等） | Codex `.rules` + 保守已知只读回退 + approval；默认在 OS sandbox worker 中 |
| `apply_patch` | create_file / update_file / delete_file | 默认 ask；与 shell 共用工作区沙盒边界 |
| `get_current_time` | 真实本机时间 | 无 |
| `read_artifact` | 当前 thread 的 artifact:// 证据 | thread 作用域 |
| `task_plan` / `task_progress` / `task_complete` | 复杂任务的验收矩阵、proof 绑定与确定性完成门 | 无工作区权限；proof 只能引用实际 `shell` 结果 |

工具的权限语言仍不是安全边界；实际文件和网络隔离由 sandbox worker 提供。


## 配置

```sh
cp config.example.toml config.toml
```

`config.toml` 含密钥，已被 Git 忽略，不要提交。配置格式为 **TOML**；shell 授权使用
Codex-compatible Starlark `.rules` 前缀规则子集，不再支持 TOML `[permissions]`。首次运行
会在 `~/.eino-assistant/rules/default.rules` 初始化 Eino 自有的零授权说明模板，已有用户
文件绝不覆盖；它不复制 Codex 用户规则或个人批准历史。项目规则须由用户目录配置中的
`[projects."/absolute/workspace"]` 显式标记为 `trusted` 才会读取。规则、命令影响分类和
sandbox 是独立层，详见 `docs/tool-policy.md`。


权限状态栏显示 `cmd=ask|auto|plan|yolo`；普通模式在已配置时显示 `sb=rw|ro`、沙盒后端与 `net=off|allow:n`，yolo 显示 `YOLO=UNSAFE`、`sb=off`、`sb_backend=host`、`net=host`。`/permissions` 可查看已加载的 Codex 规则来源、sandbox 和 runtime 预算、session allow/deny。`plan` 与 yolo 都只属于当前 TUI 进程的临时模式，不写配置或 session ledger；yolo 还会在启动/TUI transcript、`/permissions` 和工具结果中持续显示危险旁路。

### 显式 YOLO 权限模式

`--yolo` 是只允许交互 TUI 的危险启动参数，可用于裸命令、`chat`/`new` 和 `resume`；运行中的 idle TUI 也可显式执行 `/permissions yolo`。它不会加入 `Shift+Tab` 的 `ask -> auto -> plan -> ask` 循环；yolo 下必须显式执行 `/permissions ask|auto|plan` 才能离开。headless `exec` 和 informational 子命令拒绝 `--yolo`，不会静默忽略。

yolo 同时绕过普通 approval/approver/session allow-deny 和 `shell`/`apply_patch` 的 OS sandbox worker，直接使用当前 host 的读写、进程执行和网络能力；这不表示 root、管理员或虚拟机外特权。approval bypass 与 sandbox bypass 是两个独立概念，本文实现明确同时开启二者。yolo 中仍会运行规则匹配、shell 工作目录与 `apply_patch` workspace/path/symlink 检查；但 `hardShellSafetyDeny` 只是字符串级的尽力而为防护，不是 shell 解析或宿主隔离，不能据此把 yolo 当作安全模式。普通模式 sandbox 不可用时仍返回 `sandbox_unavailable` 并 fail-closed，不会静默变成 yolo。

危险警告会写入启动 stderr，并显示在 TUI transcript、状态栏和 `/permissions` 报告中。成功的 shell/apply_patch yolo 结果带有 `SandboxOutcome` 元数据（`mode=yolo`、`backend=host`、`network=host`、`bypassed=true`），便于 tool lifecycle/artifact 审计。yolo 只存在当前进程，不写 TOML、session ledger 或 resume 数据。

`model.provider` 只接受 `openai` 与 `anthropic`；省略时会规范为 `openai`，但建议始终显式填写。`model.context` 只有一个设置：`window_tokens`，即模型的完整物理上下文窗口。它必须与实际部署相符；状态栏和 `/context` 都以这个完整窗口为分母，绝不会显示“窗口减输出预留”这一误导性的容量。

使用 Anthropic 时，配置其公开的 Messages API（不是 Claude Code 的本地会话、OAuth 或订阅协议）：

为什么没有 `max_output_tokens`、Anthropic 的必填 `max_tokens` 如何处理，以及固定的上下文/压缩与恢复合同，见 [上下文管理](docs/context-management.md)。`model.pricing` 只是本地费用估算，不是服务商账单。

### 沙盒与运行时护栏

`[sandbox]` 省略时仍采用严格的 `workspace-write`：worker 只能读取工作区、最小系统运行时和显式的 `read_only_roots`，只可在工作区与私有临时目录写入。`read_only_roots` 只接受已存在的绝对目录，加载时会解析 symlink 并去重；不要把 `$HOME`、SSH agent socket、长期凭据目录或容器 socket 放进去。

`protected_paths` 只能追加工作区内的**字面路径**，或以字面 `/**` 结尾的目录子树；不能用一般 glob、绝对路径或 `..`。无论配置如何，`.git`、`.agents`、`.eino-assistant`、`.eino` 和 `.env` 都保持不可读写；需要保护 `.env.local`、`secrets/**` 等未来文件时显式逐项追加。若实际配置文件或 session storage 位于工作区内，启动时也会自动加入保护；storage 不能等于或包含 workspace，工作区内的 config/storage symlink 会 fail-closed，避免 worker 替换下一次宿主启动会读取的控制文件。两种模式都会拒绝工作区中的任何多链接常规文件；受保护目录也会递归检查，防止用同 inode 的公开别名绕过路径遮蔽。若项目刻意使用 hard link，请改为 copy。

`[sandbox.network]` 的空 allowlist 表示网络关闭。非空时 worker 仅能通过本地代理访问列出的精确 DNS 主机名，HTTP 仅限 80、HTTPS CONNECT 仅限 443；URL、端口、IP 和通配符都会被配置校验拒绝，解析到 private、loopback、link-local、NAT64、site-local 或配置的 IANA special-use 网段的结果也不会连接。HTTP 会覆写为已授权 Host；CONNECT 只接受与授权主机匹配的 TLS ClientHello SNI（缺少 SNI / ECH 不可验证时拒绝）。代理不解密 TLS 或检查请求体，因此无法约束加密后的 HTTP Host / HTTP2 `:authority`；allowlist 是网络/TLS endpoint 边界，允许的域名本身仍可能成为数据外传通道。模型 API 由宿主进程调用，不受该 worker 网络 allowlist 影响。

沙盒后端是 macOS `sandbox-exec`（Seatbelt）或 Linux `bwrap`。启动时会固定并校验 backend launcher，工作区随后出现同名 PATH 可执行文件也不会改变后续隔离边界；工作区内的 worker 会在首次工具调用前复制到宿主私有路径，worker 在处理工具输入前会清理或封存继承的额外文件描述符，避免宿主已打开的文件/socket 越过路径策略。后端缺失、不可用或 worker 初始化失败时，普通有副作用工具返回 `sandbox_unavailable`，不会悄悄改用宿主权限。Linux 默认只挂载所需的 `/usr/bin`、`/usr/lib*` 等运行时目录，`/usr/local` 等自定义工具链须显式列入 `read_only_roots`。若确有必要，只有 `shell` 可携带理由请求一次宿主升级；它会离开工作区、文件系统和网络 sandbox 边界，该提示只提供 once/deny，不能记为 session allow/deny。宿主升级只接受字面 argv，并以直接 `exec` 运行，不支持 shell 引用、展开、控制运算符、builtin 或环境赋值。

交互 TUI 的显式 yolo 是另一条明确的 host bypass：它不等待 sandbox worker，也不把普通 sandbox 失败当作 yolo；shell 与 apply_patch 都直接执行。它仍会运行规则、workspace/path/symlink 与字符串级命令检查，但这些不是 yolo 下的强制宿主安全边界，`[sandbox.network].allowed_domains` 也不再限制 yolo 的 host 网络。退出 yolo 后，已配置的普通 sandbox 状态仍会恢复用于后续工具调用。

`[runtime]` 同时限制一个完整 agent turn：`max_turn_seconds` 默认 600（最大 3600）、`max_model_steps` 默认 8（最大 32）、`max_tool_calls` 默认 16（最大 128），以及 `max_consecutive_equivalent_tool_calls` 默认 3。一次 tool-enabled 模型响应只消耗一个 model step；其中的 0..N 条工具调用仍各自计入 tool-call 预算，并可并行执行。达到模型决策预算后，系统还会发起一次已解绑工具的最终答复请求；它不能再执行工具，也不消耗 `max_model_steps`。重复调用按工具名与规范化 JSON 参数识别；同一参数在实际工作区或外部状态变更前最多允许阈值次数执行，下一次重试会被拒绝并带 `stop_retrying`。重复调用阈值的非零值不得超过 `max_tool_calls`；省略或设为 `0` 时使用默认 3，但当 `max_tool_calls` 小于 3 时自动收紧到该总预算。每个完成工具结果都会持久化为会话 artifact；普通模型视图只接收有界预览和 artifact 引用，并行批次共享预览额度。artifact 本身不是工具调用，只有模型主动调用 `read_artifact` 才消耗一次 tool-call 预算。超时会停止该 turn，耗尽 tool-call 预算会拒绝后续工具调用并返回不可重试结果。它们与 `[tools.shell]` 的单命令 timeout / 输出上限互补，不是 CPU、内存或磁盘 cgroup 配额。
