# 沙盒与运行时护栏

本文说明 `shell` / `apply_patch` 的可选 OS 文件系统沙盒、工具链可见性、保护路径、宿主升级和显式 YOLO 旁路。工具规则与审批属于独立层，见 [工具规则与影响分级](tool-policy.md)。

## 默认行为

`[sandbox]` 的 `mode` 省略或为空时，OS sandbox 关闭，工具按普通 host 执行路径运行；`approval_policy`、workspace 路径校验和工具规则仍然有效。只有显式设置 `mode = "workspace-write"` 或 `mode = "read-only"` 才会创建短生命周期 worker 并启用 OS 文件系统边界。`toolchain_visibility`、`read_only_roots` 和 `protected_paths` 只影响启用的 worker。

默认 `toolchain_visibility = "auto"` 会从宿主 `PATH` 和安全的 Go/Node/Rust/Java/Python 工具链变量发现可执行目录、符号链接目标和依赖缓存，并以只读方式暴露；`HOME`、写缓存和临时目录仍指向每次执行的私有临时目录。高安全场景可设为 `explicit`，此时只使用手动配置的 `read_only_roots`。不要把 `$HOME`、SSH agent socket、长期凭据目录或容器 socket 放入只读根。

## 文件系统与网络

`protected_paths` 只能追加工作区内的**字面路径**，或以字面 `/**` 结尾的目录子树；不能用一般 glob、绝对路径或 `..`。无论配置如何，`.git`、`.agents`、`.eino-assistant`、`.eino` 和 `.env` 都保持不可读写；需要保护 `.env.local`、`secrets/**` 等未来文件时显式逐项追加。若实际配置文件或 session storage 位于工作区内，启动时也会自动加入保护；storage 不能等于或包含 workspace。两种模式都会拒绝工作区中的任何多链接常规文件；受保护目录也会递归检查，防止用同 inode 的公开别名绕过路径遮蔽。

OS sandbox 只负责文件系统边界，网络在关闭 sandbox、`workspace-write`、`read-only` 和 YOLO 下都保持开放。不会启动网络代理、Linux relay 或网络 namespace，也没有 `[sandbox.network]`、`allowed_domains` 或 `net` 参数。旧配置中的网络表会被拒绝并要求删除；模型 API 仍由宿主进程调用。

## 后端与失败诊断

启用 sandbox 时，后端是 macOS `sandbox-exec`（Seatbelt）或 Linux `bwrap`。启动时会固定并校验 backend launcher；工作区内的 worker 会在首次工具调用前复制到宿主私有路径，worker 还会清理或封存继承的额外文件描述符，避免宿主已打开的文件/socket 越过路径策略。后端缺失、不可用或 worker 初始化失败时，普通有副作用工具返回 `sandbox_unavailable`，不会悄悄改用宿主权限。

shell 结果还会返回 `failure_class`：`command_not_found` 表示 worker 内部无法解析命令，不能直接解释为项目 prerequisite 缺失；`execution_blocked` 表示执行边界拒绝。若确有必要，只有 `shell` 可携带理由请求一次宿主升级；该提示只提供 once/deny，不能记为 session allow/deny。宿主升级只接受字面 argv，并以直接 `exec` 运行，不支持 shell 引用、展开、控制运算符、builtin 或环境赋值。

## 显式 YOLO 旁路

交互 TUI 的显式 yolo 是明确的 host bypass：它不等待 sandbox worker，也不把普通 sandbox 失败当作 yolo；shell 与 apply_patch 都直接执行。它仍会运行规则、workspace/path/symlink 与字符串级命令检查，但这些不是 yolo 下的强制宿主安全边界。退出 yolo 后，已配置的普通 sandbox 状态仍会恢复用于后续工具调用。
