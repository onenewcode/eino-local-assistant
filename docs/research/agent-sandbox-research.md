# Agent 沙盒机制：业界实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-07-17. Re-verify before adopting; vendor behavior changes.
>
> Scope: coding agent / CLI agent 的**执行沙盒**——OS 原语、容器/VM、网络隔离、与审批策略的关系、失败模式。
> Out of scope: 本仓库落地建议；纯字符串 denylist/命令审批细则（见 [cli-command-permissions-research.md](./cli-command-permissions-research.md)）；工具循环终止（见 [tool-call-control-termination-research.md](./tool-call-control-termination-research.md)）。

## 1. Summary

- **沙盒回答的是能力边界，不是意图**：限制“已允许执行的命令能造成多大伤害”；审批/规则决定“是否允许尝试”。两层独立，缺一不可。
- **本地 CLI 主流收敛到 OS 级原语 + 网络代理**，而不是默认 Docker：
  - Claude Code：Seatbelt（macOS）/ bubblewrap（Linux）+ 域代理；开源为 `@anthropic-ai/sandbox-runtime`（`srt`）。
  - Codex CLI：Seatbelt / Landlock(+seccomp 或 bubblewrap 回退) / Windows restricted token；模式 `read-only | workspace-write | danger-full-access`。
  - Gemini CLI：Seatbelt 配置档 / Docker|Podman / Windows native / 可选 gVisor、LXC。
- **双隔离是共识**：仅 FS 或仅网络都不够。无网络时仍可读密钥；有网络无 FS 限制可外泄；无 FS 限制也可改 shell 配置做持久化逃逸。
- **默认姿态**：写限 workspace（+ temp）、读默认偏宽、网络默认关或经 allowlist 代理；敏感路径（`.git`、密钥、shell rc）额外保护。
- **更强隔离分层上移**：托管/云端多用 gVisor、容器或 Firecracker 类 microVM；密钥不进 agent 阶段；egress 按**能力授予**理解，而非“域名白名单即安全”。
- **沙盒不是万能**：approval fatigue、信任边界前执行、approved domain 上的 Files API 外泄、EDR 看不见 guest、symlink 逃逸、代理/自建组件才是薄弱点。

## 2. Problem boundary

### 2.1 术语分层

| 层 | 问题 | 典型机制 |
| --- | --- | --- |
| 权限/审批 | 要不要执行这次 tool？ | allow/deny/ask、permission mode |
| **沙盒/隔离** | **执行时能触达什么？** | Seatbelt、bwrap、Landlock、容器、VM、网络代理 |
| 运行时护栏 | 跑多久、输出多大、如何杀进程？ | timeout、output cap、job object / process group |
| 信任边界 | 何时加载项目配置/hooks？ | workspace trust、延迟 parse |

本文只展开**沙盒/隔离**，但会写清它与审批、密钥、代理的接口。

### 2.2 常被混淆的概念

1. **“自动批准”≠“无沙盒”**：成熟产品在 sandbox 内 auto-allow，出界再问或拒绝。
2. **“允许 bash”≠“继承用户全权”**：bash 是能力放大器；沙盒是其配对件。
3. **容器/VM 与进程沙盒**：前者隔离整个 runtime，后者常只包 shell 子树；覆盖面不同。
4. **网络 allowlist ≠ 安全能力边界**：允许 `api.vendor.com` 等于授予该域上所有可达 API 的能力（含上传、server-side fetch）。

### 2.3 威胁模型（本地 CLI 也成立）

| 来源 | 例子 | 沙盒应挡住的 |
| --- | --- | --- |
| 提示注入 | README/网页让 agent `curl` 外传密钥 | 默认无 egress；密钥路径不可读 |
| 用户被钓鱼粘贴恶意 prompt | 直接要求读 `~/.aws` 并 POST | FS deny + egress deny（模型层通常无效） |
| 模型“为了完成任务”逃逸 | 自己找绕过沙箱的路径 | 无特权 outer escape hatch；或 VM 全包 |
| 供应链/恶意脚本 | 测试脚本读家目录、装后门 | 写限 workspace；shell rc 强制只读 |
| 持久化 | 改 `.zshrc` / git hooks | 强制 deny 路径 |

## 3. Industry mechanisms

### 3.1 隔离强度谱系

从轻到重（业界常用划分）：

