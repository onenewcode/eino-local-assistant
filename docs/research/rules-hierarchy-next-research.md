# CLI 项目指令层级：Codex CLI 与 Claude Code 公开行为复核

> 状态：研究与下一步合同，不代表本仓库已实现层级加载。
>
> 研究日期：2026-07-31。产品和公开源码会变化，实施前应按固定版本复核。
>
> 决策面：在本仓库已有“workspace 根目录二选一 `AGENTS.override.md` / `AGENTS.md`”之后，下一次最小交付应扩展哪一段发现与组合语义。

范围包括用户全局指令、project root 到启动 cwd 的祖先链、cwd 以下嵌套指令、候选文件与优先级、符号链接、预算、加载/刷新、worktree 和可观测性。不讨论权限强制、auto memory、skills、模型对自然语言冲突的保证，也不把 Claude 的 `MEMORY.md` 限额当成 `CLAUDE.md` 限额。

证据标签：

- **文档合同**：产品官方文档明确陈述的用户行为。
- **源码观察**：OpenAI Codex 固定提交 `f0c30e5` 的公开源码或测试；它能解释当前实现，但不自动成为长期兼容承诺。
- **推断 / 未知**：由证据推导的设计含义，或公开资料没有锁定的边界。

## 1. 结论

1. 两个产品都按“宽作用域先、窄作用域后”组织模型可见内容，但都不是客户端级自然语言冲突解析器。Claude 明说冲突时模型可能任意选择；Codex 的目录顺序也只保证拼接顺序。
2. Codex 的核心模型是：`$CODEX_HOME` 用户层先行；项目根到 cwd 每目录最多一个文件；项目层共享默认 32 KiB 字节预算。Claude 的核心模型是：managed → user → 祖先 project/local；cwd 以下指令按访问惰性加载；`CLAUDE.md` 没有公开硬字节/token 上限。
3. 两者都允许 symlink，但 worktree 语义不同：Codex-managed worktree 特判复制被忽略的 `AGENTS.override.md`；普通 Claude worktree 不复制 gitignored `CLAUDE.local.md`，官方建议从 home import。
4. Claude 的观测面明显更完整：`/context` 展示实际加载文件，`InstructionsLoaded` hook 给出路径和原因。Codex 公开源码保留结构化 source provenance，但当前公开用户文档没有给出等价的逐文件 hook 或专用 `/rules` 合同。
5. **本仓库下一步只实现 workspace root → 启动 cwd 的项目层级**。暂不加用户全局层、fallback 文件名、cwd 以下惰性加载或 `/rules`。这样保持现有 `internal/agent` 所有权、单一 `[rules].max_tokens` 和 create-time system prompt 冻结模型，不引入新的持久化或 TUI 生命周期。

## 2. 行为对照

