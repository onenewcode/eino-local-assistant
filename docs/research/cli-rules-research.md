# 命令行规则（CLI Rules）：业界方案调研与本仓库落地建议

> 状态：调研结论与通用实施建议，不代表任何具体能力已经在本仓库实现。
>
> 初始调研日期：2026-07-15；本地覆盖合同复核日期：2026-07-31。产品行为、文件约定和安全模型会持续变化，实际采用前应重新核验引用资料。
>
> 资料范围：官方文档 + 社区实践（Reddit / 博客）+ 多工具对比 + 开源实现思路；**不限于官方文档**。

## 1. 摘要

「命令行规则」在成熟 CLI coding agent 里不是单一文件，而是**三层系统**：

```text
1. 软规则（Soft instructions）
   AGENTS.md / CLAUDE.md / CONVENTIONS.md / .cursor/rules ...
   → 注入上下文，指导模型“怎么做事”
   → 可被忽略、被淹没、被冲突指令覆盖

2. 硬规则（Hard policy）
   allow/deny、approval mode、sandbox、hooks/execpolicy
   → 客户端在工具调用前强制执行
   → 模型无法靠“说服自己”绕过

3. 产品交互规则（Product UX rules）
   斜杠命令、中断/排队、resume、权限切换、/status 可见性
   → 决定用户如何观察和控制 agent
```

业界共识可以压缩成四句话：

1. **软规则管行为，硬规则管安全**；把“禁止 `rm -rf /`”只写在 markdown 里是安全剧场。
2. **短、具体、可验证的规则**比长文更有效；过长指令会降低遵守率并挤占工作上下文。
3. **跨工具可移植的项目真相源优先用 `AGENTS.md`**；Claude/Cursor 等用薄适配层（symlink / `@import` / 工具专属 rules）。
4. **规则要分层加载与预算化**：全局 → 项目 → 本地覆盖 → 路径/任务按需；永远-on 内容必须极短。

对本仓库（Eino 本地助手）的最小结论：

| 现状 | 缺口 |
| --- | --- |
| 仅有 `assistant.system_prompt` 作为 thread 不可变指令 | 无项目规则文件发现/合并/预算 |
| `run_command` 默认开启、`sh -c`、无审批、无沙箱 | 无硬策略层；权限等同启动用户 |
| 已有 slash、thread ledger、hybrid compaction | 规则未进入 ImmutableMessages / 可观测性 |

推荐分三期落地，而不是一次做完：

1. **v1 规则加载器**：发现 + 合并 + 注入 system/immutable + `/rules` 可见。
2. **v1.5 命令策略**：deny/allow/ask 前缀策略 + 默认审批模式（可关）。
3. **v2 深化**：路径作用域 rules、sandbox、hooks、skills 按需加载。

---

## 2. 术语与问题边界

| 术语 | 含义 | 不应混同为 |
| --- | --- | --- |
| 项目指令 / Project instructions | 写入仓库、描述“本项目怎么开发”的 markdown（`AGENTS.md` 等） | 模型厂商 system card、或用户随口一句偏好 |
| 用户全局规则 | 跨仓库的个人偏好（`~/.claude/CLAUDE.md`、`~/.codex/AGENTS.md`） | 某次会话的临时补充 |
| 软规则 | 进入 prompt 的自然语言约束 | 操作系统强制隔离 |
| 硬策略 / Policy | 工具调用前的 allow/deny/ask、沙箱、hook 拒绝 | 写在 prompt 里的“请不要……” |
| 审批模式 / Approval policy | 何时打断用户确认 | 权限系统本身（二者常叠加） |
| 沙箱 / Sandbox | FS/网络/进程级隔离 | 仅靠模型“自觉” |
| Progressive disclosure | 按路径/任务按需加载细节规则 | 启动时把所有文档塞进 system |
| Memory | agent 自学或用户沉淀的可变笔记 | 团队共识的项目规则（应提交 git） |
| Skill / Recipe | 可调用的任务流程说明 | 始终生效的项目惯例 |
| 命令行规则（本报告用语） | 上述软规则 + 硬策略 + CLI 交互约定的总称 | 仅指 shell allowlist，或仅指 `AGENTS.md` |

本报告回答：

- 成熟工具如何定义、存储、加载、执行规则？
- 哪些模式在社区里真正有效，哪些会失败？
- 本仓库应如何设计与文档化自己的规则系统？

---

## 3. 为什么 CLI agent 必须有“规则系统”

### 3.1 模型默认不知道仓库约定

Coding agent 每轮都在“新入职”。没有规则时，它会：

- 猜错包管理器 / 测试命令 / monorepo 布局；
- 用全量 `go test ./...` 或全量 build 浪费时间；
- 改不该动的生成文件、忽略现有抽象；
- 在安全边界上过度自信。

社区与产品文档反复强调：把**你会重复纠正的事实**写成规则，而不是每会话口述。[1][2][3]

### 3.2 上下文是注意力预算

规则进入 system/immutable 后，会与对话、工具输出竞争注意力。Anthropic 与社区对 Claude Code 的一致警告是：**臃肿的 `CLAUDE.md` 会让模型忽略真正重要的指令**。[2][4]

因此规则系统不仅是“多写点说明”，而是：

```text
哪些必须永远在场？
哪些按路径加载？
哪些变成 skill 按需调用？
哪些必须变成硬策略而不是文字？
```

