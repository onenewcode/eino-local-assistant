# 用户级 instructions：跨产品实践研究

> 状态：research note, not an implementation plan.
>
> 研究日期：2026-08-04。产品文档和公开源码会持续变化；采用兼容合同前应按固定版本复核。
>
> 决策面：部署中的 coding agent 如何发现用户级 instructions，如何与项目级内容组合，如何分配上下文预算，以及 session 创建、恢复、分叉和 reload 时哪些内容保持不变。
>
> 范围：Codex CLI、Claude Code、Gemini CLI、OpenCode 和 Aider 的用户级 instruction 或 convention 行为；比较 home 位置、候选选择、优先级、预算、生命周期和可观测性。
>
> 不在范围：本仓库实现、权限强制、skills、auto memory 的内部算法、通用 agent framework/API，以及没有公开证据的内部 prompt 组装推断。

## 1. 结论

1. **“用户级 instructions”不是一个统一的产品层。** Codex 把 `$CODEX_HOME` 下的一个非空 `AGENTS.override.md` 或 `AGENTS.md` 作为 user instructions；Claude 同时提供 managed policy、`~/.claude/CLAUDE.md` 和 `~/.claude/rules/`；Gemini 使用 `~/.gemini/GEMINI.md`；OpenCode 使用 `~/.config/opencode/AGENTS.md` 并提供 Claude fallback；Aider 的公开合同是用户显式 `/read` 或 config `read`，而不是自动 home 文件发现。[Codex home provider][Codex agents source] [Claude memory] [Gemini GEMINI.md] [OpenCode rules] [Aider conventions]

2. **覆盖语义分成“同级替代”和“跨层合并”两类。** Codex 的 global override/base 是同级首个非空候选，项目内容随后追加；Claude 的 user、project 和 local 文件都按作用域合并，越具体的内容排在越后；OpenCode 在每个规则类别中使用 first-match，而 `instructions` 配置文件另行合并。文件名相似不代表可以把所有产品都实现为 nearest-wins 或 override-wins。[Codex home tests] [Claude memory] [OpenCode rules]

3. **用户预算通常不是项目规则预算的简单别名。** Codex 的项目 AGENTS loader 使用独立的 project-doc 字节预算，global provider 先产生 user entry，再与项目 entries 组合；Claude 对 CLAUDE.md 没有公开硬字节上限，只建议每个文件少于 200 行，而 200 行/25KB 的硬读限只适用于 `MEMORY.md`；Aider 把显式 convention 当作普通已读上下文；Gemini 和 OpenCode 的公开材料主要描述整体 context、compaction 或统计，而没有 global-instruction 专属预算。[Codex agents source] [Claude memory] [Aider conventions] [Aider token limits]

4. **“session snapshot”至少包含三个不同问题。** Codex 的公开源码测试明确显示：fresh thread 会加载 global 和 project，普通后续 turn 保留创建时的指令文本与 source list；fork 可以按新配置重新加载并注入替换片段；cold resume 需要处理旧 source 已删除的情况。Claude 文档描述 session 启动加载以及 `/compact` 后 project-root CLAUDE.md 重读，但没有公开一个等价的 thread journal snapshot 合同。Gemini 提供 `/memory reload`，OpenCode 和 Aider 的文档没有承诺编辑规则后 active prompt 自动重算。[Codex agents tests] [Claude memory] [Gemini GEMINI.md] [OpenCode rules] [Aider commands]

5. **可审计性依赖“来源集合 + 生效时点”，不只依赖拼接文本。** Claude 的 `/context` 和 `InstructionsLoaded` hook 可以显示实际加载文件及原因；Gemini 的 footer、`/memory show` 和 `/memory reload` 暴露当前 context；Codex 的公开源码保留 source provenance 并在测试中核对 source order，但未找到一个同等稳定的用户级 `/rules` 展示合同。OpenCode 的 `debug config`、logs 和 stats 能观察配置/运行状态，却不等于 effective instruction payload；Aider 的 `/tokens`、`/show-prompts` 和显式 `/read` 也主要观察普通 context。[Claude memory] [Claude hooks] [Gemini GEMINI.md] [Codex agents tests] [OpenCode CLI] [Aider commands]

## 2. 证据中的产品行为

### 2.1 Codex CLI：独立 user provider + 创建时快照

**文档/源码事实。** 当前公开 Codex 源码把 user instructions provider 放在 Codex home 层。默认 home 是 `~/.codex`，可由 `CODEX_HOME` 覆盖；provider 按 `AGENTS.override.md`、`AGENTS.md` 顺序检查，忽略非普通文件和空白内容，返回第一个非空文件及 source path。覆盖文件读取失败会记录 warning 并继续尝试 base；没有可用文件时返回空 user entry。[Codex config source] [Codex home source] [Codex home tests]

