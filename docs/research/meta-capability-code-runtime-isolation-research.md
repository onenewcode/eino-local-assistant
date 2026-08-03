# 本地 CLI Agent 的元能力、限制与隔离：业界实践（扩写）

> Status: research note, not an implementation plan.
>
> Research date: 2026-07-27（同日扩写：补全产品矩阵、一手工程文、外层隔离与逃逸类问题）. Re-verify before adopting; vendor behavior changes.
>
> **Scope**：仅讨论 **用户本机安装 / 本机执行工具** 的 coding CLI（及强相关本地 agent CLI）。聚焦：
>
> 1. 元能力如何落地（几乎总是 shell + 文件）
> 2. 执行面在本机（vs 云 CI / 浏览器）
> 3. **如何限制**（policy / 审批 / 预算 / 信任）
> 4. **如何隔离**（OS 沙盒 / 可选容器·microVM 外层 / 网络代理 / 密钥）
>
> **Out of scope**：本仓库落地规格；云端多租户 CI 作为主设计目标（仅对照）；OS 原语逐行实现手册（见 [agent-sandbox-research.md](./agent-sandbox-research.md)）；命令规则语法 alone（见 [cli-command-permissions-research.md](./cli-command-permissions-research.md)）。

---

## 1. Summary

- **本机 CLI 的元能力 = 受控 `shell` + 文件读写/patch**，用**用户机器上的** `python`/`node`/工具链；不内嵌 Jupyter/Monaco 式「语言编辑器」。
- **LLM 常在远程，工具始终本地**——限制与隔离的作用点在 **spawn 路径与 tool 入口**，不在模型 API。
- **限制 ≠ 隔离**，成熟产品做成**两拨盘**（Claude、Codex、Cursor 工程文高度同构）：
  - 限制：许不许试（mode / allow-deny-ask / 审批 / trust）
  - 隔离：试了能碰什么（FS + 网络，OS 强制）
- **内置沙盒主流是 OS 进程级**，不是默认 Docker：
  - **Claude Code**：Seatbelt / bubblewrap + **host 侧网络代理**（Unix socket）；开源 `sandbox-runtime`（`srt`）；官方称权限提示约 **−84%**
  - **Codex CLI**：Seatbelt / Landlock(+seccomp) 或 bwrap / Windows restricted token；`sandbox_mode` × `approval_policy`；网络默认可关
  - **Cursor**：Seatbelt（动态策略含 `.cursorignore`）/ Linux Landlock+seccomp+overlay / Windows 走 **WSL2 内 Linux 沙盒**；官方称中断约 **−40%**
  - **Gemini CLI**：**多后端**——`sandbox-exec` 配置档 **或** Docker/Podman（用户机上的容器）
  - **GitHub Copilot CLI**：权限提示为主；另有 **本地 `/sandbox`** 与 **`copilot --cloud`**（云沙盒，非默认本机）
- **第二层常见外层**：用户用 Docker / **Docker Sandboxes（microVM）** / Firejail / 自备 bwrap **包住整个 agent**（尤其配合 `--dangerously-skip-permissions` / full-access）。
- **隔离必须双通道（FS+网络）**——Anthropic 明文：无网络可外泄密钥，无 FS 可逃逸到网络。
- **覆盖面是最大设计坑**：Bash 沙盒 **不等于** 文件工具 / MCP / hooks 已隔离；`dangerously-skip-permissions` 时官方建议整 agent 进容器/VM/`srt`。
- **策略层失败模式真实存在**：例如 Linux 上经 workspace 注入项目配置/hooks 的 **配置型逃逸（CBSE）** 报告；Docker socket 挂载 ≈ 破隔离；只靠 denylist/代理环境变量不够。
- 谱系上还有 **OpenHands 类 Docker-first runtime**、**Aider 类弱沙盒+git 回滚**、**用户外层 Firejail**——「本地 CLI」内部仍有多条哲学，不可只看三家旗舰。

---