### 3.3 本仓库的特殊风险面

当前 `run_command`：

- 通过 `sh -c` 执行；
- 默认开启；
- 无交互审批；
- 无沙箱；
- 仅有 timeout 与 stdout/stderr 字节上限。

这在“本机个人工具”场景可接受，但意味着：**任何进入模型上下文的内容（含工具输出、网页、恶意 README）都可能诱导执行危险命令**。软规则无法单独解决该问题。

---

## 4. 生态地图：文件约定与加载模型

### 4.1 横向对比

| 工具 | 主规则文件 | 模块化规则 | 全局层 | 本地覆盖 | 加载方式 | 硬策略层 |
| --- | --- | --- | --- | --- | --- | --- |
| **Claude Code** | `CLAUDE.md` / `.claude/CLAUDE.md` | `.claude/rules/**/*.md`（可 path 作用域） | `~/.claude/` + 企业 managed policy | `CLAUDE.local.md`（与同目录共享文件都加载） | 祖先链启动拼接；子目录按需；`@import` | permissions allow/deny/ask + hooks + sandbox |
| **Codex CLI** | `AGENTS.md` / `AGENTS.override.md` | 目录嵌套 + fallback 文件名 | `~/.codex/AGENTS.md` | `AGENTS.override.md`（替代同目录普通文件） | project root→cwd，每目录至多一个候选；有字节上限 | sandbox_mode + approval_policy + execpolicy `prefix_rule` |
| **Cursor** | 现多读 `AGENTS.md`；旧 `.cursorrules` | `.cursor/rules/*.mdc`（globs / always / agent-decided） | 用户规则 | 本地规则 | 按类型附加 | 产品内权限/模式（工具侧） |
| **GitHub Copilot** | `.github/copilot-instructions.md` | `.github/instructions/*.instructions.md` | 组织策略 | — | 仓库级自定义指令 | 组织策略/策略控制 |
| **Aider** | 任意 md，常 `CONVENTIONS.md` | 多文件 `--read` / conf `read:` | conf 可列路径 | — | 默认手动或 conf 自动 read | 无完整沙箱产品层；靠确认与模型 |
| **Cline** | `.clinerules` 或目录 | 多文件 + path frontmatter | 全局 Rules 目录；也读 `AGENTS.md` | UI 开关 | 自动发现 | 工具批准流 |
| **Continue** | `.continue/rules/` | globs / regex / alwaysApply | 用户 config | — | 自动 + agent 可创建 | 模式相关 |
| **OpenHands** | `AGENTS.md`（也认 `CLAUDE.md`/`GEMINI.md`） | Skills / microagents 触发加载 | org/user skills | — | 仓库上下文 + progressive skills | runtime 隔离取决于部署 |
| **Gemini CLI** | `GEMINI.md`；生态向 `AGENTS.md` 靠拢 | 扩展/kit 生成 | 用户配置 | — | 产品相关 | 产品相关 |
| **Goose** | `.goosehints` / `AGENTS.md` | recipes（流程）≠ rules（惯例） | 持久 MOIM 指令 | — | session / every-turn / recipe 作用域 | macOS sandbox domain blocklist 等 |
| **Open Interpreter** | system_message / profile | — | profile | — | 配置注入 | 默认确认执行；`auto_run`；实验 safe_mode；容器隔离 |

补充观察：

- **2025–2026 的可移植共识是 `AGENTS.md`**（“README for agents”），多工具与基金会叙事都在推动它成为跨产品真相源。[1][5][6]
- **Claude Code 原生读 `CLAUDE.md` 不读 `AGENTS.md`**；官方建议用 `CLAUDE.md` 内 `@AGENTS.md` 或 symlink 桥接。[9]
- **Cursor 旧 `.cursorrules` 已软废弃**，新项目应 `AGENTS.md` + 可选 `.cursor/rules`。[5]
- Aider 的优点是极简与 cache 友好：小文件只读加载，不发明复杂 schema。[7]

#### 4.1.1 本地覆盖与层级选择合同复核（2026-07-31）

本次复核的决策面是：**共享项目指令与本机私有指令同时存在时，产品选择哪些文件、以什么顺序放入上下文、作用到哪棵目录，以及何时重新读取。**仅讨论 Codex CLI 与 Claude Code 已公开的文件合同；不讨论 auto memory、权限执行、模型内部冲突消解，也不据此推断任何私有实现。