项目 discovery 由另一段 loader 负责：它从 project root 到 cwd 收集项目候选，并使用 `project_doc_max_bytes` 限制项目内容；`LoadedAgentsMd` 先保存 user instructions，再保存 project entries，最终把 user 内容放在 project 内容之前。源码注释和测试还显示，global user entry 不被当作 project entry 处理，因此不能把项目预算直接解释成整个指令树的预算。[Codex agents source]

**生命周期事实。** Codex 的测试在 fresh thread 中检查 global-before-project 的 source 顺序，并在修改 global/project 文件后验证普通第二个 turn 仍发送创建时的结构化指令前缀。另一个 fork 测试先修改 global 文件，再以新配置 fork，观察到 fork 使用新的 source 并只注入一次 replacement 片段。cold resume 测试覆盖旧 source 被删除时保留历史内容、再追加“previously provided instructions no longer apply”的处理。[Codex agents tests]

**边界。** 这些是固定公开源码和测试的观察，不等同于长期 CLI 文档承诺。源码说明了 provider、source provenance 和部分 resume/fork 行为，但没有把 global instruction 的用户可见预算、跨机器 home 映射或所有 reload 触发条件写成单独产品合同。

### 2.2 Claude Code：managed、user、project/local 的多级合并

**文档事实。** Claude Code 将 CLAUDE.md 按由宽到窄的作用域组织：组织管理的路径（macOS `/Library/Application Support/ClaudeCode/CLAUDE.md`、Linux/WSL `/etc/claude-code/CLAUDE.md`、Windows `Program Files` 路径）、`~/.claude/CLAUDE.md`、项目的 `./CLAUDE.md` 或 `./.claude/CLAUDE.md`，以及 gitignored 的 `./CLAUDE.local.md`。用户级规则还可以放在 `~/.claude/rules/`，并在项目规则之前加载；managed CLAUDE.md 不能被个人设置排除。[Claude memory]

Claude 从当前工作目录向上遍历，按文件系统根到 cwd 的顺序拼接祖先文件；同一目录的 `CLAUDE.local.md` 追加在 `CLAUDE.md` 后。子目录文件在 Claude 读取该目录中的文件时按需加载。文件内容被视作 context 而不是强制配置，冲突指令可能被模型任意选择；需要硬保证时官方建议使用 hooks 或 managed settings。[Claude memory]

**预算与生命周期事实。** 官方建议每个 CLAUDE.md 少于 200 行以改善遵守率，但明确说明 CLAUDE.md 会完整加载；200 行或 25KB 的“先到者”限制只适用于 auto-memory 的 `MEMORY.md`。每个 session 启动时读取 CLAUDE.md；`/context` 可以查看 Memory files；`InstructionsLoaded` hook 可以记录路径和加载原因。`/compact` 后 project-root CLAUDE.md 会从磁盘重读，嵌套文件等下次访问对应目录时再加载。文档没有公开 user/project instructions 是否以独立 thread snapshot 形式持久化，也没有承诺普通文件修改会自动改变当前未触发 reload 的 prompt。[Claude memory] [Claude hooks]

### 2.3 Gemini CLI：global context + 可见 reload

**文档事实。** Gemini CLI 的默认 global context 文件是 `~/.gemini/GEMINI.md`，之后加载配置 workspace 目录及其父目录的 context 文件；工具访问目录时还会进行 trusted-root 范围内的 just-in-time 发现。所有找到的内容会拼接并发送给模型；`context.fileName` 可以把默认名称改成一个或多个名称，例如 `AGENTS.md`、`CONTEXT.md` 和 `GEMINI.md`。[Gemini GEMINI.md]

该文档提供明确的用户控制面：`/memory show` 查看当前拼接内容，`/memory reload` 重新扫描并加载 hierarchy，footer 显示已加载 context 文件数量。文档还支持相对和绝对 `@file.md` imports，但没有给出 global override/base 的同级选择合同、独立 global byte/token 限额、thread journal snapshot 或 symlink target canonicalization 规则。[Gemini GEMINI.md]

### 2.4 OpenCode：全局规则首选 + 显式 instruction 集合

**文档事实。** OpenCode 在当前目录向上查找本地 `AGENTS.md`/Claude fallback，并检查 `~/.config/opencode/AGENTS.md`；若该 global 文件不存在，则可以使用 `~/.claude/CLAUDE.md` 作为兼容 fallback。其 precedence 说明是每个类别 first matching：本地 `AGENTS.md` 优先于本地 `CLAUDE.md`，OpenCode global 优先于 Claude global fallback。额外的 `instructions` 数组可以列出本地文件、glob 和 remote URL，这些内容与 AGENTS 文件组合。[OpenCode rules]