## 2. 执行位置：服务端 vs 本地

### 2.1 全景对照（避免张冠李戴）

| 形态 | 代码/命令位置 | 代表 | 本文 |
| --- | --- | --- | --- |
| 云端 Code Interpreter | 厂商 guest | OpenAI/Claude/Azure CI | 对照 |
| 浏览器 Artifacts | 用户浏览器 iframe | Claude Artifacts | 对照 |
| 云端 coding agent / 虚拟电脑 | 云 VM | Copilot coding agent、Manus/E2B、Claude Code on the web | 对照 |
| **本机 coding CLI** | **用户 OS 上的子进程** | Codex / Claude Code / Gemini / Cursor CLI / Copilot CLI / Q·Kiro 等 | **主范围** |
| 本机 + 可选容器后端 | 用户机上的 Docker/Podman | Gemini sandbox=docker；OpenHands Docker runtime | 主范围（仍本机） |
| 用户外层 microVM | 用户机或桌面产品起的 guest | Docker Sandboxes 跑 claude/codex/cursor | 主范围（外层） |

### 2.2 本机 CLI 的双位置

```text
远程/本地 LLM  ──tool_call──►  本机 host（策略·审批·持钥）
                                    │ spawn
                                    ▼
                              本机 sandboxed child（shell/py/node）
```

| 问题 | 答案 |
| --- | --- |
| py/js 在哪跑？ | 本机 PATH / 项目 venv（或容器镜像内工具链） |
| 限制/隔离作用点？ | host 决策 + 子进程 OS profile |
| 有无厂商 `container_id`？ | 无（那是云 CI） |

---

## 3. Problem boundary

### 3.1 元能力 / 限制 / 隔离

| 术语 | 含义 |
| --- | --- |
| 元能力 | 少而强的执行面（几乎总是 **shell**）使模型能写脚本扩展行动空间 |
| 限制 | 缩小「允许尝试」的集合与自动化程度 |
| 隔离 | 缩小「尝试成功后的 blast radius」 |

> shell 是能力放大器；**限制管开关，隔离管爆炸半径**。两者都要代码强制，不能靠 AGENTS.md。

### 3.2 与云 CI 假设差

| | 云 CI | 本机 CLI |
| --- | --- | --- |
| Runtime | 预装科学栈镜像 | 用户/项目工具链 |
| 状态 | 短 TTL container | 磁盘仓库 |
| 主威胁 | 租户互害 | 伤 host、密钥、持久化 |
| 「编辑器」 | Web 面板 | 无；IDE 则复用宿主 |

---

## 4. 元能力：本地如何做

### 4.1 收敛形态

```text
模型 → shell(command) →（限制）→（隔离 spawn）→ stdout/stderr/exit
     → apply_patch / 写文件 → 再 shell（test/build）
```

**没有**独立「内置 Python 编辑器」；Python/JS = 项目里的解释器 + 终端输出。IDE 扩展场景编辑面在宿主编辑器。

### 4.2 产品差异只在「壳」不在「核」

几乎所有本机 coding CLI 的核都是：**终端命令 + 工作区文件**。差异在：

- 默认是否沙盒、沙盒实现
- 审批默认松紧
- 是否 Docker 运行时优先
- 是否提供云会话旁路

---

## 5. 产品矩阵（本机相关，扩全）

> 标注：机制以公开文档/工程文/可复核二次源为准；产品会变。