| 维度 | OpenAI Codex CLI | Claude Code |
| --- | --- | --- |
| 用户全局 | **文档合同 / 源码观察**：在 `$CODEX_HOME`（默认 `~/.codex`）按 `AGENTS.override.md`、`AGENTS.md` 顺序取第一个非空普通文件；全局内容排在项目内容前。[1][4] | **文档合同**：`~/.claude/CLAUDE.md`，排在 managed policy 后、project/local 前。[5] |
| project root | **文档合同 / 源码观察**：通常以最近 `.git` 为 root；可配置 root markers。找不到 marker 时只检查 cwd；不越过 root。[1][2] | **文档合同**：从文件系统根向 cwd 走祖先链；公开合同不以 Git root 截断。[5] |
| root → cwd | **文档合同 / 源码观察**：root 到 cwd 顺序拼接，靠近 cwd 的文件后出现；每目录最多选一个候选。[1][2] | **文档合同**：祖先目录内容从文件系统根到 cwd 排序；同目录 `CLAUDE.local.md` 在 `CLAUDE.md` 后。[5] |
| cwd 以下嵌套 | **文档合同 / 源码观察**：启动发现止于 cwd，不预读后代；若后续 turn 的 environment/cwd selection 改变，固定源码会刷新选择。[1][3] | **文档合同**：读取某个子目录文件时，才加载该子目录的 `CLAUDE.md` / `CLAUDE.local.md`；path-scoped rules 也可按匹配惰性加载。[5][6] |
| 同目录优先级 | **文档合同 / 源码观察**：`AGENTS.override.md` → `AGENTS.md` → 配置 fallback，首个普通文件胜出。[1][2] | **文档合同**：base 与 local 都加载，local 后置；不是 override 替换 base。[5] |
| fallback | **文档合同 / 源码观察**：`project_doc_fallback_filenames` 是项目目录候选的有序尾部；不用于 `$CODEX_HOME`。[1][2][4] | **文档合同**：运行时原生读取 `CLAUDE.md`，不把 `AGENTS.md` 当 fallback；可用 `@AGENTS.md` import 或 `CLAUDE.md` symlink 适配。[5] |
| symlink | **源码观察**：项目发现明确允许 symlink，metadata 必须解析为普通文件；全局加载也使用跟随 symlink 的 metadata/read。[2][4] | **文档合同**：`CLAUDE.md -> AGENTS.md` 可用；`.claude/rules/` symlink 正常解析，并检测循环。[5] |
| 预算 | **文档合同 / 源码观察**：项目文档默认总预算 32 KiB，可由 `project_doc_max_bytes` 调整或设 0 禁用；root 先消费，后续文件按剩余字节截断。用户全局内容不计入该项目预算。[1][2] | **文档合同**：`CLAUDE.md` 全量加载，官方只建议每文件少于 200 行以改善遵守率；没有公开 byte/token 硬限。200 行/25 KB 是 auto-memory `MEMORY.md` 的限制，不能外推。[5] |
| 生命周期 | **文档合同**：每次 CLI run / TUI session 建立项目指令，文件改动后重启以取得确定刷新。[1] **源码观察**：普通 turn 保留 creation-time snapshot；environment selection 改变会刷新项目选择。[3] | **文档合同**：祖先链在 session 启动加载，后代惰性加载；`/compact` 后从磁盘重读 project-root `CLAUDE.md`，嵌套文件等再次访问才重载。[5][6] |
| worktree | **文档合同**：Codex-managed worktree 自动复制被忽略的 `AGENTS.override.md`，无需加入 `.worktreeinclude`。[7] 普通 Git worktree 没有此承诺。 | **文档合同**：gitignored `CLAUDE.local.md` 只在创建它的 worktree；跨 worktree 共享应 import home 文件。auto memory 另行按同一 repo 跨 worktree 共享。[5] |
| 可观测性 | **源码观察**：加载结果保留 source path provenance，测试锁定 global-before-project 与 creation-time source list。[2][3] **证据缺口**：未找到与 Claude `InstructionsLoaded` 等价的公开用户 hook 合同。 | **文档合同**：`/context` 显示实际加载的 memory files；`/memory` 显示可用位置；`InstructionsLoaded` hook 报告 `file_path` 和 `session_start`、`nested_traversal`、`path_glob_match`、`include`、`compact` 等原因，且只能观测、不能阻止或修改。[5][6] |

## 3. 关键边界与证据缺口

### 3.1 “后置”不是硬覆盖

Claude 官方文档明确说矛盾规则可能被模型任意选择。Codex 的源码只构造有序文本，没有语义级 key/value merge。因此本仓库可以承诺“root 先、cwd 后”的顺序，不能承诺自然语言的“nearest always wins”。

### 3.2 Codex 空 override 的边界不应照搬

固定源码中，项目发现先按 metadata 选择候选，后续读取时才跳过空文本；因此同目录空 `AGENTS.override.md` 可能挡住非空 `AGENTS.md`。`$CODEX_HOME` loader 则读完空 override 后继续尝试 base。[2][4] 这是不一致的源码观察，不宜升级为本仓库合同。当前本仓库已经明确“空白 override 回退 base”，下一步应保持它。