| 维度 | Codex CLI | Claude Code |
| --- | --- | --- |
| 全局 / 用户层 | `$CODEX_HOME`（默认 `~/.codex`）先尝试 `AGENTS.override.md`，否则 `AGENTS.md`；取第一个非空文件 [8][19] | managed policy → `~/.claude/CLAUDE.md` → project → local，官方表按“宽到窄”列出加载顺序 [9] |
| 项目同目录选择 | 按 `AGENTS.override.md` → `AGENTS.md` → 配置的 fallback 名称检查，**每目录至多选择一个**；override 不是在同目录 base 之后追加；固定源码明确允许符号链接 [8][19] | `CLAUDE.md` 与 `CLAUDE.local.md` 都会被发现并拼接；同目录中 local 排在 base 后，不会替换 base [9] |
| 跨目录顺序 | 从 project root（通常 Git root）到启动 cwd；越近 cwd 的文件越晚进入 prompt。找不到 root 时只检查 cwd，并在 cwd 停止，不向下预读 [8][19] | 从文件系统根到启动 cwd 拼接祖先文件；越近 cwd 的内容越晚。cwd 下的文件在 Claude 读取该子目录文件时才惰性加载 [9] |
| 加载 / 刷新时机 | 用户文档合同是每次命令或 TUI session 启动时构建；同路径文件变更后应重启。固定提交源码还显示：普通 turn 复用创建时快照，而 turn 的环境 / cwd 选择变化会刷新，这是实现观察而非稳定文档承诺 [8][19] | 每个 conversation 启动加载祖先链，子目录按读取触发；`/compact` 后重新读取 project-root 文件，嵌套文件等下次访问再加载。`InstructionsLoaded` hook 可观察 `session_start`、`nested_traversal`、`include`、`compact` 等原因 [9][21] |
| worktree 边界 | Codex-managed worktree 会自动复制被忽略的 `AGENTS.override.md`，无需列入 `.worktreeinclude` [20] | 被 gitignore 的 `CLAUDE.local.md` 只存在于创建它的 worktree；要跨 worktree 共享，官方建议从 home 目录 import [9] |

这里的“后置 / 更具体”只是**上下文顺序**，不是硬覆盖算法。Claude Code 明确警告：矛盾指令可能被模型任意选择；Codex 文档所说“closer files override”也没有把自然语言冲突提升为客户端强制策略。[8][9]

证据仍有四处边界：

1. Codex 文档同时写了“按候选顺序、每目录一个”和“跳过空文件”；当前项目级源码先选第一个存在的候选，再忽略空内容，而全局加载器会对空 override 继续尝试 base。公开测试没有明确锁定“项目目录中空 override + 非空 base”的长期合同，因此不应依赖该边界。[8][19]
2. Codex 用户文档以“一次 run / TUI session 一次”为刷新合同；当前源码对 turn 环境选择变化有额外刷新路径。二者并非必然冲突，但后者尚未成为同等清晰的用户承诺。[8][19]
3. Claude 文档允许 project instructions 放在 `./CLAUDE.md` 或 `./.claude/CLAUDE.md`，但未明确说明两者同时存在时是否都加载及其相对顺序；可观测时应以 `/context` 或 `InstructionsLoaded` 为准。[9][21]
4. Claude 文档说明启动、惰性加载与 compact 重载，但未承诺已加载文件在同一会话内被编辑后立即热刷新；Codex 则明确建议遇到陈旧内容时重启。[8][9]

### 4.2 软规则内容“该写什么”

跨 Builder.io、Anthropic、agents.md、社区模板的高频有效段落：[2][3][6]

1. **可执行命令**：install / dev / test / lint / 单文件检查（优先给 file-scoped 命令）。
2. **仓库地图**：关键入口文件、包边界、生成代码位置（短索引，不是百科）。
3. **硬性约定**：语言版本、错误处理、禁止模式、测试门槛。
4. **好/坏例子**：指向真实文件路径，比抽象原则更有效。
5. **安全与权限期望**：哪些命令可自动跑、哪些必须先问。
6. **卡住时怎么办**：缩小范围、提问、不要大范围猜测性重构。
7. **明确不要写**：可从代码读出的事实、易变细节、长教程、空话（“写干净代码”）。

### 4.3 推荐的软规则体量

| 层 | 建议体量 | 理由 |
| --- | --- | --- |
| 根 `AGENTS.md` / `CLAUDE.md` | 约 50–200 行；尽量 &lt; ~2k–4k tokens | 过长会降遵守率、占 immutable 预算 [2][4] |
| 主题 rules 单文件 | 一关注点一份，几十行级 | 便于 path 作用域与评审 |
| 全局用户规则 | 极短：语气、默认工作流 | 每个项目都会加载 |
| 永远-on 总量 | 必须适配模型 context 的固定预算 | 本仓库已有 `ImmutableOverBudget` 概念 |

官方/社区反复出现的剪枝标准：

> 删掉这条，模型会不会稳定犯错？不会就删。[2]

---

## 5. 层次、优先级与注入位置

### 5.1 常见优先级（从宽到窄）

```text
Managed / 企业策略
  → 用户全局规则
    → 仓库根项目规则
      → 嵌套目录 / 包级规则（近者优先或追加）
        → 本地覆盖（*.local.md，通常 gitignore）
          → 路径作用域 / 任务 skill（按需）
            → 当前用户消息（最高意图）
```

细节因产品而异：

| 产品 | 合并语义（实践中） |
| --- | --- |
| Codex | 每目录从 override / base / fallback 中选一个，再合并发现链；更靠近 cwd 的内容后置；有 `project_doc_max_bytes` 上限 [8][19] |
| Claude Code | 多文件拼接进上下文；同目录 local 在 base 后；子目录 lazy；path rules 触碰相关文件时加载 [9][21] |
| agents.md 叙事 | nested **nearest wins** [1][5] |
| Cline / Continue | 工作区覆盖全局；globs 决定是否进入本轮 |

**工程建议（本仓库）**：采用显式、可测试的合并算法，并在 UI 展示最终生效文本的来源列表，而不是“隐式魔法覆盖”。

建议合并算法 v1：