| 产品 | 安装形态 | 元能力 | 限制（公开要点） | 隔离（公开要点） | 备注 |
| --- | --- | --- | --- | --- | --- |
| **Claude Code** | 本机 CLI | Bash + 文件工具 | permission modes；默认曾偏「写/bash/网要批」；auto-allow 可与沙盒组合；`--dangerously-skip-permissions` | Bash：**Seatbelt / bwrap**；网：**UDS→host proxy** 域策略；`/sandbox`；开源 **srt**；web 版为云 VM+git proxy | 官方：双隔离；提示 **−84%**；沙盒主要罩 Bash 子树 |
| **Codex CLI** | 本机 CLI/IDE | shell + 文件 | `approval_policy`: untrusted / on-request / never；与 sandbox **分离**；另有 **exec-policy** 等细规则面（社区/文档提及 Starlark 向） | `sandbox_mode`: read-only / **workspace-write** / danger-full-access；macOS Seatbelt；Linux Landlock/seccomp 或 bwrap；Windows restricted token；网络默认可关 | Auto 预设常 = workspace-write + on-request |
| **Cursor Agent / CLI** | IDE + CLI | Shell 工具 | Auto-run / allowlist；沙盒外再批；headless 可静默 deny | macOS **Seatbelt 动态 profile**（含 deny 写 `.git/hooks`、`.cursorignore` 等）；Linux **Landlock+seccomp+overlay**（`.cursorignore` 不可读）；Windows **WSL2 内 Linux 沙盒** | 官方：中断 **−40%**；强调 **给模型讲清沙盒失败原因** |
| **Gemini CLI** | 本机 CLI | tools/shell | 动态权限，命令可请求额外权限 | `GEMINI_SANDBOX` / settings：`sandbox-exec` 档 **或 docker/podman**；可自定义 sandbox Dockerfile | 多后端是差异点 |
| **GitHub Copilot CLI** | 本机 CLI | 改文件/跑命令 | 默认权限提示；`--allow-tool` / `--allow-all`；autopilot | **`/sandbox enable` 本地沙盒**；**`copilot --cloud` 云沙盒**（策略可继承 cloud agent firewall） | 本地与云双轨；本地沙盒曾属 preview |
| **Amazon Q / Kiro CLI** | 本机 CLI | 用本机工具读写与命令 | 可确认每步；custom agent 可裁工具与权限 | 公开材料偏**确认+工具裁剪**；社区用 OS/目录权限自建沙盒 | 文档迁到 Kiro；OS 沙盒非其叙事中心 |
| **OpenHands** | 本机/服务 + **Docker runtime** | 容器内 shell/浏览器等 | 安全分析器等（版本演进） | **Docker-first**：agent 与 tool runtime 分进程；可挂 volume；有远程 runtime | 与「轻量 OS 沙盒」哲学不同；摩擦：状态双进程、镜像栈 |
| **Aider** | 本机 CLI | 编辑+可选 shell | 确认、git | **弱内建 OS 沙盒**；依赖 **git 可回滚** + 用户外层 Docker | 代表「软限制 + 版本控制」一极 |
| **Goose** | 本机 | 扩展/命令 | 扩展权限模型 | 可选 **Docker 跑 extension**；分析报告建议容器限制挂载 | 扩展面扩大攻击面 |
| **用户外层方案** | 包任意 CLI | 同内层 | 常开 skip-permissions | **Docker / Docker Sandboxes microVM / Firejail / 手写 bwrap** 只挂 workspace | 社区高频：危险模式 × 外层隔离 |

**读表方式**：旗舰三家（Claude / Codex / Cursor）证明 **本机 OS 沙盒 + 审批分离** 是 2025–2026 共识；Gemini 证明 **容器后端** 仍是合法本机选项；OpenHands 证明 **整 runtime 容器化** 是另一条产品线；Aider 证明 **可以几乎不沙盒**（用别的风险对冲）。

---

## 6. 如何限制（constrain）

### 6.1 限制栈（业界分层，扩写）

```text
0. 能力面：注册哪些 tool；custom agent 裁剪工具集（Q/Claude subagents）
1. 工作区信任：未 trust 不加载项目 hooks/settings/MCP（防打开即中招）
2. 会话/全局 mode：plan、default、acceptEdits、auto、bypass…
3. 规则引擎：deny > ask > allow；前缀/模式；Codex exec-policy 等细粒度
4. 审批 UX：一次 / 会话 / 永久；沙盒内 auto-allow；分类器自动批（Claude auto mode 路线）
5. ——通过后进入隔离 spawn（§7）——
6. 运行时护栏：timeout、输出上限、每轮 tool 数、杀进程组
7. 可见性：状态栏、/permissions、/sandbox、tool 卡上 decision
```