### 3.3 字节预算与近端优先存在张力

Codex 从 root 开始消费字节预算；预算用尽会截断或完全丢掉更靠近 cwd 的文件。[2] 所以“近端后置”不等于“近端在预算压力下优先保留”。本仓库当前按 token 估算截断单个 root 文件。为了保持已有配置和最小变更，下一步仍采用 root-first 的单一 token 预算，但必须在返回结构中标记被截断的 source，不能把它描述为 nearest-preserving 策略。

### 3.4 Claude 的两个 project 文件位置仍有未锁定细节

Claude 文档允许 project instructions 位于 `./CLAUDE.md` 或 `./.claude/CLAUDE.md`，但没有在该页明确两者同时存在时是否都加载及相对顺序。[5] `/context` 和 `InstructionsLoaded` 是实际版本上的裁决面。本仓库下一步不引入这两个文件名，因此无需猜测。

### 3.5 symlink 与信任

两个产品都允许 symlink，这说明“链接本身位于发现目录、目标可在别处”是现实用法；公开文档没有把它描述为安全隔离边界。项目指令是软提示，不能借 symlink 范围代替 sandbox/permissions。本仓库当前 `os.Stat` + `os.ReadFile` 已跟随指向普通文件的 symlink；下一步保持，不扩大成递归 import。

## 4. 最小本地架构审计

这部分只用于确定改动边界，不作为外部产品证据。

- `internal/agent/project_instructions.go` 已负责 root 候选选择、symlink-to-regular、空白 fallback、token 截断和 prompt 格式化；层级仍应留在该包，不新增 `internal/rules`。
- `cmd/eino-assistant/run_tui.go` 已解析 canonical `workspaceRoot`，并只在创建新 session 时调用 `ComposeWithLayers`。`/resume` 直接使用账本里的 create-time system prompt。
- `tools.ResolveWorkspaceRoot` 在未配置 `[workspace].root` 时把启动 cwd 当作 workspace root；显式 root 可以与启动 cwd 不同。因此层级加载需要单独传入 `StartDir`，不能把安全 clamp 的 workspace root 偷换成 Git root。
- `[rules].max_tokens` 当前是整个规则 block 的 token 上限。扩展成层级后继续作为项目链聚合上限，避免同时引入 byte 配置或每文件配置。
- thread journal 已持久化完整 system prompt，compaction 使用 immutable system message；不需要为本步修改 `internal/store` / `internal/chat`。

## 5. 可交给实现子任务的下一步合同

### 5.1 输入与范围

将 project instruction loader 的输入扩展为：

```text
WorkspaceRoot  canonical workspace/security root
StartDir       process startup cwd
MaxTokens      existing [rules].max_tokens aggregate budget
```

若 canonical `StartDir` 位于 `WorkspaceRoot` 内，发现链为 `WorkspaceRoot` 到 `StartDir`（两端都含）；否则为兼容显式 `[workspace].root` 指向其他目录的现有用法，发现链退化为只检查 `WorkspaceRoot`，并在返回 bundle 中记录该 fallback，不能报错阻止启动。

### 5.2 发现与选择

1. 按 root → selected start dir 枚举真实目录链；不向 workspace root 之外上探，不自动寻找 `.git`，不扫描 start dir 后代。
2. 每目录依次检查 `AGENTS.override.md`、`AGENTS.md`。
3. 缺失、非普通文件、读取后仅空白的候选继续尝试下一个；指向普通文件的 symlink 允许，保持现有行为。
4. 每目录最多选择一个非空候选。跨目录全部保留，按 root → start dir 顺序组合。
5. 文件读取错误保持当前 fail-fast；不静默把权限错误当缺失。

### 5.3 预算与格式