```text
parts = []
append(global)                 # ~/.eino-assistant/AGENTS.md
append each project file from repo root → cwd:
  AGENTS.override.md if exists else AGENTS.md
  (optional fallbacks: CLAUDE.md, CONVENTIONS.md — 可配置)
append(local)                  # AGENTS.local.md gitignored
join with clear source headers
truncate by token/byte budget from the *middle/low-priority* side,
never silently drop the nearest project core without surfacing a warning
```

### 5.2 注入位置：system vs 工具 vs 每轮

| 注入位置 | 适合 | 风险 |
| --- | --- | --- |
| Thread 创建时的 immutable system | 稳定身份 + 项目核心规则 | 规则更新后旧 thread 仍用旧快照（需定义策略） |
| 每轮重新编译的 prefix | 希望规则热更新 | 与“thread 不可变 system”模型冲突，需版本化 |
| 工具描述 / tool preamble | 命令安全、输出截断语义 | 不宜塞长文项目惯例 |
| 独立 user/system 段 “Project rules” | 可观测、可截断 | 要防止被当成不可信数据混淆 |
| Hook / policy engine（不进模型） | 安全硬约束 | 不替代说明性规则 |

本仓库现状：`SystemPrompt` 在 `CreateThread` 时写入 journal，并作为 `ImmutableMessages` 前缀。[见 `internal/chat/session.go`、`internal/store/thread.go`]

这与 Claude/Codex“启动加载规则”接近，但目前**没有项目文件发现**，也**没有规则版本字段**。

推荐语义：

```text
effective_instructions =
  product_base_prompt          # 产品固定行为（工具用法、截断语义、不编造）
  + user_config.system_prompt  # config.yml 中的助手人设
  + compiled_project_rules     # 规则加载器输出
```

并在 thread meta 记录：

- `rules_digest`（hash）
- `rules_files[]`（路径列表）
- `rules_bytes` / `rules_tokens`

`/resume` 时可选：

1. **冻结**（默认）：沿用创建时规则，保证可复现；
2. **刷新**：`--reload-rules` 重编译并写新事件（显式）。

### 5.3 软规则 vs 硬策略：必须拆开

| 需求 | 正确层 |
| --- | --- |
| “测试用 `go test ./internal/foo`” | 软规则 |
| “提交信息用约定格式” | 软规则 |
| “禁止 `rm -rf`、禁止读 `~/.ssh`” | **硬策略** |
| “`npm install` 需审批” | **硬策略 / approval** |
| “workspace 外写入需升级权限” | **sandbox + approval** |
| “每次编辑后跑 formatter” | hook / skill，不是长 prose |

Claude Code 明确：markdown 指令是 soft；必须执行用 hooks/permissions。[9]  
Codex 明确：sandbox 与 approval 是两层，可再加 execpolicy 前缀规则。[8][10]

---

## 6. Shell / `run_command` 安全规则

> 专题深挖（审批 UX、风险分级 L0–L3、策略预设、威胁走查、分阶段权限路线）见  
> **[cli-command-permissions-research.md](./cli-command-permissions-research.md)**。本节保留总览，避免与专题文重复维护细节。

### 6.1 成熟产品的两层安全模型

```text
         ┌──────────────────────────────┐
         │  Approval policy（社会层）    │
         │  never / on-request /        │
         │  untrusted / dontAsk ...     │
         └──────────────┬───────────────┘
                        │
         ┌──────────────▼───────────────┐
         │  Sandbox / execpolicy（技术层）│
         │  read-only / workspace-write │
         │  prefix allow|prompt|forbid  │
         │  hooks PreToolUse            │
         └──────────────────────────────┘
```

**Codex（公开模型）** [8][10]：

| 维度 | 典型值 | 含义 |
| --- | --- | --- |
| `sandbox_mode` | `read-only` / `workspace-write` / `danger-full-access` | 能碰哪里 |
| `approval_policy` | `untrusted` / `on-request` / `never` / granular | 何时问人 |
| execpolicy | Starlark `prefix_rule(pattern, decision=allow\|prompt\|forbidden)` | 命令前缀策略 |
| 保护路径 | 即使 workspace-write 也可能限制 `.git` 等 | 减少自毁 |

**Claude Code（公开模型）** [9][11]：

| 维度 | 机制 |
| --- | --- |
| permission mode | default / acceptEdits / plan / dontAsk / bypassPermissions |
| rules | allow / deny / ask 模式匹配（如 `Bash(git status:*)`） |
| hooks | PreToolUse 可 allow/deny/modify |
| sandbox | Bash 可在受限环境执行（与权限模式组合） |

**Open Interpreter** [12]：

- 默认执行前确认；`auto_run` 关闭确认；
- `safe_mode` + semgrep 是实验层；
- **没有一等公民命令 allowlist**；社区安全分析建议自建；
- 真隔离靠 Docker/E2B。

**Goose** [13]：

- rules/hints ≠ recipes；
- 关键约束可用 every-turn persistent instructions；
- Desktop sandbox 有 domain blocklist；shell allow/deny 仍是演进中需求。

### 6.2 只靠 denylist 不够

公开讨论与安全研究的稳定结论：

1. **字符串 denylist 可被编码/拼接/间接调用绕过**（`base64|sh`、`python -c`、`env`、`xargs` 等）。
2. **`sh -c` 本身就是策略分析难点**：复杂 shell 语法使“前缀匹配”不完备。
3. 因此工业方案是 **默认拒绝 + 小 allowlist + 沙箱 + 审批**，而不是维护无穷黑名单。
4. 对本地个人 CLI，可接受的务实序列是：