### 6.2 各层要点

#### 能力面与「元能力开关」

- 关掉 shell = 关掉元能力（只读审查场景）。
- Custom / sub-agent：缩小工具集 = 结构性限制（比 prompt「别用 rm」可靠）。

#### 信任边界

- Claude/Codex 生态均强调：**项目配置是 inbound**。
- 未信任前执行 hooks = 经典事故模式；限制栈必须把 trust 放在加载配置之前。

#### 两拨盘（限制侧一半）

| 产品 | 限制旋钮 | 隔离旋钮 |
| --- | --- | --- |
| Codex | `approval_policy` | `sandbox_mode` + network 子项 |
| Claude | permission mode / sandbox auto-allow vs regular | `/sandbox` FS+network profile；fallback unsandbox 可配 |
| Cursor | Auto-run / allowlist | sandbox enabled + sandbox.json 路径 |

**禁止**把「never 审批」与「full-access」绑成唯一「爽快模式」而不文档化风险。

#### 规则与 denylist

- 字符串 denylist **必被绕过**（拼接、编码、解释器内执行）。
- 规则层适合：明显危险 UX、企业强制 deny、前缀 allow 降噪。
- **真实边界靠隔离**。

#### 审批疲劳对策（产品共识）

| 对策 | 来源信号 |
| --- | --- |
| 沙盒内少问、出界再问 | Claude −84%；Cursor −40% 中断 |
| 安全命令预 allow（echo/cat 等） | Claude 早期 permission 模型 |
| 分类器自动批一部分 | Claude Code auto mode 工程文（用户批准率极高→疲劳） |
| 失败原因结构化回模型 | Cursor：避免「同命令死循环重试」 |

#### 运行时护栏

timeout、output cap、tool 预算——限制 **DoS/成本/上下文撑爆**，不替代 FS/网络。

### 6.3 元能力（写脚本再执行）的额外限制含义

| 风险 | 限制层能做的 | 必须靠隔离的 |
| --- | --- | --- |
| 脚本内 curl 外传 | 禁止 curl 字符串（弱） | **断网 / 代理 allowlist** |
| 读 `~/.aws` | 路径 deny 规则（部分） | **读策略 / 密钥不进环境** |
| 改 `~/.zshrc` | 难靠命令规则 | **强制写保护路径** |
| MCP 执行 | MCP 单独 allow | MCP 是否进同一 OS profile |
| 无限 `npm i` | tool 预算、开网审批 | 写仍限 workspace |

---

## 7. 如何做隔离（isolate）

### 7.1 目标与双隔离

Anthropic 工程文（Claude Code sandboxing）明确定义：

1. **Filesystem isolation** — 只能碰允许的目录  
2. **Network isolation** — 只能连允许的服务器（经 **沙盒外 proxy**）  
3. **两者缺一不可**

Cursor / Codex 公开材料同一逻辑：沙盒内自由，**出界（尤其上网）再批**。

### 7.2 本机隔离档位谱

```text
L0 同进程 eval                    → 禁止
L2 本机 WASM（Pyodide 等）         → 可选旁路，非 coding 主路径
L3 OS 进程沙盒（Seatbelt/bwrap/Landlock）→ 旗舰默认
L4 本机容器（Docker/Podman）       → Gemini 选项；OpenHands 主路径；用户外层
L5 microVM（Firecracker/Docker Sandboxes 等）→ 强外层 / 云；本机桌面产品渐多
```

2026 行业讨论（博客/论文向）：对**完全不可信、多租户**倾向 L5/gVisor；对**本机开发者 CLI** 旗舰仍押 **L3 可用性**，L4/L5 作外层或无人值守。

### 7.3 旗舰实现对照（机制级）