这个模型把“规则候选优先级”和“显式附加文件”分开。公开规则文档没有为 global instructions 给出独立预算、创建时 snapshot、resume/fork 重新加载或编辑后 active prompt 重算合同。OpenCode 其他 CLI 文档提供 `debug config`、logs、stats、session export/import 等观察工具，但这些不自动证明 instruction payload 已被完整显示或 reload。[OpenCode rules] [OpenCode CLI]

### 2.5 Aider：显式 convention attachment，而非自动 global hierarchy

**文档事实。** Aider 的 convention 文档建议把 `CONVENTIONS.md` 通过 `/read CONVENTIONS.md` 或 `aider --read CONVENTIONS.md` 加入 chat，并标记为 read-only；也可以在 `.aider.conf.yml` 中用 `read` 设置始终加载一个或多个文件。公开文档没有定义 `~/.aider/CONVENTIONS.md` 或祖先目录自动发现语义。[Aider conventions]

Aider 的 token 文档把 chat history、repository map 和 thinking tokens 分开估算，并明确 provider token limit 不由 Aider 强制。`/read`、`/drop`、`/reset`、`/tokens` 和 `/show-prompts` 提供显式 context 操作和观察面；公开 convention 文档没有承诺 convention 文件被修改后自动 reload，也没有定义恢复 session 时如何把 convention attachment 作为持久快照重放。[Aider conventions] [Aider token limits] [Aider commands]

## 3. 对照表

| 维度 | Codex CLI | Claude Code | Gemini CLI | OpenCode | Aider |
| --- | --- | --- | --- | --- | --- |
| 用户来源 | `$CODEX_HOME`，默认 `~/.codex` 的 `AGENTS.*` | managed、`~/.claude/CLAUDE.md`、`~/.claude/rules/` | `~/.gemini/GEMINI.md` | `~/.config/opencode/AGENTS.md`，再 fallback `~/.claude/CLAUDE.md` | 显式 `/read`、`--read` 或 config `read` |
| 同级选择 | override → base，第一个非空普通文件 | user/project/local/rules 组合 | 文档支持配置多个 filename，但未规定 override/base | 每个类别 first-match；额外 instructions 合并 | attachment 顺序/配置，未定义自动层级 |
| 与项目组合 | user 在 project entries 前；project 有独立 byte budget | 宽作用域先、窄作用域后，内容拼接 | global、workspace、JIT 内容拼接 | local/global 规则 + instructions 集合 | convention 作为普通已读 context |
| 用户专属硬预算 | 未见独立 user byte/token cap；project budget 不等同 user | CLAUDE.md 无公开硬 cap；`MEMORY.md` 的 200 行/25KB 不适用 | 未见 global 专属 cap | 未见 global 专属 cap | 沿用普通 context/token 估算 |
| 新 session / reload | fresh thread 重新 provider；普通 turn 保持创建时内容 | session 启动加载；`/compact` 重读 project root | `/memory reload` 显式重扫 | 自动 reload effective rules 未被公开承诺 | 显式 `/read`/`/reset`，自动 reload 未被承诺 |
| resume / fork | 测试覆盖 creation snapshot、source 删除和 fork replacement | durable instruction snapshot 细节未公开 | checkpoint/resume 与 memory reload 的关系未在该页锁定 | session continue/fork 有 CLI，但规则重算边界未公开 | convention attachment 的 resume 语义未公开 |
| 可观测性 | source provenance、源码测试；无等价稳定 `/rules` 合同 | `/context`、`/memory`、`InstructionsLoaded` | footer、`/memory show/reload` | `debug config`、logs、stats，不等于 prompt source list | `/tokens`、`/show-prompts`、显式 read |

## 4. 跨产品综合

### 4.1 先分层，再谈 precedence

公开证据支持至少四个不同概念：组织 managed policy、用户偏好、项目共享规则、当前 task 的显式附件。Claude 把 managed policy 与 behavioral CLAUDE.md 明确分开；Codex 和 OpenCode 把 home provider 与 project discovery 分开；Aider 则不把“总是读入一个文件”包装成隐式规则层。把它们都命名为 global instructions 会掩盖信任、分享范围和生命周期差异。

### 4.2 `override` 是选择算法，不是语义覆盖

Codex 的 `AGENTS.override.md` 选择一个同级 source，Claude 的 `CLAUDE.local.md` 反而追加在 base 后，OpenCode 的 `AGENTS.md` 与 `CLAUDE.md` 是类别内 first-match。因而产品若采用 override/base，必须记录“哪个候选被选中”和空白/读取错误的 fallback；若采用合并，则必须说明顺序，不能暗示自然语言冲突会被确定性解决。

### 4.3 预算与会话状态应拆成可验证的控制点

“用户文件不占 project-doc 预算”与“用户文件仍然占模型 context”可以同时成立。可验证的预算至少要区分：规则文件的加载上限、整体 context window、压缩/剪枝阈值、provider accounting，以及真正限制任务继续的 step/time budget。产品公开资料中没有证据支持用一个 `max_tokens` 名称代替这些概念。