1. `[rules].max_tokens` 是整个项目链的聚合上限，包括为每个 source 生成的可见标题/截断提示。
2. 从 root source 开始顺序消费预算；某 source 超出剩余预算时保留能容纳的 rune-safe 头部和截断提示，预算耗尽后停止。
3. bundle 至少返回每个已选择 source 的绝对发现路径、估算 token 数、是否截断，以及 start-dir-outside-root fallback 标志；这为测试与后续 `/rules` 提供结构化事实。
4. prompt 中每个 source 有独立、稳定的相对路径标题，避免多个 `AGENTS.md` 无法区分；标题本身不宣称硬覆盖。

### 5.4 生命周期与验收

- 新 session、`/new`、`/clear` 重新捕获并加载；普通 turn、`/compact`、`/resume` 沿用 create-time system prompt。
- 不增加文件 watcher 或热 reload。
- 至少测试：root-only 兼容、root+中间+cwd 顺序、每目录 override/base、空白 fallback、start dir 越界退化、symlink-to-file、非普通文件、聚合预算跨文件截断、读取错误、resume 仍使用旧 prompt。
- 交付门槛：`go test ./...`、`go build ./...`、`go tool golangci-lint run ./...`、`git diff --check`。

## 6. 非目标

- `$CODEX_HOME` / `~/.eino-*` 用户全局 AGENTS 层。
- `project_doc_fallback_filenames`、`CLAUDE.md`、`.claude/CLAUDE.md` 或 `@import`。
- 自动 Git root / marker 发现，或改变 `[workspace].root` 的 sandbox clamp 含义。
- cwd 以下惰性加载、path globs、turn 中 cwd 切换、watcher、reload。
- Codex-managed worktree 复制、`.worktreeinclude` 或 Claude auto-memory 的跨-worktree 行为。
- `/rules`、hook、journal 中独立 rules digest/source 事件；bundle 只为以后保留结构化来源。
- 将软项目指令升级为权限、sandbox 或确定性的冲突覆盖。

## 7. 后续顺序

完成上述层级后，下一候选应是一个独立的用户全局层：先定义 home 位置、override/base 选择、是否拥有独立预算，以及它是否进入 thread snapshot。再下一步才是 `/rules` 或等价可观测面。不要把 global、lazy traversal、worktree copy 和 UI 一次并入层级 loader。

## 参考资料

1. OpenAI Codex，[Custom instructions with AGENTS.md](https://developers.openai.com/codex/agent-configuration/agents-md) 与 [Project instructions discovery](https://developers.openai.com/codex/config-file/config-advanced#project-instructions-discovery)（访问：2026-07-31；本次官方 manual helper 因站点 HEAD 超时，关键行为另由固定源码复核）。
2. OpenAI Codex 公开源码，固定提交 [`f0c30e5`](https://github.com/openai/codex/commit/f0c30e528a54bdf0fa9a4d52ff74b34383434811)：[`agents_md.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/src/agents_md.rs)、[`agents_md_tests.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/src/agents_md_tests.rs) 与 [`config.schema.json`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/config.schema.json)（提交时间及访问：2026-07-31）。
3. OpenAI Codex 固定提交测试：[`core/tests/suite/agents_md.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/tests/suite/agents_md.rs) 与 [`model_visible_layout.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/core/tests/suite/model_visible_layout.rs)（访问：2026-07-31）。
4. OpenAI Codex 固定提交，[`codex-home/src/instructions/mod.rs`](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/codex-home/src/instructions/mod.rs)（访问：2026-07-31）。
5. Anthropic Claude Code，[How Claude remembers your project](https://code.claude.com/docs/en/memory)（页面 `dateModified`：2026-07-22；访问：2026-07-31）。
6. Anthropic Claude Code，[Hooks / InstructionsLoaded](https://code.claude.com/docs/en/hooks#instructionsloaded)（访问：2026-07-31）。
7. OpenAI Codex，[Git worktrees / Copy ignored local files into managed worktrees](https://developers.openai.com/codex/environments/git-worktrees#copy-ignored-local-files-into-managed-worktrees)（访问：2026-07-31）。
