# 迭代：workspace 根 AGENTS 本地覆盖

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-07-31 |
| 分支 | `codex/iteration-doc-finalize` |
| 状态 | **已最终交付；完整 RulesBlock 预算与正文保留单调性均已锁定** |
| 实现范围 | `f4ff706` 至 `c31a19a` |
| 调研依据 | [cli-rules-research.md](../research/cli-rules-research.md) §4.1.1 |
| 产品说明 | [memory.md](../memory.md) §3、§7 |

## 1. 用户目标

在不扩大既有“仅 workspace 根项目指令”模型的前提下，支持本机私有的根
`AGENTS.override.md`：当它有效时替代共享的根 `AGENTS.md`，并保持项目指令的
创建时冻结、软规则属性和有界注入。合同必须可测试、可解释，且不能把规则加载
拆到新的 `internal/rules` 包。

用户需要由此获得两种明确用途：

1. `AGENTS.md` 保存可提交、可共享的团队约定。
2. `AGENTS.override.md` 保存当前 workspace 的本地替代约定；是否 gitignore 由项目决定。

## 2. 调研来源与证据质量

本次决策面只涉及指令文件候选选择、层级、符号链接、加载时机和预算，不把
memory、权限或 sandbox 行为混入“本地覆盖”合同。

| 来源 | 用途 | 证据质量与边界 |
| --- | --- | --- |
| OpenAI Codex 官方文档：[Custom instructions with AGENTS.md](https://developers.openai.com/codex/agent-configuration/agents-md)、[Project instructions discovery](https://developers.openai.com/codex/config-file/config-advanced#project-instructions-discovery) | 候选顺序、每目录至多一个、root 到 cwd、启动时加载、预算 | **高，动态产品合同**；访问于 2026-07-31，页面会随产品更新 |
| OpenAI Codex 固定源码 [`f0c30e5`](https://github.com/openai/codex/commit/f0c30e528a54bdf0fa9a4d52ff74b34383434811)，尤其 `agents_md.rs`、codex-home instructions、`AgentsMdManager` 及对应测试 | 符号链接、空文件边界、创建时缓存及环境选择变化时刷新 | **高，可复现实现证据**；只能证明该提交，不能替代未来用户合同 |
| OpenAI Codex 官方 [worktree 文档](https://developers.openai.com/codex/environments/git-worktrees#copy-ignored-local-files-into-managed-worktrees) | managed worktree 对 ignored override 的复制行为 | **高，动态产品合同**；本仓库未实现该能力 |
| Anthropic Claude Code 官方 [Memory and project instructions](https://code.claude.com/docs/en/memory) 与 [`InstructionsLoaded`](https://code.claude.com/docs/en/hooks#instructionsloaded) | `CLAUDE.local.md` 追加、祖先链、子目录 lazy load、compact 重载、symlink/import | **高，动态产品合同**；访问于 2026-07-31，没有固定源码提交支撑 |
| 本仓库 [cli-rules-research.md](../research/cli-rules-research.md) | 对上述来源的对照、证据空白和落地语境 | **中，二次归纳**；不是运行时真相 |
| 本仓库实现、测试和 durable thread 测试 | 本迭代实际合同 | **最高，本地行为真相**；文档与外部产品冲突时以代码和测试为准 |

可直接确认的事实是：Codex 在同一目录按 override、base、fallback 候选顺序至多
选择一个；Claude Code 则将同层 `CLAUDE.local.md` 放在 `CLAUDE.md` 后共同加载。
由此采用“本地文件替代共享文件”是 Codex 对齐，不是跨产品统一标准。

证据空白也保留在合同中：Codex 固定提交的项目级发现会先选第一个存在的候选，
之后才忽略空内容，因此没有锁定“空 override 回退 base”；其 codex-home 加载器却会
继续尝试 base。本仓库明确选择并测试回退，不声称这是 Codex 的稳定行为。

## 3. 范围与非目标

### 本次范围

- 只在已解析的 workspace 根检查 `AGENTS.override.md` 和 `AGENTS.md`。
- 定义有效候选、回退、符号链接、BOM/空白和 I/O 错误行为。
- 将被选文件名用于 prompt 标题，并让受预算截断的 loader 产物显式以省略号结尾。
- 保持 `[rules]` 默认开启、默认约 8,000 token 估算预算。
- 保持新 session 创建时加载、durable system prompt 冻结的生命周期。
- 同步 README、配置示例、架构、memory 产品说明和研究依据。

### 非目标

- 用户全局、Git root 到 cwd 祖先链、子目录或 path-scoped 指令。
- 配置式 fallback 文件名、`CLAUDE.md` 原生加载、文件 import。
- `/rules`、来源列表、digest、截断告警 UI 或显式 reload。
- Codex-managed worktree 的 ignored override 自动复制。
- 热重载、turn cwd 变化刷新、`/compact` 后重载。
- 把自然语言指令升级为权限、审批或 sandbox；AGENTS 始终只是软指导。
- 限制符号链接目标必须位于 workspace 内。

## 4. 根候选选择合同

选择算法可概括为：

```text
workspace root
  -> AGENTS.override.md
  -> AGENTS.md
  -> 第一个有效候选
  -> 至多注入一个；不拼接
```

| 输入情形 | 合同行为 |
| --- | --- |
| override 与 base 都是有效普通文件 | 只选择 override；base 不进入 prompt |
| override 不存在或读取前消失 | 继续尝试 base |
| override 是目录、FIFO 等非普通文件 | 跳过并尝试 base |
| override 是指向普通文件的 symlink | 跟随并选择；`Path` 仍记录 workspace 中的 override 路径 |
| override 是 dangling symlink 或目标消失 | 按不存在处理并尝试 base |
| symlink 指向 workspace 外普通文件 | 允许；目标内容会发送给模型，使用者必须把目标视为可信配置 |
| override 为空，或去 BOM 后仅含空白 | 跳过并尝试 base |
| 两个候选都无效 | `Found=false`，不生成 project-instructions block，不报错 |
| `stat`/`read` 遇到非 `not exist` 错误 | 返回错误；启动或新 thread 创建失败，不静默降级 |

BOM/空白的精确语义：读取后只移除文本开头的一个 UTF-8 BOM（`U+FEFF`），紧接着
对正文执行 Unicode-aware `TrimSpace`。结果为空则候选无效并继续回退；结果非空则
规范化后的正文直接保存为 `ProjectInstructions.Text`，首尾原始空白不再保留。
BOM 不在文件开头时不会被特殊处理。

`workspaceRoot` 会先去除首尾空白并转为绝对路径；空 root 是错误。这里不做 Git root
发现，也不解析 workspace root 自身的真实路径。候选检查使用 `os.Stat`，所以会跟随
符号链接，并只接受解析后的普通文件。

## 5. 创建时生命周期

```text
进程初次新建 thread / TUI /new / TUI /clear
  -> compose persona + product policy
  -> 从磁盘重新选择根 AGENTS 候选
  -> 加入当时的 memory summary
  -> 写入 thread.created 的 durable system prompt
  -> 普通 turn、compact、resume 始终复用该快照
```

| 时机 | 是否重新读取 AGENTS | 结果 |
| --- | --- | --- |
| 进程启动且创建新 session | 是 | 选择结果进入新 thread durable system |
| `/new` | 是 | 创建独立 thread；重组规则和 memory |
| `/clear` | 是 | 创建独立 thread并保留旧 thread；重组规则和 memory |
| 普通 turn | 否 | 使用创建时 system prompt |
| 只编辑 override/base | 否 | 当前 thread 不变；下次 `/new` 或 `/clear` 生效 |
| `/memory` 修改 | 否 | memory 工具立即看到落盘结果，但 system prefix 不变 |
| `/compact` | 否 | 只压缩对话；不修改 durable system prompt |
| `/resume` | 否 | 恢复被 resume thread 创建时的 system prompt，不采用当前磁盘内容 |

该选择优先保证账本可复现和 provider prefix cache 稳定。若重组失败，`/new` 或
`/clear` 不替换当前 session；不存在“部分刷新”。

## 6. Token 预算合同

配置层合同：`rules.enabled` 缺省为 `true`；`rules.max_tokens=0` 使用默认值 8,000；
负数无效；显式值上限为 100,000。加载器直接收到非正值时也回退到 8,000。预算使用
`usage.EstimateText` 的本地估算，不等于 provider 返回的精确 tokenizer 计数。

`f3276ce` 将预算对象从正文 `Text` 修正为 loader 最终注入的完整格式化
`RulesBlock`。对 `LoadProjectInstructions` 返回的有效 bundle 和任意正 `maxTokens`，
最终不变量为：

```text
usage.EstimateText(FormatProjectInstructionsBlock(bundle)) <= maxTokens
bundle.Tokens == usage.EstimateText(FormatProjectInstructionsBlock(bundle))
```

标题、实际选中文件名、截断标记和保留正文都计入该预算。loader 会保存去 BOM 后经
`TrimSpace` 规范化的完整正文到 `ProjectInstructions.Text`；`Truncated` 表示完整无截断
格式超过预算，`Tokens` 表示最终格式化块的估算值。persona、工具 policy、memory 和
整个 system prompt 不属于 `[rules].max_tokens` 的预算对象。

`c31a19a` 进一步固定截断呈现和预算增长行为：

1. 完整格式化块的估算值恰好等于预算时原样保留，`Truncated=false`；少 1 token 时
   进入截断，最终块仍不超过预算。
2. loader 的截断块不显示 `_Note:`。省略号已经表达截断，再加入 note 会在其刚好可
   容纳的预算阈值牺牲规则正文，导致预算增加反而保留更少内容。
3. 当框架可容纳时，截断块是“标题及实际文件名 + 正文 rune 前缀 + `…`”；按 rune
   二分截断，不切断 UTF-8。预算增加不会减少已保留的规则正文。
4. 连带文件名的标题框架也容不下时，确定性退化为单个 `…`；`maxTokens=1` 时其估算
   值和 `Tokens` 都是 1。
5. 加载器直接收到非正预算时仍使用默认 8,000，因此极小预算合同从正值 1 开始。

`ProjectInstructions` 是 exported struct。为兼容已有调用方，手工构造、没有 loader
内部 `maxTokens` 的 bundle 仍走 legacy formatter：当 `Truncated=true` 时保留旧
`_Note:` 格式。该兼容路径没有原始预算，因而不承诺上述 loader 预算上限或 `Tokens`
一致性。该修正不改变候选顺序、生命周期和软规则属性。

## 7. 包边界

| 位置 | 本能力中的职责 | 明确不负责 |
| --- | --- | --- |
| `internal/agent/project_instructions.go` | 根候选选择、读取、截断、格式化 | memory 存储、thread ledger、硬权限 |
| `internal/agent/compose_layers.go` / `prompt.go` | 组装 persona、产品 policy、rules、memory layers | 自己读取 memory store |
| `internal/config` | `[rules]` 默认值与校验 | 运行时文件发现 |
| `cmd/eino-assistant/run_tui.go` | 在创建路径接线 workspace/config/memory 与 composer | 定义规则选择算法 |
| `internal/tui` | `/new`、`/clear` 时触发 composer；`/resume` 不触发 | 保存 AGENTS 内容或热刷新 |
| `internal/chat` + `internal/store` | 冻结并恢复 create-time durable system prompt | 重新发现项目指令 |
| `internal/memory` | 生成独立的语义记忆摘要 | AGENTS 候选选择 |

不新增 `internal/rules`。`[rules]` 是配置名称，加载器继续归属于 prompt 的消费方
`internal/agent`，与 [architecture.md](../architecture.md) 一致。

## 8. 测试与门禁

本次新增/强化的定向测试覆盖：

- override 优先且不拼接 base；
- 空文件和 BOM + whitespace override 回退 base；
- 有效正文去 BOM 后立即 `TrimSpace`，并在预算计算前保存为规范化 `Text`；
- 指向 workspace 外普通文件的 symlink 可用；
- 非普通 override 跳过；
- 完整格式化块恰好命中预算时不截断，少 1 token 时截断且仍不超限；
- 普通截断及 `maxTokens=1` 时确定性退化为单个 `…`；
- loader 截断块不显示 note，且预算增长不会减少已保留的正文；
- override 截断时标题使用实际选中文件名并以 `…` 标记截断；
- loader 产物的 `Tokens` 始终等于最终格式化块的本地估算值；
- 手工构造的 legacy `Truncated` bundle 仍保留旧 note 格式；
- 既有 TUI 测试锁定 `/resume`、`/compact` 不重组 create-time system prompt。

文档提交前执行完整门禁：

- [x] `go test ./...`
- [x] `go build ./...`
- [x] `go tool golangci-lint run ./...`
- [x] `git diff --check`

完整 RulesBlock 上限、精确边界、极小预算、正文规范化、正文保留单调性和 legacy
formatter 兼容测试已随 `f3276ce` 与 `c31a19a` 提交，并包含在实现审查门禁中。

## 9. 提交拆分

| 提交 | 作用 |
| --- | --- |
| `f4ff706` `docs: refresh local instruction override research` | 用 Codex/Claude 官方材料和 Codex 固定源码复核覆盖合同 |
| `9229505` `feat: support root agents override` | 实现根 override/base 候选选择、回退、symlink 和文件名可见性 |
| `81a361f` `docs: document root agents override` | 更新 README、配置示例、架构和 memory 产品说明 |
| `cf374a1` `fix: enforce project instruction token budget` | 修复极小预算下正文 `Text` 超限，并补截断文件名测试 |
| `070d516` `test: lock root instruction selection contract` | 锁定 BOM/whitespace、symlink、非普通文件合同 |
| `96a5201` `docs: clarify root instruction contract` | 澄清“首个有效候选”、软规则和 workspace 根边界 |
| `f3276ce` `fix: cap formatted project rules block` | 将标题、文件名、截断标记和正文统一纳入 loader 最终 RulesBlock 预算，并锁定边界与正文规范化 |
| `c31a19a` `fix: preserve rules as budget grows` | 移除 loader 截断 note，锁定预算增长时正文保留单调性，并保留 legacy formatter 兼容行为 |
| `cbcc297` `docs: record root agents override iteration` | 汇总目标、证据、合同、门禁、偏离和后续的初版记录 |
| 本提交 `docs: finalize root agents override iteration` | 同步最终实现与测试，定稿本轮交付记录 |

## 10. Codex / Claude 对齐与显式偏离

### 与 Codex 对齐

- 同目录候选以 `AGENTS.override.md` 优先于 `AGENTS.md`，至多选择一个。
- override 是 base 的替代品，而不是追加层。
- 普通文件 symlink 可作为候选。
- 项目指令是有界、模型可见的软指导，不承担硬权限。
- session 使用创建/加载阶段得到的指令快照，普通 turn 不反复读取磁盘。

### 相对 Codex 的显式偏离

| 偏离 | 理由 |
| --- | --- |
| 只检查 workspace 根，不做全局或 root→cwd 层级 | 保持既有 v1 范围，避免在本地覆盖迭代中引入层级冲突模型 |
| 空白 override 稳定回退 base | 对用户更可恢复；Codex 固定项目级源码未锁定该边界 |
| token 估算预算，而非 `project_doc_max_bytes` 字节预算 | 沿用本仓库既有 context/usage 预算体系 |
| 无 fallback filenames | 尚无对应配置需求 |
| turn cwd/环境变化不刷新 | durable thread system 冻结优先；当前账本没有 system revision 事件 |
| 不复制 ignored override 到 managed worktree | 本产品没有 Codex-managed worktree 生命周期 |

### 与 Claude Code 对齐

- 指令是 context，不是 enforced configuration；硬阻断留给权限/hook/sandbox。
- 支持通过 symlink 复用项目指令文件的实践。
- 新 conversation 建立时加载持久项目上下文。

### 相对 Claude Code 的显式偏离

| 偏离 | 理由 |
| --- | --- |
| `AGENTS.override.md` 替代 base；不追加 local | 采用 Codex 候选模型，避免同层冲突与重复 token |
| 原生文件名是 AGENTS，不是 CLAUDE | 选择跨工具项目真相源；Claude 可用 import/symlink 适配 |
| 无 managed/user/ancestor/subdirectory/rules 层 | v1 只承诺 workspace 根 |
| `/compact` 不重载指令 | 保持 durable system 不变和账本可复现；Claude 明确会在 compact 后重载 |
| workspace 外 symlink 不弹审批 | 当前 loader 把项目配置视为可信输入；风险已文档化，尚无 import approval UX |

## 11. 后续

1. 单独设计用户全局与 root→cwd 层级；先定义跨层顺序、总预算分配、空候选和冲突
   可观察性，再扩展加载器。
2. 增加 `/rules` 或等价只读视图，展示来源、估算 token、截断状态和 create-time
   snapshot；不要借 UI 暗示自然语言规则是硬策略。
3. 若引入规则刷新，先为 durable system revision、resume 可复现性和 prefix-cache
   失效定义账本事件；不能在普通 turn 中静默热改。
4. 评估 workspace 外 symlink/import 的信任提示，以及多 worktree 的本地 override
   传播策略；不要默认照搬 Codex-managed worktree 行为。
