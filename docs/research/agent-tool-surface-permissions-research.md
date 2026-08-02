# 细粒度权限配置：Claude Code 与 Codex 如何配、如何评

> 状态：业界调研笔记，不是实现方案。  
> 调研日期：2026-07-17。厂商文档与 schema 会变，采用前请复核原文。  
>  
> **主轴**：成熟 coding agent 的 **细粒度权限配置模型**——配置放哪、规则长什么样、求值顺序、会话记忆、与沙箱/审批的正交关系。  
> **次轴**：多工具拆分如何 **承载** 这些规则（path / argv / domain），而非替代配置机制。  
> **不在范围**：本仓库迁移计划；通用「什么是 agent」教程。

## 1. 摘要

1. **细粒度不是「多写几句 prompt」**，而是 **可序列化的策略层**：规则文件 + 配置层级 + 求值引擎 +（可选）OS 沙箱。宿主强制，模型无法覆盖。  
2. **Claude Code** 的细粒度主轴是 **`permissions.{allow,ask,deny}[]` 字符串规则** + **`defaultMode`** + **多层 settings 合并** + **交互「不再询问」落盘** + **PreToolUse hooks**。匹配单位是 **工具名 + specifier（命令前缀 / path / domain / 顶层参数）**。  
3. **Codex** 的细粒度主轴是 **正交两层**：  
   - **能力边界**：旧版 `sandbox_mode` / 新版 beta **permission profiles**（路径级 `read|write|deny` + 网络 domain）；  
   - **何时问人**：`approval_policy`；  
   - **出沙箱命令**：`.rules` 里 **Starlark `prefix_rule`**（argv 前缀 → `allow|prompt|forbidden`）。  
4. 两者都区分 **「规则写在哪」**（user / project / managed）、**「规则怎么赢」**（Claude：bucket 序 deny→ask→allow；Codex 规则：forbidden>prompt>allow；路径：更具体优先且 deny 压 write）、**「人点允许后记什么」**。  
5. 上一版只强调「拆工具」不够：拆工具是 **规则挂载点**；细粒度来自 **配置语法与求值**，不是工具列表本身。

## 2. 问题边界

| 问题 | 配置层要回答的是 |
| --- | --- |
| 这条 tool call 允许吗？ | 规则匹配 → allow / ask / deny（或 Codex 的 sandbox 能否做） |
| 配置谁说了算？ | scope 优先级 / managed 不可覆盖 |
| 细到什么程度？ | 工具名、path glob、argv 前缀、网络 host、参数值 |
| 用户点「允许」后？ | 一次 / 会话 / 永久写回哪份文件 |
| 复合 shell / 旁路？ | 拆分脚本再评 vs 整段 opaque；沙箱兜底 |

**细粒度权限配置 ≠ 工具多。**  
工具多只是让规则有 **更干净的匹配键**（`Write(path)` vs `Bash(echo > f)`）。没有配置模型，多工具也只是默认姿态不同。

---

## 3. Claude Code：配置模型（细粒度主文档）