#### Claude Code / `srt`

| 项 | 机制 |
| --- | --- |
| 覆盖 | **Bash 及子进程**（官方强调）；文件工具/MCP/hooks **不自动等同** |
| FS | 默认可写 cwd（及配置 allowWrite）；OS 强制 |
| 网 | 子进程 **不直连**；**Unix socket → host proxy**；域确认/allowlist；可定制 proxy 规则 |
| 平台 | macOS Seatbelt；Linux bubblewrap（+ socat 等） |
| 产品 | `/sandbox`；auto-allow vs regular；**sandbox 失败可否 unsandbox** 可配 |
| 开源 | `anthropic-experimental/sandbox-runtime` |
| 云旁路 | Claude Code on the web：隔离 VM；**git 凭据不进 guest**，scoped proxy 推送 |

#### Codex CLI

| 项 | 机制 |
| --- | --- |
| 模式 | read-only / workspace-write / danger-full-access |
| 网 | 与模式解耦；workspace-write 下常 **默认无网** |
| 写保护 | 工作区可写时仍常保护 `.git` / 产品目录等（文档与分析报告） |
| 平台 | Seatbelt；Linux Landlock+seccomp 或 vendored bwrap 组合；Windows token/ACL 演进 |
| 调试 | `codex sandbox seatbelt|landlock|windows` 类辅助 |
| 配置 | `config.toml`：`approval_policy`、`sandbox_mode`、`[sandbox_workspace_write]`、环境变量策略等 |

#### Cursor

| 项 | 机制 |
| --- | --- |
| macOS | Seatbelt；**运行时生成** profile；结合 admin/workspace/`.cursorignore` |
| 强制 deny 写示例 | `.vscode`、部分 `.cursor`、`.cursorignore`、`.git/config`、`.git/hooks`、code-workspace 等 |
| Linux | Landlock + seccomp；workspace **overlay**；ignored 文件不可读不可改 |
| Windows | **WSL2 内跑 Linux 沙盒**（原生 Windows 通用开发沙盒难） |
| 模型侧 | Shell 工具说明沙盒；**失败结果写明约束并建议 escalate**（防死重试） |
| 效果 | 官方：中断约少 40% |

#### Gemini CLI

| 项 | 机制 |
| --- | --- |
| 开关 | CLI flag / `GEMINI_SANDBOX` / `settings.json tools.sandbox` |
| 后端 | `sandbox-exec` 配置档 **或** `docker` / `podman` |
| 容器 | 可 `SANDBOX_FLAGS`；项目内 `sandbox.Dockerfile` 定制工具链 |
| 语义 | 工作区挂载路径一致性（容器场景） |

#### 其他本机相关

| 产品 | 隔离摘要 |
| --- | --- |
| Copilot CLI | 本地 `/sandbox` + 可选云；权限模型与 sandbox 叠加 |
| OpenHands | Docker runtime 默认；安全加固文档（勿随意暴露端口/挂 socket） |
| Aider | 几乎无 OS 沙盒；git 回滚 + 外层 |
| 用户 Firejail/bwrap 包 agent | 整进程树可见 FS 收窄；常配合 skip-permissions |

### 7.4 网络隔离：可靠 vs 不可靠

| 做法 | 可靠性 | 谁在用 |
| --- | --- | --- |
| 沙盒策略禁止 connect + host proxy 做域 ACL | 高 | Claude/`srt` 方向 |
| 内核/平台防火墙 + 专用沙盒用户 | 中高 | Windows Codex 演进 |
| 仅 `HTTP_PROXY=死地址`、PATH 假 ssh | **低（advisory）** | 早期/补丁层教训 |
| 模型提示「不要上网」 | 无效 | — |

开网时：会话级批域；理解 allowlist 域 = 该域**全部 API 能力**；装包场景 escalate。

### 7.5 强制硬保护（写 allow 内的第二道墙）

跨产品反复出现：