```text
模拟环境 (just-bash / WASM)
    → OS 进程沙盒 (Seatbelt / Landlock / bwrap / restricted token)
        → 容器 / gVisor
            → microVM / 全量 VM
```

| 形态 | 代表 | 优点 | 代价 / 边界 |
| --- | --- | --- | --- |
| 模拟 shell/FS | Vercel `just-bash`、WASM QuickJS | 启动极快、无真 syscall | 无真实二进制、原生扩展、GPU |
| OS 原语 | Claude `srt`、Codex、Gemini Seatbelt | 低延迟、贴合本地开发、无需 Docker | 共享主机内核；读面常偏宽；实现细节平台分叉 |
| 容器 / gVisor | OpenHands DockerWorkspace、claude.ai gVisor | 依赖可打包、多租户更成熟 | 守护进程/镜像成本；共享内核仍弱于 VM（gVisor 补用户态内核） |
| microVM / 全 VM | E2B/Firecracker、Docker Sandboxes、Cowork VM | 硬件边界、自有内核 | 启动与运维更重；EDR 可见性下降 |

**本地 coding CLI 的主导选择是 OS 原语**；**云端/多租户/无人值守**倾向 gVisor 或 microVM。

### 3.2 双隔离模型（FS + Network）

Anthropic 与开源 `srt` 明确表述：有效沙盒需要**同时**具备文件系统与网络隔离。

- **无网络隔离**：可外泄 `~/.ssh`、凭据、源码。
- **无文件隔离**：可改配置拿持久权限，或通过本地 socket/代理逃逸到真网络。
- **网络路径**：子进程不直接连外网，而是经 **host 侧 proxy**（HTTP + SOCKS）；域 allow/deny 在 proxy 强制执行；Linux 常去掉 net ns，只留 Unix socket 到 proxy。

### 3.3 成熟 CLI 对照

#### Claude Code（本地 Bash 沙盒 + 云 VM）

