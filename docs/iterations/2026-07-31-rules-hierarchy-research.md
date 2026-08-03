# 迭代：项目指令层级下一步研究

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-07-31 |
| 分支 | `codex/rules-hierarchy-next-research` |
| 状态 | **研究完成，未实现** |
| 研究产物 | [rules-hierarchy-next-research.md](../research/rules-hierarchy-next-research.md) |
| 前置实现 | root-only override 合同（`96a5201`） |

## 1. 目标

复核 OpenAI Codex CLI 与 Claude Code 对用户全局、project root → cwd、cwd 以下嵌套指令、优先级、fallback、symlink、预算、生命周期、worktree 与可观测性的当前公开行为，并为 root-only override 之后的最小实现锁定可验收合同。

本迭代只改研究和迭代文档，不修改产品代码、配置或用户文档。

## 2. 证据方法

- OpenAI：先按 `openai-docs` 流程运行 Codex manual helper；官方站点 HEAD 请求超时，因此用 OpenAI 官方页面入口加公开仓库固定提交 `f0c30e5` 的源码与测试交叉复核。
- Anthropic：读取 2026-07-31 可访问的官方 memory 与 hooks 页面；memory 页声明 `dateModified=2026-07-22`。
- 结论逐项分为文档合同、固定源码观察、推断 / 未知；未用社区文章填补产品内部行为。
- 本地只读 `docs/architecture.md`、`internal/agent` 加载器、配置接线、workspace root 解析和 session 冻结路径，用于选择包边界，不作为行业证据。

## 3. 研究结论

1. Codex：用户全局先于项目；项目 root → cwd 每目录一个候选；默认项目聚合预算 32 KiB；固定源码普通 turn 使用 creation-time snapshot。
2. Claude：managed → user → project/local；祖先链启动加载，cwd 后代按访问惰性加载；`CLAUDE.md` 没有公开硬限；`/context` 与 `InstructionsLoaded` 提供逐文件观测。
3. 两者的文件顺序都不是自然语言冲突的硬覆盖保证。
4. 本仓库当前所有权和冻结语义足以支持一个窄的 project hierarchy 增量，无需新增包或改账本分层。

## 4. 推荐的下一实现

只扩展 `internal/agent`：从 canonical workspace root 到启动 cwd，每目录按 `AGENTS.override.md` → `AGENTS.md` 选一个非空普通文件，root → cwd 组合，共享现有 `[rules].max_tokens`，并返回结构化 source/truncation 元数据。

启动 cwd 若不在显式 workspace root 内，退化为 root-only，以保持现有“从任意目录指向另一个 workspace”用法。新 session、`/new`、`/clear` 重载；普通 turn、compact、resume 冻结。

完整输入、发现、预算、生命周期和测试合同见研究文 §5。

## 5. 非目标

- 用户全局 AGENTS、fallback 文件名、Claude 文件/import 兼容。
- Git root marker 自动发现或修改 workspace sandbox 边界。
- cwd 以下 lazy loading、热刷新、turn cwd 切换。
- managed-worktree 文件复制、`/rules`、hooks、rules journal/digest。
- 任何产品代码实现。

## 6. 验收

- [x] 覆盖用户要求的十个行为维度。
- [x] 区分文档合同、源码观察、推断 / 未知。
- [x] 固定 Codex 源码提交并引用关键源码/测试。
- [x] 给出可交给实现子任务的合同、测试面和非目标。
- [x] `go test ./...`
- [x] `go build ./...`
- [x] `go tool golangci-lint run ./...`
- [x] `git diff --check`
- [x] 提交后工作区干净。