```text
Phase A: 危险模式 deny + 超时/输出上限 + 默认 ask（高危）
Phase B: 可信前缀 allow（go test、git status、rg…）
Phase C: workspace sandbox（只能写仓库与 tmp）
Phase D: 可选 yolo / danger-full-access（显式、可审计）
```

### 6.3 对本仓库 `run_command` 的差距清单

| 能力 | 现状 | 建议 |
| --- | --- | --- |
| 执行器 | `sh -c` | 保留，但策略层先解析/分类；长期可提供 `argv[]` 工具减少歧义 |
| 审批 | 无 | `tools.run_command.approval: never\|on_request\|untrusted` |
| deny/allow | 无 | 配置化 prefix/regex 规则；**deny &gt; ask &gt; allow** |
| 工作目录 | 可指定；默认进程 cwd | 默认钳制在 workspace root；越界 ask/deny |
| 环境 | 继承用户环境 | 可选剥离敏感 env；禁止改 shell rc |
| 输出 | 64KiB 默认截断 | 保持；策略命中应返回结构化拒绝原因给模型 |
| 可观测 | 工具卡显示命令 | 增加 policy decision 字段（allowed/asked/denied） |
| 关闭开关 | `disabled: true` | 保留 |

示例策略配置（建议形态，尚未实现）：

```yaml
tools:
  run_command:
    disabled: false
    timeout_seconds: 60
    max_output_bytes: 65536
    workspace_only: true
    approval: on_request   # never | on_request | untrusted
    policy:
      # 最严优先
      - { decision: deny,  prefix: ["rm", "-rf", "/"] }
      - { decision: deny,  match: "(?i)curl\\s+.*\\|\\s*(ba)?sh" }
      - { decision: ask,   prefix: ["git", "push"] }
      - { decision: ask,   prefix: ["go", "get"] }
      - { decision: allow, prefix: ["go", "test"] }
      - { decision: allow, prefix: ["git", "status"] }
      - { decision: allow, prefix: ["rg"] }
      - { decision: ask,   match: ".*" }   # default
```

说明：

- **markdown 规则里可以重复写“先跑单包测试”**，但 **deny/ask 必须在 Go 策略引擎执行**；
- 策略拒绝应是 **soft tool result**（带 `denied=true` 与原因），让模型改命令，而不是 ReAct 崩溃；
- 与现有“非零退出是软结果”的设计一致。

---

## 7. 产品交互规则（CLI UX rules）

软/硬规则之外，Claude Code 与 Codex 还通过**交互协议**定义“合法行为”。本仓库 `AGENTS.md` 已要求对齐二者。应视为规则系统的第三支柱：

| 主题 | 成熟行为 | 与规则系统的关系 |
| --- | --- | --- |
| 斜杠命令 | `/help` `/status` `/compact` `/resume`… | 应有 `/rules` 展示生效规则与策略模式 |
| 中断 | Esc 取消当前 turn/工具 | 策略询问 UI 也需可取消 |
| 排队 | busy 时入队；local slash 可插队 | 审批提示不应打乱队列语义 |
| 会话 | 多 session、resume、recover | 规则 digest 应随 thread 可审计 |
| 权限切换 | 会话内切换 approval/sandbox | 变更应写 journal 事件 |
| 可见性 | 工具卡、tokens、ctx% | 规则截断/拒绝要可见，避免静默 |

**不要把 UX 规则只写进 system prompt。** 它们属于产品状态机；prompt 只描述“你现在处于何种模式”。

---

## 8. 编写模式：什么有效

### 8.1 有效模式

1. **祈使 + 可验证**  
   - 好：`使用 go tool golangci-lint run ./...`  
   - 无效：`注意代码质量`
2. **DO / DON’T 对照**  
   社区与 Builder 指南均强调显式反例。[3][6]
3. **指向仓库内真实文件**  
   `错误处理照抄 internal/store/thread.go 的包装方式`
4. **file-scoped 命令**  
   避免默认全量测试/构建。[3]
5. **根文件做路由，细节拆分**  
   根 `AGENTS.md` 短；`rules/testing.md`、`rules/tui.md` 分治。[2][4]
6. **第二次犯同样错再沉淀规则**  
   避免预写百科。
7. **跨工具单真相源**  
   以 `AGENTS.md` 为 canonical；`CLAUDE.md` / `.cursor/rules` 做薄适配。[5][6]

### 8.2 模板（建议作为本仓库文档示例）

```markdown
# AGENTS.md

## 项目一句话
本地 Eino CLI 编程助手（Go + Bubble Tea TUI + thread ledger）。

## 常用命令
- 测试：`go test ./...`
- 构建：`go build ./...`
- 风格：`go tool golangci-lint run ./...`
- 单包：`go test ./internal/tui -count=1`

## 架构边界
- 原始 thread journal 不可因 compact 改写
- 工具大输出进 artifact；模型侧看 digest
- 对照 Codex / Claude Code 的 UX，不从零臆造

## 编码约定
- 新增依赖：`go get <module>@latest`（不要手写版本号）
- 风格以 golangci 聚合配置为准

## 不要做
- 不要提交 config.yml 或密钥
- 不要在没有说明时引入新的全局状态旁路 thread store
- 不要假设 run_command 有沙箱

## 改动门槛
每次可交付变更至少：
`go test ./... && go build ./... && go tool golangci-lint run ./...`
```

