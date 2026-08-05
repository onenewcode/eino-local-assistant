# shell / apply_patch 权限、沙盒与运行时护栏

> 工具面 = **Codex 子集**：`shell` + `apply_patch`（另加产品用 `get_current_time`、`read_artifact`）。

## 1. 工具与边界

| 工具 | 作用 | 默认权限姿态 | 执行边界 |
| --- | --- | --- | --- |
| `shell` | `sh -c` 终端/进程 | cautious 种子 allow 少量只读命令；其余 ask | 短生命周期 sandbox worker |
| `apply_patch` | create_file / update_file / delete_file | 默认 ask | 短生命周期 sandbox worker |
| `get_current_time` | 本机时间 | 无 | 宿主进程，无文件/网络副作用 |
| `read_artifact` | thread 内 artifact:// | thread 作用域 | 会话 artifact 存储 |

产品 system prompt 追加 Tool usage policy（优先 `apply_patch` 改文件、时间必须 `get_current_time`）。见 `internal/agent/prompt.go`。模型服务 API 与 TUI/会话账本留在宿主进程；本页的 OS sandbox 只约束 `shell` 和 `apply_patch` worker。

## 2. 配置语法（Codex + Claude 规则串）

```toml
approval_policy = "on-request"   # Codex: on-request | never

[workspace]
root = ""

[sandbox]
# workspace-write（默认）或 read-only
mode = "workspace-write"
# 只接受已存在、绝对且会解析 symlink 的目录。
# read_only_roots = ["/absolute/path/to/toolchain"]
# 只能追加字面工作区路径或字面 `/**` 子树，不接受一般 glob、绝对路径或 ..。
# 内建 .git、.agents、.codex、.env 永远受保护。
# protected_paths = [".env.local", "secrets/**"]

# 空 allowlist = 无网络；只接受精确 DNS 主机名。
# [sandbox.network]
# allowed_domains = ["proxy.golang.org", "registry.npmjs.org"]

[runtime]
# 0 使用默认值；最大值分别为 3600、64、128。
max_turn_seconds = 600
max_react_steps = 8
max_tool_calls = 16

[permissions]
profile = "cautious"
allow = [
  "Shell(go test *)",
  "ApplyPatch(src/**)",
]
ask = [
  "Shell(git push *)",
]
deny = [
  "Shell(sudo *)",
  "ApplyPatch(.env)",
]

[tools.shell]
timeout_seconds = 60
max_output_bytes = 65536

[tools.apply_patch]
max_bytes = 262144
```

| 规则名 | 工具 |
| --- | --- |
| `Shell` / `Bash` / `run_command` | `shell` |
| `ApplyPatch` / `Write` / `Edit` | `apply_patch` |

权限规则按 **deny -> ask -> allow** 求值。Shell 含元字符时 allow 降为 ask；普通 sandbox 调用的 hard deny 不能被 session allow 或 `approval_policy = "never"` 绕过。权限字符串不是安全边界：通过审批只决定能否启动 sandbox worker，不能扩大其文件或网络权限。

`approval_policy` 是进程启动时读取的静态配置，不是 TUI 会话内模式。Headless `exec` 没有交互 approver，继续按该静态值处理：`on-request` 遇到需要审批的 `DecisionAsk` 时 fail-closed，`never` 只保留现有的 `DecisionAsk` 自动放行语义；二者都不能改写 `DecisionDeny`、工作区/路径钳制或 sandbox 边界。TUI 的动态模式不会改写配置文件，也不改变 headless 语义。

### 2.1 TUI 会话内模式（即将加入）

TUI 只规划 `ask <-> auto` 两个会话内模式，不扩展为 `plan`、`read-only` 或其他更大的权限档位。这里的模式是当前 TUI 进程的临时运行时状态，不写入 TOML、session ledger 或 resume 数据；进程退出后不保留。

- `/permissions` 无参数只读展示当前模式、静态规则、sandbox 和 runtime 信息，不改变任何授权状态。
- `/permissions ask` 与 `/permissions auto` 只在 idle 时接受；busy、compacting 或其他非 idle 状态拒绝命令，不进入 FIFO，也不改变正在运行的工具或审批。
- `ask` 让普通 `DecisionAsk` 继续进入现有 TUI once / session / deny 审批；`auto` 只在工具实际授权边界自动放行 `DecisionAsk`。`DecisionAllow` 仍受后续执行护栏约束，`DecisionDeny`（包括 hard deny）不能被模式改写。
- `auto` 不绕过 `workspace_only`、路径解析与 path clamp；不绕过 sandbox，sandbox backend 不可用时仍返回 `sandbox_unavailable` 并 fail-closed；shell 的 host escalation 仍是逐次 once / deny，不能由 `auto` 或 session allow 自动放行。
- 已注册的 `shell` 与 `apply_patch` 必须读取同一动态模式；只更新状态栏或 `/permissions` 文案而不改变工具授权路径，不算实现该模式。

状态栏的 `cmd=ask|auto` 与 `/permissions` 的 `mode` 均反映当前 TUI 进程状态；`approval_policy` 仍作为独立的静态配置字段展示。

## 3. Sandbox

- `workspace-write` 是默认模式：工作区可写，worker 私有临时目录可写；`read-only` 将工作区以只读方式挂载。
- `read_only_roots` 仅用于显式放行外部工具链/运行时目录。不要放入 `$HOME`、密钥目录、SSH agent socket、Docker socket 或其他高权限主机路径。
- `protected_paths` 是字面工作区相对路径，或以字面 `/**` 结尾的目录子树。受保护目录会连同子树被遮蔽；`.env.local` 等派生配置文件必须显式列出。`.git`、`.agents`、`.codex`、`.env` 是不可移除的内建保护路径。实际 config 文件和 session storage 若落在工作区内，会在启动时自动追加；storage 等于/包含 workspace 或 workspace 内控制路径经过 symlink 时 fail-closed，防止 worker 替换宿主控制面。会话启动时会扫描工作区（及受保护子树）中的多链接常规文件并 fail-closed，防止 hard-link inode 别名绕过路径 mask；每次工具执行只重绑私有 temp 目录，不再全树重扫。
- `sandbox.network.allowed_domains` 为空时无 egress。非空时 worker 只能连本地代理，代理只允许 HTTP 80 或 HTTPS CONNECT 443 到精确 allowlist 域名，并拒绝 URL、端口、IP literal、通配符及解析到 private、loopback、link-local、NAT64、site-local 或配置的 IANA special-use 网段的目标。普通 HTTP 会覆写为已授权 Host；CONNECT 要求可见的 TLS ClientHello SNI 与 CONNECT authority 一致，缺少 SNI 或 ECH 无法验证时拒绝。
- 代理不做 TLS 解密或请求体检查，因此无法检查 CONNECT 隧道内加密后的 HTTP Host / HTTP2 `:authority`。allowlist 是 DNS/IP/TLS endpoint 边界，不是可强制的 HTTP-origin 策略；允许的域名仍可能成为数据外传或其自身反向代理路由的通道。
- macOS 使用 `sandbox-exec`/Seatbelt，Linux 使用 `bwrap`（bubblewrap）。启动时固定 backend launcher，工作区随后写入同名 PATH 二进制不会影响后续调用；worker 在处理工具请求前移除或标记继承的额外 FD 为 close-on-exec，避免宿主已打开 file/socket 传给 shell；Linux 仅默认挂载最小运行时目录，额外 `/usr/local` 工具链需显式 `read_only_roots`。后端不存在、不能启动或 worker 初始化失败时工具返回 `sandbox_unavailable`，绝不静默退回宿主执行。Windows 在 V1 不支持强制 sandbox，因此同样 fail-closed。

只有 `shell` 能请求 `sandbox_permissions = "require_escalated"` 的一次性宿主升级，并且必须给出 `justification`。这会离开工作区、文件系统和网络 sandbox 边界；无论 `approval_policy` 如何设置，都必须由用户逐次选择 once 或 deny，不能复用 session allow/deny（`approval_policy = "never"` 也不会自动批准 host 提权）。审批前会将可执行文件解析并 pin 为绝对路径，modal 展示并指纹化该 pinned argv，审批后不再重新 `LookPath`。为让审批文本与实际执行一致，V1 的宿主升级只接受无引用、转义、展开、重定向、管道或控制运算符的字面 argv，并以直接 `exec` 而不是 `sh -c` 运行；不能使用 shell builtin 或环境赋值，且拒绝 `sudo`、`doas`、`su`、`setsid`、`nohup`；workspace 内可执行文件不可用于 host 提权。`apply_patch` 永远没有宿主升级路径。

## 4. Runtime 护栏

`[runtime]` 限制整个 ReAct turn，而不只是一个 shell：

- `max_turn_seconds`：默认 600 秒，最大 3600 秒；达到 deadline 会取消整个 turn。
- `max_react_steps`：默认 8，最大 64；限制 model <-> tools 循环。
- `max_tool_calls`：默认 16，最大 128；预算耗尽返回不可重试的工具结果。

`[tools.shell]` 的 timeout 与输出上限仍是每一条命令的附加护栏。达到 stdout 或 stderr 硬上限时会对原始命令进程组执行 TERM -> 宽限 -> KILL，并标记输出受限，而不是继续排空无限输出。它会清理仍留在原始组中的常规前后台进程，不是可证明的进程树终止：macOS 通用 Seatbelt shell 的后代可 `setsid(2)` / `setpgid(2)` 脱离该组，工具返回后仍可能在既有 sandbox 权限内运行；分离本身不会扩大文件或网络权限。宿主升级也只清理原始进程组，且分离后代保留宿主权限；仅应批准可信、前台、短时命令。需要取消后保证没有任意 shell 后代时，使用 Linux PID namespace、容器或 VM backend，并在该 backend 不可用时 fail-closed。`[tools.apply_patch].max_bytes` 与单调用总输入上限约束补丁大小。这一版本不提供 CPU、内存、磁盘、进程数或 TLS 内容检查；高风险/多租户执行应使用容器、gVisor 或 microVM 等额外隔离层。

## 5. TUI 与审计

- `ask` 模式下普通审批提供 once / session / deny；Esc = deny。`auto` 只跳过这一步的 `DecisionAsk`，不改变 deny、路径钳制或 sandbox 决策。
- 宿主升级审批带有明显风险提示，只提供 once / deny，不能记为 session allow；命令会完整换行显示，控制字符会转义，并附带 SHA-256 指纹以避免长命令或终端序列伪装审批内容。长命令用 PgUp/PgDn 在审批详情中逐页检查。
- 状态栏显示当前 TUI 会话的 `cmd=ask|auto`、`sb=rw|ro`、后端可用性与 `net=off|allow:n`；不会把静态配置误报为持久化的会话设置。
- `/permissions` 无参数只读显示当前模式、权限规则、sandbox 模式/后端、只读根数量、受保护路径、网络 allowlist、runtime 预算和 session allow/deny；带 `ask|auto` 参数的切换仅在 idle 生效。
- 工具输入、结果、沙盒元数据及 runtime 终止原因会进入该 session 的 tool lifecycle/artifact 记录；不要仅依赖 TUI 文案进行审计。

普通审批的 session key：

```text
shell|<前两 token>|<workspace>
apply_patch|<abs path>|<workspace>
```