| 维度 | 机制 |
| --- | --- |
| 覆盖面 | 主要包 **Bash 及其子进程**；内建 Read/Edit、MCP、hooks 不自动等同 |
| 平台 | macOS `sandbox-exec`/Seatbelt；Linux bubblewrap + socat；Windows 走 WSL2 或 `srt` alpha（独立 sandbox 用户 + WFP） |
| FS 默认 | 写：cwd + 会话 temp；读：默认宽，可用 deny/allow 收紧；敏感文件可 credentials 策略 deny/mask |
| 网络 | 默认无预置域名；经域代理；新域可会话提示；可 `tlsTerminate` 做 MITM 与密钥注入 |
| 与审批关系 | 沙盒 ≠ permission mode；sandbox 内可 auto-allow，仍尊重 deny/关键路径；失败可 `dangerouslyDisableSandbox` 回退到普通审批（可关） |
| 产品效果 | 内部数据：沙盒后 permission prompt **约降 84%**；缓解 approval fatigue |
| 云端 | Claude Code on the web：隔离 VM；**git 凭据不进 sandbox**，经 scoped proxy 推送指定分支 |
| 开源 | [`anthropic-experimental/sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime) / npm `@anthropic-ai/sandbox-runtime` |

`srt` 配置语义（可复用为设计参考）：

| 通道 | 默认 | 规则优先级 |
| --- | --- | --- |
| 读 | 默认允许 | deny-then-allow；`allowRead` 覆盖 `denyRead` |
| 写 | 默认拒绝 | allow-only；`denyWrite` 覆盖 `allowWrite` |
| 网络 | 默认拒绝 | allow-only 域；`deniedDomains` 优先 |
| Unix socket | 默认拒绝 | 平台差异（mac 可 path allowlist；Linux 常 seccomp） |

另有**强制写保护**（即使在 allowWrite 内）：如 `.bashrc`/`.zshrc`、`.gitconfig`、部分 `.claude/*`、IDE 配置目录等。

Anthropic 跨产品形态（2026-05 工程文）：

| 产品 | 模式 | 要点 |
| --- | --- | --- |
| claude.ai 代码执行 | 短暂 gVisor 容器 | 服务端、无用户本机 FS；多租户传统威胁模型 |
| Claude Code | HITL + OS 沙盒 | 开发者可理解 bash；沙盒内少问 |
| Claude Cowork | 本地全 VM（后演进为 agent loop 在 host、执行在 guest） | 非技术用户；凭据留 host keychain；mount 模式含 read-only / rw / rw-no-delete |

#### Codex CLI

| 维度 | 机制 |
| --- | --- |
| 模式 | `read-only` / **`workspace-write`（默认）** / `danger-full-access` |
| 平台 | macOS Seatbelt；Linux Landlock(+seccomp) 或 bubblewrap 回退；Windows 原生 restricted token/ACL |
| workspace-write | 工作区可写；**`.git` / `.codex` / `.agents` 保持只读**；`writable_roots` 扩写范围 |
| 网络 | **与模式解耦**：`workspace-write` 下默认 `network_access = false`；显式开启 |
| 审批 | 独立拨盘：`untrusted` / `on-request` / `never`；可对出沙箱/网络 escalate 询问 |
| 危险组合 | `danger-full-access` + `never` 仅适合**已有外层隔离**的一次性容器；禁止当笔记本默认 |
| 云环境 | **setup 阶段**可联网+密钥；**agent 阶段**默认无网且 **secrets 在 agent 前移除** |
| 读限制缺口 | 社区 issue：当前常“限写不限读”；完整 chroot 式只读根仍是诉求 |

推荐组合（文档共识）：

| 意图 | sandbox + approval |
| --- | --- |
| 不信任仓库探索 | `read-only` + `on-request` |
| 日常交互开发 | `workspace-write` + `on-request` |
| 只读 CI | `read-only` + `never` |
| 自带隔离的无人值守 | `danger-full-access` + `never` |

#### Gemini CLI

| 维度 | 机制 |
| --- | --- |
| 启用 | `-s` / `GEMINI_SANDBOX` / `settings.json tools.sandbox` |
| 后端 | Seatbelt 配置档；Docker/Podman；Windows native；可选 `runsc`(gVisor)、实验 LXC |
| Seatbelt 档 | `permissive-open`（默认：限写、网络开）→ `strict-proxied`（读写严 + 代理网络）等 |
| 容器语义 | 工作区以**相同绝对路径** bind mount，便于路径一致性 |

#### 其他产品取向（简）

| 产品 | 沙盒取向 |
| --- | --- |
| OpenHands | DockerWorkspace / 容器化 agent server |
| Open Interpreter | 默认真机；Docker/E2B 作可选安全模式 |
| Aider | 弱内建沙盒；依赖确认 + git 可回滚 + 外层 Docker |
| Goose | 工具权限 + 可选 Docker/MicroVM 类隔离配置 |
| Cursor / Devin / Copilot Coding Agent | 任务级云端/宿主沙盒（产品差异大） |
| Docker Sandboxes | 面向多 agent CLI 的 microVM 封装 |

### 3.4 与审批层的接口（两拨盘）

```text
┌──────────────────────┐     ┌──────────────────────┐
│ approval_policy /    │     │ sandbox_mode /       │
│ permission mode      │     │ FS+network profile   │
│ “问不问”             │     │ “能不能碰到”         │
└─────────┬────────────┘     └──────────┬───────────┘
          │                             │
          ▼                             ▼
     allow / ask / deny ──► execute in sandbox ──► (optional) escalate unsandboxed
```

成熟行为：

1. **沙箱内成功**：少问或 auto-allow。
2. **沙箱拒绝（EPERM）**：模型可见错误；或提示用户批准 **出沙箱重试**（应可被 org 禁用）。
3. **明确危险路径**：即使在沙箱 auto 模式仍 deny/ask（如关键 `rm`、deny 规则）。
4. **无人值守**：优先“紧沙箱 + never 审批”，而非“宽沙箱 + 自动全过”。

### 3.5 密钥与身份

收敛模式：

| 模式 | 做法 | 代表 |
| --- | --- | --- |
| 永不进入 | 凭据留 host；guest 用 scoped token | Cowork VM；Claude web git proxy |
| 分阶段存在 | setup 有 secret，agent 阶段剥离 | Codex cloud |
| 代理注入 | 沙箱内见 sentinel；仅允许域由 proxy 换真密钥 | Claude credentials `mask` + TLS terminate |
| 路径/env deny | 拒绝读 `~/.ssh`、`AWS_*` 等 | `srt` / Claude credentials 配置 |

原则：**“凭据从未进入沙箱”强于“模型被要求不要读凭据”**。

### 3.6 工作区信任与启动时序

Claude 公开事故模式：在 “Do you trust this folder?” **之前**解析项目 hooks/settings → 恶意仓库可在同意前执行。修复形态：

- 项目本地配置/hooks **延后到信任确认之后**；
- 把 project-open、config-load、localhost listener 当 inbound 请求，而非“本地故可信”。

与沙盒正交，但是沙盒体系的前置条件：沙盒配置本身若来自不可信树，可被用来**放宽**边界。

### 3.7 2026-07-17 证据补充：覆盖面、托管环境与生命周期

这次复核补充了一个容易遗漏的边界：**不能只问“是否启用 sandbox”，还要问哪些进程、凭据和网络路径确实穿过它。**

| 系统 / 形态 | 可核验机制 | 隔离不能自动覆盖的部分 |
| --- | --- | --- |
| Claude Code 内建 Bash sandbox | `Bash` 及子进程受 Seatbelt / bubblewrap 和代理约束；默认 cwd 与会话临时目录可写 | 内建 Read/Edit、MCP server、command hook 不在这一个 Bash 进程树内；且默认整机可读，敏感路径必须显式 deny |
| Claude whole-process runtime | `@anthropic-ai/sandbox-runtime` 可包装任意进程，采用 OS 级 FS 限制和 host proxy；容器 / VM 是更强的外层选择 | 同宿主内核仍不是 VM 边界；TLS 不解密时，域名 allowlist 不能约束请求内容 |
| Codex CLI 本地执行 | 官方开源源码显示权限 profile 与 approval 分离：`read-only` / `workspace-write` / disabled，平台后端为 Seatbelt、Linux sandbox 与 Windows restricted token | 这是命令执行边界，不应据此假定所有外部 MCP、hooks 或 GUI 进程都已被同一 OS policy 包住；需逐工具验证启动路径 |
| GitHub Copilot cloud agent | 每个任务运行在由 GitHub Actions 驱动的 ephemeral development environment；专用 Agents secrets 与其他 GitHub secret 类别隔离；可配置出网 firewall | 官方明示 firewall 只覆盖 agent 通过 Bash 启动的进程，不覆盖 MCP server、setup step 或 Actions appliance 外进程；不能视作完整安全方案 |
| E2B / Firecracker 类远程 sandbox | E2B 公开 self-host 栈使用 Firecracker；Firecracker 以 KVM guest 为主边界，叠加 jailer、seccomp、cgroups 和 namespaces | microVM 降低 guest-to-host / 租户横移，不会阻止 agent 滥用已注入的密钥或已允许的 egress |

托管环境还增加了两个本地 CLI 不那么显眼的控制点：

1. **sandbox controller 本身要鉴权**：E2B 的 secure access 使用 sandbox 创建时的 access token；关闭后，持有 sandbox ID 的任意主体可控制 controller API。控制平面权限不能只依赖 guest 内的文件权限。
2. **暂停不等于销毁**：E2B 文档说明 paused sandbox 可保留文件系统、内存、运行进程和已加载变量，直到显式 kill。因此含短期凭据的快照 / pause 应被视为凭据仍驻留，终止时要一起销毁或轮换。这是由产品语义导出的安全结论，不是供应商的自动保证。
3. **网络策略要在协议层验收**：Copilot 明示其 firewall 覆盖缺口；E2B 的 domain 规则仅基于 HTTP Host / TLS SNI，且对 DNS、UDP/QUIC 有文档化边界。不能以“端口已连接”或 agent 的工具提示代替端到端的 egress 测试。

对本地、云端和 microVM 都成立的结论是：将**整个 agent runtime**放进边界比仅包 shell 更强；将**凭据留在边界外的 broker / scoped token**比在 guest 内隐藏环境变量更强；将网络规则放到**无法被 guest 绕过的执行路径**比单纯设代理环境变量更强。

## 4. Efficient / reasonable patterns

### 4.1 默认配置（本地 coding agent）

合理默认（多产品交汇）：

```text
filesystem.write  = workspace (+ session temp) ; deny shell/git meta configs
filesystem.read   = broad OR deny home-secrets ; optional workspace-only recipe
network           = deny-by-default ; proxy allowlist when needed
unix sockets      = deny (esp. docker.sock)
approval          = on-request for sandbox escape / network enable
visibility        = show mode + sandboxed? + decision reason
```

### 4.2 何时选哪一层

| 场景 | 更合适的沙盒 |
| --- | --- |
| 本机交互开发、要真 `npm`/`pytest` | OS 原语（Seatbelt/bwrap/Landlock） |
| 不信任依赖/要可复现镜像 | 容器 / devcontainer |
| 多租户托管代码执行 | gVisor 或 microVM |
| 非技术用户桌面 agent | 全 VM + 用户选 mount |
| 仅文本变换、浏览器侧 | 模拟 shell / WASM |
| CI 只读分析 | read-only + 无网 + never 审批 |

### 4.3 设计模式清单

1. **两拨盘显式化**：`sandbox_*` 与 `approval_*` 分开配置、分开展示。
2. **出界可观测**：`Operation not permitted` / proxy blocked 应成为模型可消费的结构化错误。
3. **网络当能力授予**：域 + 方法（GET/HEAD）+ 可选 body/API 路径策略；关键域 MITM。
4. **强制 deny 列表**：shell rc、git hooks/config、agent 自身配置目录。
5. **symlink 先于路径校验解析**。
6. **escape hatch 可关**：`allowUnsandboxedCommands=false` / managed policy。
7. **调试入口**：`srt <cmd>` / `codex sandbox …` 让用户复现边界。
8. **审计**：记录 sandbox on/off、network decision、是否 unsandboxed 重试。

### 4.4 与“少工具 vs 真 bash”的关系

业界观察：与其暴露 50 个窄工具，不如 **bash + 沙盒**。沙盒是让 bash 可交付的前提，而不是事后补丁。

## 5. Pitfalls

| 坑 | 说明 |
| --- | --- |
| 只做命令字符串黑名单 | 易被 `$IFS`、编码、间接执行绕过；且不限制已允许二进制的副作用 |
| 只限写、不限读 | 默认可读密钥；Codex/多数 OS 沙盒历史姿态 |
| 开网却无代理/方法限制 | POST 外泄；供应链下载 |
| allowlist 当“安全域” | Anthropic：`api.anthropic.com` + 攻击者 key → Files API 外泄；需 session token 绑定 |
| 沙盒仅包 bash | 文件工具/MCP/hooks 仍可能全权；覆盖面需写清 |
| Docker socket 放进 allow | 等价于宿主 root 级逃逸面 |
| approval fatigue | 93% 点同意量级时，审批单独不足；要用边界换少问 |
| 信任前执行 | hooks/settings 在 trust dialog 前加载 |
| 自建代理最弱 | 公开复盘：hypervisor/gVisor 稳，自研 allowlist proxy 出事 |
| VM 与 EDR | 隔离同时挡掉主机 EDR 可见性；需另建遥测 |
| Ubuntu userns 限制 | 24.04+ AppArmor 限制 unprivileged userns → bwrap 失败 |
| nested Docker 弱化 | 需要 weaker nested sandbox 开关时安全语义变化 |
| 平台语义漂移 | mac glob vs Linux 字面路径；网络 flag 在 Seatbelt 被忽略等 bug |

## 6. Open questions

- **读限制默认是否应收紧到 workspace-only？** 安全更好，但破坏“读系统头文件/全局工具配置”的本地开发体验；Codex 仍有增强 issue。
- **TLS 终结的默认开法**：能做请求级策略与密钥 mask，但破 mTLS/证书钉扎，运维成本高。
- **进程沙盒 vs 整 agent 进沙盒**：整进程更强，但调试、GUI、MCP 本地交互更痛；Claude 本地偏 bash 子集，Cowork 偏 VM。
- **Windows 一等支持成熟度**：各家从“不支持 / WSL / alpha 独立用户”到 native sandbox，API 未完全对齐。
- **多 agent 信任升级**：子 agent 输出被当成高信任“自己人”时，隔离收益可能被抵消。
- **策略即代码的跨工具标准**：OPA/hooks/企业 MDM mount allowlist 正在长，但无统一 schema。

## 7. 机制速查表

| 控制点 | Claude Code / srt | Codex CLI | Gemini CLI | 云/托管 |
| --- | --- | --- | --- | --- |
| FS 写 | cwd+temp allow-only | workspace-write + protected paths | Seatbelt/container mount | 镜像内盘或挂载卷 |
| FS 读 | 默认可读，可 deny | 通常宽；强化中 | 档位从 permissive→strict | 会话盘 |
| 网络 | proxy allowlist | workspace 默认关 | open 或 proxied 档 | 阶段化 / 租户策略 |
| 实现 | Seatbelt / bwrap | Seatbelt / Landlock / Win token | Seatbelt / Docker / … | gVisor / Firecracker / 全 VM |
| 与审批 | 沙盒内 auto，出界再问 | 独立 approval_policy | 工具 sandbox 开关 | 常无 HITL，靠边界 |
| 密钥 | 不进 web sandbox；mask/deny | cloud agent 前剥离 | 依赖镜像/挂载纪律 | scoped token + proxy |

## References

主要来源（2025–2026；采用前请复核原文）：

1. Anthropic Engineering, *Beyond permission prompts: making Claude Code more secure and autonomous with sandboxing* (2025-10-20) — https://www.anthropic.com/engineering/claude-code-sandboxing  
2. Anthropic Engineering, *How we contain Claude across products* (2026-05-25) — https://www.anthropic.com/engineering/how-we-contain-claude  
3. Anthropic Sandbox Runtime README — https://github.com/anthropic-experimental/sandbox-runtime  
4. Claude Code docs: sandboxing / sandbox environments — https://code.claude.com/docs/en/sandboxing （及 sandbox-environments）  
5. OpenAI Codex: sandboxing concept & agent approvals/security — https://developers.openai.com/codex/concepts/sandboxing , https://developers.openai.com/codex/agent-approvals-security  
6. OpenAI, *Building a safe sandbox for Codex on Windows* — https://openai.com/index/building-codex-windows-sandbox/  
7. Codex issue: read-restricting sandbox mode — https://github.com/openai/codex/issues/7657  
8. Gemini CLI sandboxing docs — https://geminicli.com/docs/cli/sandbox/  
9. Michael Livs, *A thousand ways to sandbox an agent* (2026) — https://michaellivs.com/blog/sandbox-comparison-2026/  
10. Simon Willison on Anthropic containment overview (2026-05-30) — https://simonwillison.net/2026/May/30/how-we-contain-claude/  
11. Coding agent sandbox landscape gist — https://gist.github.com/wincent/2752d8d97727577050c043e4ff9e386e  
12. Anomity guide on Codex sandbox + approval two dials (2026-06) — https://anomity.ai/blog/securing-openai-codex-sandbox-and-approvals-guide/  
13. DigitalApplied Codex config/profiles/sandbox deep dive (2026) — https://www.digitalapplied.com/blog/codex-cli-deep-dive-config-profiles-sandbox-2026  
14. Docker Sandboxes product docs — https://docs.docker.com/ai/sandboxes/  
15. OpenHands Docker sandbox guide — https://docs.openhands.dev/sdk/guides/agent-server/docker-sandbox  
16. OpenAI Codex source snapshot (commit `315195492c80fdade38e917c18f9584efd599304`, accessed 2026-07-17): permission profiles and platform sandbox selection — https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/core/src/config/permissions.rs , https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/sandboxing/src/manager.rs
17. Anthropic, *Configure the sandboxed Bash tool* and *Choose a sandbox environment* (accessed 2026-07-17) — https://code.claude.com/docs/en/sandboxing , https://code.claude.com/docs/en/sandbox-environments
18. Anthropic Sandbox Runtime README (accessed 2026-07-17) — https://github.com/anthropic-experimental/sandbox-runtime
19. GitHub Docs, *About Copilot coding agent* / cloud-agent firewall and secrets (accessed 2026-07-17) — https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-cloud-agent , https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/customize-the-agent-firewall , https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/configure-secrets-and-variables
20. E2B docs, *Secured access*, *Internet access*, *Sandbox persistence* (accessed 2026-07-17) — https://e2b.mintlify.app/docs/sandbox/secured-access , https://e2b.mintlify.app/docs/network/internet-access , https://e2b.mintlify.app/docs/sandbox/persistence
21. E2B self-host source and Firecracker design (accessed 2026-07-17) — https://github.com/e2b-dev/infra/blob/main/self-host.md , https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md

Evidence note: the public `developers.openai.com` pages returned HTTP 403 to this research environment on 2026-07-17. Codex-specific implementation claims in item 16 were therefore cross-checked against the official open-source snapshot rather than inferred from secondary posts.

Related local research notes (structure only, not industry evidence):

- [cli-command-permissions-research.md](./cli-command-permissions-research.md) — 审批/规则栈中沙盒所处层级  
- [tool-call-control-termination-research.md](./tool-call-control-termination-research.md) — 执行门禁在 agent 循环中的位置  