### 8.3 Memory / Rules / Skills 分工

| 类型 | 谁写 | 生命周期 | 用途 |
| --- | --- | --- | --- |
| Rules（AGENTS.md） | 人 / 团队 | 版本控制、长期 | 稳定惯例 |
| Memory | agent 或个人 | 机器本地、可删 | 调试笔记、个人偏好 |
| Skills / Recipes | 人 | 按需加载 | 发布、迁移、复盘流程 |
| Hooks / Policy | 人 | 强制 | 安全与门禁 |

混用是常见失败源：把一次性调试笔记写进团队 `AGENTS.md`，或把“禁止推送 main”只写进 memory。

---

## 9. 失败模式（社区高频）

| 失败 | 表现 | 缓解 |
| --- | --- | --- |
| Rule bloat | 模型“无视”CLAUDE/AGENTS | 删到 &lt;200 行核心；其余 path/skill |
| 冲突指令 | 根与包级互相矛盾 | nearest-wins + 检测重复关键；CI 检查 |
| 安全剧场 | 只在 md 写禁止项 | 硬策略 + 默认 ask |
| 旧 thread 冻住旧规则 | 改了 AGENTS 但 resume 仍旧行为 | 显示 rules_digest；提供刷新 |
| 静默截断 | 超长规则被砍掉用户不知 | `/rules` + 启动警告 |
| 工具输出投毒 | stdout 含 “ignore rules…” | 明确 untrusted evidence 边界（本仓库 artifact 已有类似措辞） |
| allowlist 过宽 | `Bash(*)` / yolo 常态化 | 分模式；危险模式要显式 |
| denylist 幻想 | 以为正则能拦一切 | 沙箱 + 工作区钳制 |
| 多文件漂移 | AGENTS/CLAUDE/cursorrules 各写一份 | 单真相源 + 生成/链接 |
| UX 不可见 | 用户不知道当前能否自动跑命令 | 状态栏显示 `policy=ask` / `sandbox=off` |

Reddit 等社区对 Claude Code 的抱怨高度集中在：**文件太长、规则被忽略、期望 soft 规则当 hard config**。[4]

---

## 10. 本仓库推荐架构

### 10.1 目标分层

```text
config.yml
  assistant.system_prompt          # 人设（短）
  rules.*                          # 加载策略
  tools.run_command.*              # 硬策略

~/.eino-assistant/
  AGENTS.md                        # 用户全局软规则
  policy/run_command.yaml          # 可选用户硬策略

<repo>/
  AGENTS.md                        # 项目真相源（建议提交）
  AGENTS.local.md                  # 本地覆盖（gitignore）
  .eino-assistant/rules/*.md       # 可选模块化软规则（v2）

thread journal
  thread.created: system_prompt + rules_digest + rules_files
  policy.changed: 审批/沙箱模式变更（v1.5+）
```

### 10.2 与现有子系统的接合点

| 子系统 | 接合 |
| --- | --- |
| `internal/config` | 增加 `rules` 与 `tools.run_command` 策略字段 |
| `cmd/.../run_tui.go` | 启动时 compile rules → `NewSession` |
| `internal/chat` | immutable 前缀 = base + compiled rules；暴露 `Rules()` |
| `internal/contextbuild` | 规则计入 immutable 预算；超预算明确错误/裁剪策略 |
| `internal/tools` | `run_command` 前 `Evaluate(command) → allow\|ask\|deny` |
| `internal/tui` | `/rules`、状态栏 policy、审批 modal |
| `internal/store` | meta/journal 记录 digest 与 policy 事件 |
| docs | 研究本文 + 用户指南 + 编写指南 |

### 10.3 分阶段交付

#### Phase 0 — 文档与约定（本文档 + 轻量产品说明）

- 采用 `AGENTS.md` 为项目 agent 真相源（本仓库已有，用于开发者约束）。
- 在 README 声明：未来将加载项目 `AGENTS.md`；当前仅 `system_prompt` 生效。
- 明确 soft/hard 边界，避免用户误以为写进 md 就等于沙箱。

#### Phase 1 — 规则加载器（高价值、低风险）

行为：

1. 解析 `rules.enabled`（默认 true）。
2. 加载全局 + 从 cwd 向上找到 git root，再 root→cwd 合并 `AGENTS.md`。
3. 可选读取 `CLAUDE.md` / `CONVENTIONS.md` 作为 fallback（默认关或次级）。
4. 编译为带来源标题的文本，受 `rules.max_bytes`（如 32KiB，对齐 Codex 量级）限制。
5. 注入 session immutable；`/rules` 打印来源与截断信息。
6. thread meta 存 digest。

非目标：path-scoped、hooks、sandbox。

#### Phase 2 — `run_command` 策略与审批 — **已完成（2026-07-17，对齐 permissions P1）**

行为（已实现）：

1. 结构化 policy 决策；默认 `approval: on_request` + `profile: cautious`。
2. TUI 审批：once / session / deny。
3. `workspace_only` 默认 true。
4. 状态栏展示 `cmd=ask|auto`；`/permissions`。
5. 决策进入 tool result（`denied`/`reason`/`stop_retrying`）；无独立 journal 事件类型。