- `.git/hooks`、常还有 `.git/config`
- Shell rc：`.bashrc` / `.zshrc` …
- 编辑器/agent 配置：防 agent **改掉自己的权限与 ignore**
- 凭证目录：`~/.ssh`、`~/.aws` …（读或写至少一侧）

Cursor 公开 Seatbelt 片段直接 deny 写 hooks 与 `.cursorignore`——说明 **「防策略被工作区内恶意文件改松」** 是一等需求。

### 7.6 覆盖面清单（设计必答题）

| 路径 | 常见是否在 shell 沙盒内 |
| --- | --- |
| shell 及子孙 | ✅ |
| apply_patch / 内建 Edit | ⚠️ 常自管路径，需 workspace 钳制 |
| MCP | ⚠️ 常 **外**；srt 宣称可沙任意进程但产品默认未必 |
| hooks | ⚠️ trust 后仍可能在沙盒外 |
| 整 CLI 进程 | ❌ 除非用户外层 Docker/Firejail/microVM |

Claude 文档逻辑（sandbox environments）：**skip-permissions 时**必须靠容器/VM/**sandbox runtime 罩住文件工具、MCP、hooks**，否则隔离形同虚设。

### 7.7 密钥

| 模式 | 说明 |
| --- | --- |
| API key 留 host | shell 环境不注入；模型命令读不到 |
| Git 凭据不进 guest | 云 Claude web 用 scoped proxy；本机更难，常审批 push + 系统 credential |
| 代理注入 | 沙盒内 sentinel，proxy 换真密钥（高级） |

### 7.8 外层隔离（本机仍算「本地安装」）

当内置 L3 不够或要开 YOLO：

| 外层 | 要点 |
| --- | --- |
| 手写 bwrap / Firejail 包 `claude`/`codex` | 只 bind workspace；社区用于 skip-permissions |
| Docker 只挂项目目录 | **禁止**随便挂 `docker.sock`、家目录、云配置 |
| **Docker Sandboxes** | microVM；独立 daemon；网络代理可注入凭据不进 agent；可跑多种 agent 模板 |
| git worktree + 逻辑隔离 | 弱；防误伤主工作区，不防密钥外泄 |

### 7.9 已知尖锐失败模式（扩检索）

| 问题 | 含义 |
| --- | --- |
| **配置型沙盒逃逸（CBSE）** | 公开分析：Linux bwrap 场景下可在 workspace 写入项目配置/hooks，**退出沙盒后在 host 全权执行**——隔离边界若不含「配置加载信任」则被绕过 |
| Ubuntu AppArmor vs userns | Cursor/Codex/bwrap 均可能被 `apparmor_restrict_unprivileged_userns` 卡住；需 profile 或文档化回退 |
| Seatbelt 已 deprecated 仍在用 | Cursor 文：Chrome 等仍用；无完美替代 |
| 读面过宽 | 多产品默认「限写不限读」→ 无网时仍本地读密钥；社区要 read-restrict 模式（Codex issue 等） |
| Docker socket | 挂入 ≈ root 级逃逸 |
| sandbox 失败自动 unsandbox | 可变成静默降级；应可关且审计 |
| 代理环境变量断网 | 忽略 proxy 的二进制仍出网 |

### 7.10 推荐组合（本机 CLI）

| 意图 | 限制 | 隔离 |
| --- | --- | --- |
| 不信任仓库 | 强 ask / 未 trust 不执行 | read-only 或等价 |
| 日常开发 | on-request | **workspace-write + 网关** + 硬保护路径 |
| 装包/拉依赖 | 开网会话级批准 | 同上 + 临时 network allow |
| 无人值守 YOLO | never 审批 | **必须**紧沙盒 **或** 外层 L4/L5；禁止裸 full-access |
| CI 只读审查 | never | read-only + 忽略用户/项目危险配置（见 Codex ephemeral 讨论实践） |

### 7.11 参考架构

```text
┌─ Host CLI ─────────────────────────────────────────────┐
│ trust → policy(deny/ask/allow) → approval UX            │
│ 持有 API key；文件工具路径规范化                         │
│ 网络 proxy / 审计 / 状态展示                             │
└───────────────────────┬─────────────────────────────────┘
                        │ spawn(cmd, SandboxProfile)
                        ▼
┌─ Guest 子进程树 ────────────────────────────────────────┐
│ Seatbelt | Landlock+seccomp | bwrap | (可选) container  │
│ FS allow-write + always-deny；网 deny 或经 proxy         │
│ 无长期密钥；timeout + 杀组                               │
└─────────────────────────────────────────────────────────┘

可选外层：Docker / microVM / Firejail 包住整个 Host CLI
```

---

## 8. 限制 + 隔离套在元能力上

### 8.1 单次 shell

```text
shell("pytest -q")
  → 能力启用？ trust？ mode？
  → policy allow|ask|deny → 审批范围
  → SandboxProfile spawn
  → timeout / 输出 cap
  → 结果（含沙盒失败原因）→ 模型
```

### 8.2 写脚本再跑

| 步骤 | 限制 | 隔离 |
| --- | --- | --- |
| 写 `x.py` | 路径策略 | workspace 写钳制 |
| `python x.py` | shell 规则 | 同 profile；脚本内联网仍无网 |
| 读密钥路径 | 弱 | 读 deny / 无密钥 env |
| 改 hooks | 弱 | **always-deny 写 hooks** |

### 8.3 设计清单（可配置表面）

```text
Capability:   shell, edit, mcp, web, ...
Trust:        workspace_trusted, defer_project_config
Approval:     untrusted | on_request | never | classifier_auto
Rules:        deny > ask > allow；exec-policy / hooks
Sandbox:      mode, writable_roots, network, deny_read, deny_write_always
Fallback:     allow_unsandbox? (default false in hardened)
Guards:       timeout, output_cap, tool_budget
Outer:        none | docker | microvm | firejail
Visibility:   status, /permissions, /sandbox, audit log
Model UX:     sandbox errors explained + escalate hint
```

---

## 9. Efficient / reasonable patterns

1. **默认**：本机执行 + **workspace-write + 网络关 + on-request**  
2. **两拨盘**永远分离配置  
3. **双隔离** FS+网络；网络走 **强制 proxy** 而非 env 谎言  
4. **硬保护** hooks/rc/凭证/自身配置  
5. **信任前**不加载项目 hooks  
6. **覆盖面**写进文档；YOLO ⇒ 外层包全进程  
7. **模型可感知沙盒**（Cursor 经验）：失败可恢复，而非死磕  
8. **元能力保持一个强 shell**，不靠 80 个语言 tool  
9. **Python/JS = 项目工具链**，不内嵌第二 runtime（除非显式 WASM 旁路）  
10. **企业**：禁止静默 unsandbox；审计；只读审查忽略 ambient 用户配置  

**不要从云 CI 抄**：container TTL、预装科学镜像当默认、Artifacts iframe。  
**不要从「完全不可信多租户」抄到笔记本默认**：microVM 作默认会伤 UX；作外层/无人值守更合适。

---

## 10. Pitfalls

| 坑 | 缓解 |
| --- | --- |
| 只调研三家旗舰 | 看清 Docker-first / 弱沙盒 / 外层 microVM 等并列哲学 |
| 混限制与隔离 | 两拨盘 |
| 混模型位置与执行位置 | 工具本地 |
| 以为 Bash 沙盒 = 全 agent 安全 | 查 MCP/hooks/文件工具 |
| denylist 当主防 | 主防 FS+网 |
| skip-permissions 无外层 | 容器/VM/srt 全罩 |
| 配置写入 workspace 逃逸 | trust 模型；hooks 延迟；只读挂载配置 |
| docker.sock | 不挂 |
| 读面过宽 + 一度开网 | 收紧读或密钥 deny |
| 沙盒失败静默降级 | 可关 fallback |
| Linux userns/AppArmor | 文档依赖与探测回退 |
| 给模型的错误不可读 | 结构化沙盒错误 + escalate 提示 |
| 外层隔离却挂全家目录 | 只挂 workspace |

---

## 11. Open questions

- 读限制模式（read-restrict roots）何时成旗舰默认？  
- MCP 默认是否与 Bash 共用 `srt`/Seatbelt profile？  
- Windows 原生通用开发沙盒是否取代 WSL2 路径？  
- 配置型逃逸（CBSE）类问题的标准缓解是否「项目 settings 签名 / 只读 + host 全局策略优先」？  
- 分类器 auto-approve 与沙盒如何分工才不引入新绕过？  
- 本机 WASM `run_code` 旁路是否值得与 shell 并存？  
- Docker Sandboxes microVM 是否会变成「本地 YOLO 标准外层」？  

---

## 12. 概念速查

```text
位置：工具/py/js → 本机；LLM → 常远程

元能力：shell + 文件（项目工具链）

限制：trust → mode → policy → approval → budget
隔离：L3 OS 双通道（FS+网）为主；L4/L5 外层可选
两拨盘：approval  ×  sandbox
覆盖面：shell ≠ 全局；YOLO 要外层全罩
密钥：不进 guest env；git 理想走 proxy
```

---

## References

### 一手 / 官方与工程

- Anthropic, *Beyond permission prompts: making Claude Code more secure and autonomous*（双隔离、−84%、proxy、开源 srt）: https://www.anthropic.com/engineering/claude-code-sandboxing  
- Anthropic, *How we contain Claude across products*（HITL 沙盒、devcontainer 无人值守）: https://www.anthropic.com/engineering/how-we-contain-claude  
- Anthropic, *How we built Claude Code auto mode*（审批疲劳、分类器）: https://www.anthropic.com/engineering/claude-code-auto-mode  
- Claude Code Docs, *Sandboxing* / *sandbox environments*: https://code.claude.com/docs/en/sandboxing  
- `anthropic-experimental/sandbox-runtime`: https://github.com/anthropic-experimental/sandbox-runtime  
- OpenAI / ChatGPT Learn, *Sandbox*、*Agent approvals & security*: https://learn.chatgpt.com/docs/sandboxing  
- OpenAI, *Building Codex Windows sandbox*: https://openai.com/index/building-codex-windows-sandbox/  
- Cursor, *Implementing a secure sandbox for local agents*（Seatbelt/Landlock/WSL2、−40%、模型侧错误）: https://cursor.com/blog/agent-sandboxing  
- Gemini CLI, *Sandboxing*: https://geminicli.com/docs/cli/sandbox/  
- GitHub Docs, *About GitHub Copilot CLI*（local `/sandbox`、`copilot --cloud`）: https://docs.github.com/copilot/concepts/agents/about-copilot-cli  
- Docker Docs, *AI Sandboxes*（agents 模板、microVM 向）: https://docs.docker.com/ai/sandboxes/  
- OpenHands, Docker runtime / troubleshooting: https://docs.openhands.dev/  

### 分析与生态（二次源，采用时复核）

- Agent Safehouse 等对 Codex/Claude/Goose/Cursor 的 sandbox 分析报告（实现细节向）  
- Cymulate 等关于 **configuration-based sandbox escape** 与 AI 工具的公开分析（2026）  
- 社区：bwrap/Firejail 包 Claude skip-permissions；Docker Sandboxes 跑多 agent  

### 对照（非本机 CLI 主路径）

- OpenAI Code Interpreter（服务端 container）: https://developers.openai.com/api/docs/guides/tools-code-interpreter  
- E2B / Manus 云 microVM  

### 本仓库相关调研

- [agent-sandbox-research.md](./agent-sandbox-research.md) — OS 原语与双隔离深挖  
- [cli-command-permissions-research.md](./cli-command-permissions-research.md) — 权限分层与 denylist 局限  
