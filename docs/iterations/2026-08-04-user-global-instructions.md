# 迭代：用户级全局 instructions 最小能力

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-08-04 |
| 范围 | home-scoped user instructions + project instructions composition |
| 状态 | 已交付；实现与完整门槛验证通过 |
| 研究依据 | [user-global-instructions-cross-product-research.md](../research/user-global-instructions-cross-product-research.md) |

## 1. 目标与边界

为本机用户提供一个独立于 workspace 和可配置 `storage.data_dir` 的软 context
层：默认读取 `~/.eino-assistant/AGENTS.override.md` 或 `AGENTS.md`，每次最多选择
一个有效候选。该层不改变权限、sandbox、memory 或 session store 语义，也不提供
`CLAUDE.md`、imports、watcher 或 reload 命令。

## 2. 实现合同

- 用户和项目 loader 都归属 `internal/agent`；同目录按 override、base 顺序选择，
  空白/非普通文件回退，普通文件 symlink 跟随，非 `NotExist` 的 I/O 错误失败。
- 用户 block 标题明确为 `User instructions`，保留选中文件 path、token、truncated、
  found 等元数据；默认 `rules.global_max_tokens = 4000`，与项目 `rules.max_tokens`
  独立，使用 `usage.EstimateText` 的 rune-safe 截断。
- 新 session 的顺序是 persona/tool policy -> user instructions -> project
  instructions -> memory。`rules.enabled = false` 时两类 AGENTS 都不加载；缺省的
  global root 保持旧的 project-only 调用兼容。
- runtime 构造时固定 `os.UserHomeDir()/.eino-assistant`。fresh、`/new`、`/clear`
  使用 composer 重新读磁盘；resume 直接使用 `chat.OpenSession` 的冻结 system
  prompt，不重组当前文件。

## 3. 生命周期与安全

用户 instructions 是软提示，不能替代 `permissions`、`sandbox` 或 tools 的硬约束。
文件内容在创建 session 时进入 durable system snapshot；编辑 home 文件不会影响当前
session，下一次 fresh/new/clear 才生效。用户 home 路径只是 context 来源，不会新增
任何 sandbox mount、读写权限或 protected-path 语义。

## 4. 验证

Focused tests cover missing files, override precedence, empty fallback, non-regular files,
symlink provenance, read errors, Unicode budgets, independent global/project budgets,
disabled rules, and fresh composer reload behavior. The full repository gate is run after
integration before the implementation commit is pushed.