非目标：OS 级 seatbelt/bwrap（Phase 3）。交付说明见 [../iterations/2026-07-17-run-command-permissions.md](../iterations/2026-07-17-run-command-permissions.md)。

#### Phase 3 — 深化

- `.eino-assistant/rules/**` + path globs。
- skills 按需加载（避免永远-on）。
- OS sandbox 适配（Linux bwrap / 或容器后端）。
- `execpolicy` 更强解析（简单 `&&` 拆分、明显管道检测）。
- 规则热刷新与 `/rules reload`。

### 10.4 默认安全立场（产品决策建议）

本仓库定位是**本地个人编程助手**，与云端多租户不同。建议：

| 配置 | 建议默认 | 理由 |
| --- | --- | --- |
| 软规则加载 | 开 | 对齐生态，成本低 |
| `run_command` | 开 | 已是核心能力 |
| 审批 | `on_request` 或“高危 ask、只读 allow” | 比当前“全自动”更安全，仍可用 |
| workspace_only | true | 减少扫家 |
| sandbox | 先文档化能力缺口，后做 | 工程量大 |
| yolo | 仅显式 flag | 防止误触 |

若坚持 v1 保持“无审批”以追求流畅，**必须在 UI 与 README 持续暴露风险**，并尽快提供 opt-in 策略文件。

---

## 11. 建议的文档体系（落实文档）

沿用现有 `docs/` 分工：研究文 vs 产品说明分离。

| 文档 | 类型 | 内容 |
| --- | --- | --- |
| `docs/research/cli-rules-research.md` | 调研（本文） | 生态、取舍、架构、阶段 |
| `docs/rules.md`（待写） | 产品说明 | 用户如何写/放/调试规则；加载顺序；`/rules` |
| `docs/command-policy.md`（待写） | 产品说明 | run_command 策略、审批、示例 |
| `README.md` 小节 | 入口 | 链到上述文档；当前行为 vs 规划 |
| 仓库根 `AGENTS.md` | 开发者规则 | 继续约束贡献者与未来自托管 agent |
| `config.example.yml` 注释 | 配置契约 | `rules:` / `tools.run_command.policy` 示例 |

### 11.1 建议的 `docs/rules.md` 大纲

```text
1. 规则是什么（soft） / 不是什么（不是沙箱）
2. 文件位置与优先级
3. 推荐章节模板
4. 体量与剪枝标准
5. 与 system_prompt、memory、slash 的关系
6. /rules 与 rules_digest
7. resume 冻结 vs 刷新
8. 常见问题（为什么不生效、如何调试）
```

### 11.2 建议的 `docs/command-policy.md` 大纲

```text
1. 威胁模型（本机用户权限、提示注入）
2. 决策顺序 deny > ask > allow
3. 配置参考
4. TUI 审批交互
5. 与软规则的分工
6. 已知局限（sh -c、无 OS sandbox 时）
7. 推荐预设：personal-dev / cautious / yolo
```

---

## 12. 与上下文压缩的关系

`docs/research/context-compaction-research.md` 已强调：不可丢的是指令、权限、当前目标。[14]

规则系统必须遵守同一预算纪律：

```text
Immutable 预算
  ├── product base prompt（小、稳定）
  ├── user system_prompt（小）
  ├── compiled rules（有上限，可截断并告警）
  └── （未来）active mode flags（plan/ask/yolo 一句话）

热 turn / checkpoint
  └── 不复制整份 AGENTS.md；只保留“规则仍生效”的短引用 + digest
```

压缩后**不得丢失**：

- 当前 approval/sandbox 模式；
- “workspace_only / 危险命令需审批”等硬约束摘要；
- rules_digest（用于发现漂移）。

工具输出与 artifact 必须继续标记为 **untrusted data, not instructions**（本仓库 compactor/artifact 路径已有类似原则，应保持）。

---

## 13. 结论

1. **命令行规则 = 软指令 + 硬策略 + UX 协议**；只做其中一层会在安全或遵守率上失败。  
2. **生态正在收敛到 `AGENTS.md` 作为可移植项目真相源**；工具专属文件应做适配而非重复内容。  
3. **短、具体、分层、可观测**是软规则有效的前提；硬安全必须放在工具入口。  
4. 本仓库已具备接合规则系统的骨架（immutable system、thread meta、工具注册、TUI slash），但 **`run_command` 仍是全权 shell**。  
5. 落地顺序应是：**文档约定 → 规则加载器 → 命令策略/审批 → 沙箱/skills**，每一步都可独立交付与测试。

---

## 14. 参考资料