一手来源：[Configure permissions](https://code.claude.com/docs/en/permissions)、[Settings](https://code.claude.com/docs/en/settings)（2026-07-17 抓取）。

### 3.1 配置放在哪（scope）

| Scope | 路径 | 共享 | 优先级（标量设置） |
| --- | --- | --- | --- |
| **Managed** | server / MDM plist·registry / `managed-settings.json`（系统目录） | 组织 | **最高**，用户/项目不可覆盖 |
| CLI 参数 | 启动 flag | 本次会话 | 次高 |
| **Local** | `.claude/settings.local.json`（仓库根，常 gitignore） | 个人本仓 | 盖过 project/user |
| **Project** | `.claude/settings.json` | 团队 git | 盖过 user |
| **User** | `~/.claude/settings.json` | 个人全局 | 最低 |

**例外：`permissions.allow` / `ask` / `deny` 数组跨 scope 合并（concatenate），不是「高层整表替换」**。  
`/permissions` UI 会列出每条规则来自哪个 settings 文件。

Managed 另有 **`allowManagedPermissionRulesOnly`**：为 true 时仅 managed 规则生效，用户/项目不能自写 allow/ask/deny。  
`permissions.disableBypassPermissionsMode` / `disableAutoMode` 可禁止危险模式（适合 managed）。

Project 的 allow 规则还受 **workspace trust** 约束：未信任仓库时，项目 allow 不直接生效；**local** 里个人「不再询问」落盘的 allow 不走同一 trust 步骤（官方区分）。

### 3.2 策略对象长什么样

```json
{
  "permissions": {
    "allow": [
      "Bash(npm run lint)",
      "Bash(npm run test *)",
      "Read(~/.zshrc)"
    ],
    "ask": [
      "Bash(git push *)"
    ],
    "deny": [
      "Bash(curl *)",
      "Read(./.env)",
      "Read(./.env.*)",
      "Read(./secrets/**)"
    ],
    "defaultMode": "default",
    "additionalDirectories": []
  }
}
```

字段角色：

| 字段 | 作用 |
| --- | --- |
| `allow[]` | 匹配则 **免批执行** |
| `ask[]` | 匹配则 **强制弹窗**（即使更窄 allow 也存在） |
| `deny[]` | 匹配则 **禁止**（可摘掉整个工具） |
| `defaultMode` | **未命中规则**时的会话默认姿态 |
| `additionalDirectories` | 扩大「工作区」只读/编辑范围 |

### 3.3 规则语法（细粒度的「键」）

统一形态：`Tool` 或 `Tool(specifier)`。

| 形态 | 例子 | 含义 |
| --- | --- | --- |
| 整工具 | `Bash`、`Read`、`WebFetch` | 匹配该工具全部调用 |
| `Bash(*)` | 同 bare `Bash` | deny 时 **从模型上下文移除** 工具 |
| 精确命令 | `Bash(npm run build)` | 精确匹配 |
| 命令通配 | `Bash(npm run *)`、`Bash(git * main)`、`Bash(* --version)` | `*` 可跨空格；`Bash(ls *)` 有词边界，`Bash(ls*)` 会匹配 `lsof` |
| 尾缀 `:*` | `Bash(ls:*)` | 与 `Bash(ls *)` 等价（仅尾缀识别） |
| 路径 | `Read(./.env)`、`Edit(docs/**)` | 文件类按 path/glob |
| 域名 | `WebFetch(domain:example.com)` | 网络 fetch |
| 参数（仅 deny/ask） | `Agent(model:opus)`、`Bash(run_in_background:true)` | 顶层 scalar 参数；`allow` **不用** 此语法 |
| 工具名 glob（deny/ask） | `mcp__*`、`*` | bare 名 deny → 从上下文移除；allow 的工具名 glob 有严格限制（如须 `mcp__server__*`） |

**故意不能写的**（防绕过）：  
`Bash(command:rm *)`、用 `param:` 去匹配已有 canonical 字段（`command` / `file_path` / `path` / `url`…）会被忽略并启动警告——因为复合命令可绕 `command:` 字面匹配。应写 `Bash(rm *)`。

### 3.4 求值顺序（关键）

官方明确：

```text
对一次 tool call：
  1. 是否命中任一 deny？ → deny（结束）
  2. 是否命中任一 ask？  → ask（结束）
  3. 是否命中任一 allow？ → allow（结束）
  4. 否则走 defaultMode / 工具类型默认
```

- **特异性不改变 bucket 顺序**：宽 deny `Bash(aws *)` 会挡住窄 allow `Bash(aws s3 ls)`。  
- **ask 压 allow**：同 call 既 match ask 又 match allow → **仍弹窗**。  
- **deny 两种语义**：  
  - bare `Bash`：工具从上下文消失；  
  - scoped `Bash(rm *)`：工具仍可见，调用时拦截。

规则由 **Claude Code 宿主** 强制；`CLAUDE.md` / 用户 prompt **不能** 改写允许集。

### 3.5 defaultMode（未命中规则时）

| Mode | 配置行为摘要 |
| --- | --- |
| `default`（Manual） | 工具类型默认：读多免批；Bash/写要批（内置只读 bash 集合除外） |
| `acceptEdits` | 自动接受工作区（+ additionalDirectories）内文件编辑及常见 fs 命令（mkdir/touch/mv/cp 等） |
| `plan` | 探索：读 + 只读 shell，不改源码 |
| `auto` | 自动批 + 后台安全检查 |
| `dontAsk` | 未预批则 **自动 deny**（白名单外全拒） |
| `bypassPermissions` | 基本跳过提示；**显式 ask 规则仍提示**；危险 `rm -rf /|~` 等熔断仍提示 |

可启动 flag / 会话切换；managed 可禁用 bypass/auto。

### 3.6 交互审批如何「变细」并持久化

| 工具类型 | 首次默认 | 「Yes, don’t ask again」 |
| --- | --- | --- |
| Read-only | 工作区内通常不批 | N/A |
| Bash | 要批（只读内置命令除外） | **永久**写入仓库根 `.claude/settings.local.json` 的 allow 规则（按 repo 生效，含 worktree 解析到主 checkout） |
| File modification | 要批 | **仅本 session**，不写永久文件 |

- 审批弹层可对 Bash 用 **Ctrl+E** 拉风险说明（Low/Med/High），不执行命令。  
- `/permissions`：查看/管理全部规则及来源文件。  
- 配置热重载：改 `permissions` 一般无需重启；`/doctor` 查 managed 无效项剥离。

### 3.7 Hooks：规则表达不了时的可编程细粒度

**PreToolUse hook** 可在静态 allow/ask/deny 之外返回 allow/deny/ask 或改写输入——官方把 hooks 标成权限扩展点。  
这是「配置语言不够时」的出口，不是替代 settings。

### 3.8 复合 Bash（细粒度能否拆开评）

文档对 compound commands 有专门规则（规则须覆盖子命令才能 allow 等；抓取片段表明：静态 Bash 规则在管道/复合场景有明确语义，不能只 match 前半段就放行整链）。  
与 Codex 的 tree-sitter 拆分不同产品，但目标同类：**防止 `safe && dangerous` 只 allow 了 safe 前缀**。

### 3.9 Claude 细粒度配置「清单」

若只记一张表：

| 旋钮 | 控制什么 |
| --- | --- |
| `deny/ask/allow` 规则串 | 某类 tool call 的三态 |
| specifier 语法 | 命令前缀 / path / domain / 参数 |
| 多层 settings 合并 | 组织底线 + 团队共享 + 个人例外 |
| `defaultMode` | 无规则命中时的默认姿态 |
| 「不再询问」落盘策略 | Bash 永久 vs Edit 会话 |
| hooks | 任意逻辑 |
| managed-only 开关 | 企业锁死 |

---

## 4. Codex：配置模型（细粒度主文档）

一手来源：[Agent approvals & security](https://developers.openai.com/codex/agent-approvals-security)、[Permissions (profiles)](https://developers.openai.com/codex/permissions)、[Rules](https://developers.openai.com/codex/rules)、[Config reference](https://developers.openai.com/codex/config-reference)（2026-07-17 抓取）。

### 4.1 正交两层（先分清）

```text
┌─────────────────────────────────────────────┐
│ approval_policy                             │
│   何时停下来问人（untrusted / on-request /  │
│   never / granular 对象）                   │
└──────────────────┬──────────────────────────┘
                   │ 与
┌──────────────────▼──────────────────────────┐
│ 能力边界（二选一体系，勿混用）                │
│  A) 旧：sandbox_mode + sandbox_workspace_*  │
│  B) 新 beta：default_permissions + profiles │
└──────────────────┬──────────────────────────┘
                   │ 另加
┌──────────────────▼──────────────────────────┐
│ Rules（.rules / prefix_rule）               │
│   出沙箱或升级时的 argv 前缀 allow/prompt/   │
│   forbidden                                 │
└─────────────────────────────────────────────┘
```

- **Sandbox / profile**：命令 **物理** 能否写某路径、能否上网。  
- **Approval**：**要不要问**。  
- **Rules**：尤其控制 **离开沙箱 / 特殊前缀** 时的决策。  

官方：`Auto` 示例 ≈ `--sandbox workspace-write --ask-for-approval on-request`：工作区内读写跑命令可自动，**出工作区写或要网络** 再问。

### 4.2 旧体系：`sandbox_mode`（仍广泛使用）

| `sandbox_mode` | 能力 |
| --- | --- |
| `read-only` | 读；不写 / 限制执行面 |
| `workspace-write` | 工作区可写；默认 **网络关** |
| `danger-full-access` | 近乎无沙箱（隔离环境才用） |

`config.toml` 示例：

```toml
approval_policy = "on-request"   # untrusted | on-request | never | { granular = { ... } }
sandbox_mode    = "workspace-write"

[sandbox_workspace_write]
network_access = true
# writable_roots = [ "..." ]   # 额外可写根（文档/社区均有）
```

**可写根内仍有受保护路径（只读）**：`<root>/.git`（含 gitdir 解析）、`.agents`、`.codex` 等递归只读。

CLI：`--sandbox`、`--ask-for-approval` / `-a`；`--yolo` 类组合跳过审批+沙箱（高危）。

`approval_policy` 还可 **granular**：分别控制 sandbox 升级、rules、MCP elicitation、skill 等是否交互。

`approvals_reviewer = "user" | "auto_review"`：可把「该问人」的请求先交给 reviewer agent（auto-review 文档）。

### 4.3 新体系（beta）：Permission profiles

官方强调：**profile 与旧 `sandbox_mode` 不组合**；任一处出现 `sandbox_mode` / `--sandbox` 则走旧体系。Managed `allowed_permission_profiles` 可强制走 profile。

**内置 profile**：

| 名 | 含义 |
| --- | --- |
| `:read-only` | 本地命令只读 |
| `:workspace` | 活跃 workspace roots + 系统 temp 可写 |
| `:danger-full-access` | 去掉本地沙箱限制 |

用户自定义 + `default_permissions = "project-edit"`：

```toml
default_permissions = "project-edit"

[permissions.project-edit]
description = "Project editing with OpenAI API access."
extends = ":workspace"

[permissions.project-edit.workspace_roots]
"~/code/app" = true
"~/code/shared-lib" = true

[permissions.project-edit.filesystem]
":minimal" = "read"

[permissions.project-edit.filesystem.":workspace_roots"]
"." = "write"
".devcontainer" = "read"
"**/*.env" = "deny"

[permissions.project-edit.network]
enabled = true

[permissions.project-edit.network.domains]
"api.openai.com" = "allow"
"*.github.com" = "allow"
"tracking.example.com" = "deny"
```

**Filesystem 细粒度**：

| 访问 | 含义 |
| --- | --- |
| `read` | 读/列目录；不可创建改删 |
| `write` | 读写含创建改删 |
| `deny` | 读写皆拒；从更宽 write 中挖洞 |

优先级：

- **更具体 path 覆盖更宽**；  
- 同 path：`deny` > `write` > `read`；  
- 可在宽 deny 下用更窄 path 重新 `write`（例：`~/Documents` deny，`~/Documents/codex` write）。

**Network 细粒度**：

- `enabled` 改沙箱网络策略；domain **allowlist-first**；`deny` 压 `allow`；  
- `*.example.com` / `**.example.com` / `*`（仅 allow）；  
- 默认挡 loopback/私网；`unix_sockets` 白名单等。

`extends = ":workspace"` 继承内置基线（如 workspace 下 `.codex` 只读除非覆盖）；**禁止** extends `:danger-full-access`。

配置层：组织 `/etc/codex` 与用户 `~/.codex` 可 **同名 profile 合并** workspace_roots 等。

### 4.4 Rules：出沙箱命令的 argv 级细粒度

路径：`~/.codex/rules/*.rules`、team config、**可信** 项目 `<repo>/.codex/rules/`。  
语言：**Starlark**（安全子集）。TUI allow list 可写入 `~/.codex/rules/default.rules`。

```python
prefix_rule(
  pattern = ["gh", "pr", "view"],
  decision = "prompt",   # allow | prompt | forbidden
  justification = "Viewing PRs is allowed with approval",
  match = ["gh pr view 7888", ...],
  not_match = ["gh pr --repo openai/codex view 7888"],  # 非前缀
)
```

| 字段 | 作用 |
| --- | --- |
| `pattern` | argv **前缀**；元素可为字面量或字面量并集 |
| `decision` | 多规则命中取 **最严**：`forbidden` > `prompt` > `allow` |
| `justification` | 提示/拒绝文案；forbidden 建议给替代方案 |
| `match` / `not_match` | 加载时校验用例（防写错规则） |

匹配语义：命令当作 **argv 列表**（类 `execvp`），**不是** 模糊字符串。

**Shell 包装拆分（关键细粒度）**：

- 对 `bash -lc` / `bash -c` / zsh/sh 同类：  
  - 若脚本是 **安全线性链**（纯词 + `&&` `||` `;` `|`，无重定向/替换/通配/赋值/控制流）→ **tree-sitter 拆成多条 argv**，**每条分别评规则，最严赢**。  
    → `git add . && rm -rf /` 即使 allow 了 `git add` 也不会整段 auto-allow。  
  - 若含 `>`、`$()`、通配、`if` 等 → **不拆**，整段当作 `["bash","-lc","<full script>"]` 一条评（保守）。

调试：`codex execpolicy check --rules ... -- <cmd...>` 输出最严 decision 与命中规则。

Smart approvals 默认开启时，升级场景 Codex 可 **提议** 一条 `prefix_rule` 供用户确认后写入。

### 4.5 Codex 细粒度配置「清单」

| 旋钮 | 控制什么 |
| --- | --- |
| `approval_policy` | 何时问人（含 granular 分类） |
| `sandbox_mode` **或** `default_permissions`+profiles | 路径/网 物理边界 |
| profile `filesystem` path→read/write/deny | **路径级** 读写挖洞 |
| profile `network.domains` | **域名级** 出口 |
| `prefix_rule` | **argv 前缀** 出沙箱 allow/prompt/forbidden |
| config layers + profiles | 用户/项目/企业分层 |
| `approvals_reviewer` | 人审 vs auto_review agent |
| `execpolicy check` | 规则单测 |

---

## 5. 对照：两边「细」在何处

| 维度 | Claude Code | Codex |
| --- | --- | --- |
| 主配置文件 | `settings.json`（多 scope） | `config.toml` + `rules/*.rules` +（beta）`[permissions.*]` |
| 三态决策 | allow / ask / deny **显式数组** | approval 问人 + rules 的 allow/prompt/forbidden；沙箱是另一轴 |
| 路径细粒度 | `Read(path)` / `Edit(path)` 规则串 | profile filesystem **read/write/deny**（OS 执行边界） |
| 命令细粒度 | `Bash(prefix *)` 通配字符串 | `prefix_rule` **argv 前缀** + 安全 shell 拆分 |
| 网络细粒度 | `WebFetch(domain:…)`、MCP 等 | profile domains / network_proxy |
| 求值 | deny→ask→allow bucket；**无**「窄 allow 打穿宽 deny」 | 路径更具体优先；规则最严 wins；deny>write>read |
| 持久化「记住允许」 | Bash→`settings.local.json`；Edit→session | allow list→`default.rules`；profile 在 toml |
| 企业锁 | managed-settings、allowManagedPermissionRulesOnly | requirements.toml、allowed_permission_profiles |
| 可编程扩展 | PreToolUse hooks | auto_review / managed policy 等 |
| 与「多工具」关系 | 规则 **按工具名** 挂载，path 挂在 Read/Edit | 能力在 **沙箱/profile**；命令策略在 **rules**；patch 与 shell 分轨 |

**共同模式**：

1. **配置即策略**（可 git / 可 MDM）。  
2. **匹配键结构化**（tool+specifier 或 argv 或 path ACL）。  
3. **明确优先级**（bucket 或 severity 或 path 特异性）。  
4. **人批结果写回策略**（不同工具不同 TTL）。  
5. **第二道物理边界**（Claude 侧 sandbox 文档另述；Codex 把 OS sandbox/profile 做成一等配置）。

---

## 6. 合理默认与反模式

### 6.1 合理

| 做法 | 为什么细 |
| --- | --- |
| 团队 project settings：deny `.env`，allow 常用 `Bash(npm test *)` | 路径+命令双键，可 review |
| Codex profile：workspace write + `**/*.env` deny + 域名 allowlist | 写范围与秘密挖洞 |
| Claude local「不再询问」只升 Bash 前缀 | 永久粒度停在命令前缀，不整开 Bash |
| Codex `prefix_rule` + `execpolicy check` | 规则可单测 |
| managed 禁止 bypass | 企业底线不可被用户 settings 拆掉 |

### 6.2 反模式

| 反模式 | 问题 |
| --- | --- |
| 只有一个 `command` 字符串 + 少量正则 | 没有 path ACL / argv 前缀语言 / scope 合并 |
| 用提示词代替 deny 规则 | 宿主不强制 |
| 混用 Codex `sandbox_mode` 与 `default_permissions` | 官方：旧设置抢占 profile |
| 以为窄 allow 能例外于宽 deny（Claude） | 官方：不能 |
| 永久 allow 整个 `Bash` | 细粒度退化为全开 |
| 忽略复合命令拆分语义 | `safe && evil` 漏评 |

---

## 7. 对「创建文件」权限在配置层如何表达

| 产品 | 细粒度表达方式 |
| --- | --- |
| Claude | 主路径：`Write`/`Edit` 工具 + path 规则 / `acceptEdits` 模式；Bash 创建走 `Bash(touch *)` 等 **另一条规则**。用户对 Edit 的 session 记忆与对 Bash 的 permanent 记忆 **分离**。 |
| Codex | 主路径：patch 落在 **workspace-write / profile write roots** 内则可能免「出沙箱」；`.env` 可 `deny`；若用 shell 写文件，仍受 **同一 filesystem ACL**，不是换 argv 就换权限宇宙。出沙箱再叠 `prefix_rule`。 |

配置层答案：**按 path（与工具类型）授权写**，而不是按「touch 字符串」授权。

---

## 8. 开放问题

- Claude compound Bash 与 hooks 在各版本的完整算法需对照最新 permissions 全文与 changelog。  
- Codex permission profiles 仍标 **Beta**；与 Desktop/CLI 一致性 issue 仍存在。  
- 「用户 deny 一次」是否写入 **session deny 规则**（对称于 allow 落盘）：公开文档详写 **allow 记忆** 多于 **deny 记忆**，需产品实测。  
- Starlark rules 与 Smart approval 提议前缀的 UX 边界随版本变。

---

## References

1. Claude Code — Configure permissions：https://code.claude.com/docs/en/permissions  
2. Claude Code — Settings（scopes、settings.json 示例、热重载、managed）：https://code.claude.com/docs/en/settings  
3. OpenAI Codex — Agent approvals & security（sandbox × approval、protected paths、network、granular）：https://developers.openai.com/codex/agent-approvals-security  
4. OpenAI Codex — Permissions（beta profiles、filesystem read/write/deny、network domains、extends）：https://developers.openai.com/codex/permissions  
5. OpenAI Codex — Rules（`prefix_rule`、argv、shell 拆分、`execpolicy check`）：https://developers.openai.com/codex/rules  
6. OpenAI Codex — Configuration reference（`approval_policy`、`sandbox_mode` 等键）：https://developers.openai.com/codex/config-reference  
7. 互补笔记（shell 审批 UX / 六层栈）：[cli-command-permissions-research.md](./cli-command-permissions-research.md)
