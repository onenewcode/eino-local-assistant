# AGENTS.md 项目规则加载

## 背景

Codex、Claude Code 等 coding agent 会把项目级 instruction 文件纳入新会话上下文。本项目此前只使用配置里的 assistant system prompt，仓库中的 `AGENTS.md` 不会自动传给模型。

## 本轮变更

- 新建 session 时，从 workspace 祖先目录到当前 workspace 按顺序加载规则文件。
- 同目录存在 `AGENTS.override.md` 时替代 `AGENTS.md`；`AGENTS.local.md` 始终作为该目录的本地追加规则。
- 越靠近 workspace 的文件追加在后，便于局部规则覆盖/补充通用规则。
- 单个 instruction 文件限制 128 KiB；读取失败或超限会阻止新会话启动，避免静默丢失规则。
- TUI 的 `/new` 复用已加载的有效 system prompt。
- 恢复已有 session 不重新拼接当前文件，保持 durable ledger 中的 system prompt 不变。

## 边界

本轮只加载 `AGENTS.md`、`AGENTS.override.md` 和 `AGENTS.local.md`，不实现 rules glob 或远程策略；项目文件内容仍属于不可信输入，不能替代权限系统。