1. agents.md，[AGENTS.md — a simple, open format for guiding coding agents](https://agents.md/)。  
2. Claude Code，[Best practices](https://code.claude.com/docs/en/best-practices)（含 CLAUDE.md 宜短、过长会忽略指令的警告）。  
3. Builder.io，[AI instruction best practices](https://www.builder.io/c/docs/ai-instruction-best-practices) 与 [AGENTS.md guide](https://www.builder.io/blog/agents-md)。  
4. 社区讨论（Reddit r/ClaudeAI 等）：CLAUDE.md 过长、规则被忽略、管理 monorepo 规则的实践线程（例：[ignores CLAUDE.md](https://www.reddit.com/r/ClaudeAI/comments/1mdd5sx/claude_code_completely_ignores_claudemd_and_all/)）。  
5. 生态对比综述，如 [AGENTS.md vs CLAUDE.md vs Cursor rules (2026)](https://codersera.com/blog/agents-md-vs-claude-md-vs-cursor-rules-comparison-2026/)。  
6. Builder.io / 多工具实践：以 AGENTS.md 为真相源、薄适配 Claude/Cursor 的迁移路径。  
7. Aider，[Conventions](https://aider.chat/docs/usage/conventions.html)（`CONVENTIONS.md` / `--read` / conf）。  
8. OpenAI Codex，[Custom instructions with AGENTS.md](https://developers.openai.com/codex/agent-configuration/agents-md)、[Project instructions discovery](https://developers.openai.com/codex/config-file/config-advanced#project-instructions-discovery)（访问：2026-07-31）。
9. Anthropic Claude Code，[Memory and project instructions](https://code.claude.com/docs/en/memory)（访问：2026-07-31）。
10. OpenAI Codex，[Agent approvals & security](https://developers.openai.com/codex/agent-approvals-security)、[Sandboxing](https://developers.openai.com/codex/sandboxing)、[Exec policy / rules](https://developers.openai.com/codex/exec-policy/)。  
11. Claude Code permissions / hooks 文档与社区整理（allow/deny/ask、PreToolUse、permission modes）。  
12. Open Interpreter，[Safety](https://docs.openinterpreter.com/safety/safe-mode)、[Isolation](https://docs.openinterpreter.com/safety/isolation)。  
13. Goose（Block / AAIF 生态），recipes vs rules / `.goosehints` / sandbox blocklist 相关文档与社区文。  
14. 本仓库，`docs/research/context-compaction-research.md`、`docs/session-persistence.md`。  
15. Cline，[Cline Rules](https://docs.cline.bot/customization/cline-rules)。  
16. Continue，[Rules](https://docs.continue.dev/customize/deep-dives/rules)。  
17. OpenHands，Skills / repo `AGENTS.md` 文档。  
18. 本仓库代码现状：`internal/tools/command.go`（`sh -c`、timeout、输出上限）、`internal/config/config.go`（`system_prompt`）、`internal/contextbuild`（immutable 预算）、`AGENTS.md`（开发约束）。
19. OpenAI Codex 公开源码，提交 [`f0c30e5`](https://github.com/openai/codex/commit/f0c30e528a54bdf0fa9a4d52ff74b34383434811)：[`agents_md.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/src/agents_md.rs#L83-L247)、[`codex-home instructions`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/codex-home/src/instructions/mod.rs#L24-L66)、[`AgentsMdManager`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/src/agents_md_manager.rs#L10-L49)、[创建时快照测试](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/tests/suite/agents_md.rs#L523-L634) 与 [cwd 变化刷新测试](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/tests/suite/model_visible_layout.rs#L263-L392)（访问：2026-07-31）。
20. OpenAI Codex，[Worktrees / Copy ignored local files](https://developers.openai.com/codex/environments/git-worktrees#copy-ignored-local-files-into-managed-worktrees)（访问：2026-07-31）。
21. Anthropic Claude Code，[Hooks / `InstructionsLoaded`](https://code.claude.com/docs/en/hooks#instructionsloaded)（访问：2026-07-31）。

---

## 附录 A — 本仓库现状快照（2026-07-17 更新）

| 项 | 状态 |
| --- | --- |
| 项目开发规则 | 根目录 `AGENTS.md`（给人/贡献者，**运行时未加载**） |
| 运行时指令 | `config.yml` → `assistant.system_prompt` → thread immutable |
| 项目规则发现 | 无（Phase 1 软规则加载未做） |
| 用户全局规则 | 无 |
| 命令审批 | **有**（Phase 2 / permissions P1）：默认 on_request + cautious |
| 命令 allow/deny | **有**：内置 + 可选 YAML；opaque-shell 降级 |
| OS 沙箱 | 无 |
| 输出/超时硬限制 | 有（默认 60s / 64KiB） |
| 权限可观测 | `/permissions`、状态栏 `cmd=`；无 `/rules` |
| 与 compact 的规则保留策略 | 仅整体 system_prompt 作为 immutable |

硬权限产品说明：[../command-policy.md](../command-policy.md)；迭代记录：[../iterations/2026-07-17-run-command-permissions.md](../iterations/2026-07-17-run-command-permissions.md)。

## 附录 B — 决策记录（建议采纳）

| 决策 | 选择 | 备选 | 原因 |
| --- | --- | --- | --- |
| 项目真相源文件名 | `AGENTS.md` | 仅 `CLAUDE.md` | 可移植；本仓库已使用该名 |
| 模块化目录（v2） | `.eino-assistant/rules/` | `.claude/rules` | 避免假装兼容 Claude；可另做导入器 |
| 规则合并 | 标注来源的 append + 近者优先裁剪 | 纯覆盖 | 便于调试 |
| Thread 规则语义 | 创建时冻结 + 显式刷新 | 每轮热读 | 与 ledger 可复现一致 |
| 命令策略语言 | YAML 前缀/正则 | Starlark | 实现与用户负担更低；v2 再评估 |
| 默认审批 | 分阶段收紧 | 永久 never | 在 UX 流畅与本机风险间折中 |