### 4.4 snapshot、reload、resume、fork 不是同义词

一个实现可以在 session 创建时冻结 source/text，在 `/compact` 重读某一层，在 `/resume` 重放历史指令，并在 fork 时按新环境重新选择；Codex 的测试正好展示了这种分叉。另一个产品可以把 hierarchy 作为可 reload 的活动视图（Gemini），或只在显式 attachment 时改变 context（Aider）。生命周期合同必须分别写 fresh、ordinary turn、compact、resume、fork 和 explicit reload。

### 4.5 source provenance 是最小可解释性单元

一个文本 digest 不能回答“为什么这条指令生效”。至少需要 source path（或受保护的显示标识）、scope、选择结果、加载时点/版本，以及它是否完整或被截断。Claude 和 Gemini 已把其中一部分放进用户控制面；Codex 的源码保留 source list；OpenCode/Aider 的公开文档更偏向配置或普通 context 观察。

## 5. 陷阱与证据缺口

- **home 不等于稳定物理路径。** `$CODEX_HOME`、`~/.claude`、`~/.gemini` 和 `~/.config/opencode` 在容器、远程环境、profile 切换、sandbox 或 symlink home 下可能映射到不同目录；公开文档没有为所有产品承诺 canonicalization、权限继承或跨环境同步。
- **空文件和错误候选的处理不一致。** Codex 的 global provider 会在空 override 后尝试 base，并对可恢复读取错误记录 warning；项目 loader 的 fallback、错误传播和预算逻辑是另一段代码。不能从一个层的行为外推另一个层。
- **imports/remote files 不是普通 local file 的等价物。** Claude 的 `@` import、Gemini 的 `@file.md` 和 OpenCode 的 remote `instructions` 都扩大了 source graph；公开资料没有统一的大小上限、循环语义、信任审批或 snapshot 规则。
- **snapshot 可能保存文本，也可能只保存 source identity。** Codex 测试提供了文本重放和 source deletion 的具体行为；其他产品的 durable session 资料不足，不能把“下次 session 会加载”写成“同一 thread 会重放相同文本”。
- **instructions 仍是软提示。** Claude 明确说 CLAUDE.md 不是 enforced configuration；任何产品的 user instruction 都不应代替 hook、permission、sandbox 或其他硬控制，除非产品公开给出独立强制机制。

## References

- [OpenAI Codex: custom instructions with AGENTS.md](https://developers.openai.com/codex/agent-configuration/agents-md)（官方文档，访问：2026-08-04）。
- [OpenAI Codex: project instructions discovery](https://developers.openai.com/codex/config-file/config-advanced#project-instructions-discovery)（官方文档，访问：2026-08-04）。
- [Codex `agents_md.rs` at commit `78306a32`](https://github.com/openai/codex/blob/78306a32afe99ce88fbc3616f8ef325baae91cd0/codex-rs/core/src/agents_md.rs)（公开源码，提交/访问：2026-08-04）。
- [Codex `codex-home` user instruction provider](https://github.com/openai/codex/blob/022f1221e8af678c2c16f58aa09550545954d939/codex-rs/codex-home/src/instructions/mod.rs) 与 [provider tests](https://github.com/openai/codex/blob/022f1221e8af678c2c16f58aa09550545954d939/codex-rs/codex-home/src/instructions/tests.rs)（公开源码，访问：2026-08-04）。
- [Codex AGENTS instruction tests](https://github.com/openai/codex/blob/78306a32afe99ce88fbc3616f8ef325baae91cd0/codex-rs/core/tests/suite/agents_md.rs)（公开测试，访问：2026-08-04）。
- [Anthropic Claude Code: How Claude remembers your project](https://code.claude.com/docs/en/memory)（官方文档，页面访问：2026-08-04）。
- [Anthropic Claude Code: `InstructionsLoaded` hook](https://code.claude.com/docs/en/hooks#instructionsloaded)（官方文档，访问：2026-08-04）。
- [Gemini CLI: `GEMINI.md` context files](https://geminicli.com/docs/cli/gemini-md/)（官方文档，访问：2026-08-04）。
- [OpenCode: rules](https://opencode.ai/docs/rules/)（官方文档，页面更新时间：2026-08-04；访问：2026-08-04）。
- [OpenCode: CLI](https://opencode.ai/docs/cli/)（官方文档，访问：2026-08-04）。
- [Aider: specifying coding conventions](https://aider.chat/docs/usage/conventions.html)（官方文档，访问：2026-08-04）。
- [Aider: token limits](https://aider.chat/docs/troubleshooting/token-limits.html) 与 [in-chat commands](https://aider.chat/docs/usage/commands.html)（官方文档，访问：2026-08-04）。
