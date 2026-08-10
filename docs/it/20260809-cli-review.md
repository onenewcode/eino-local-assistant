# CLI non-interactive review

本轮增加独立的 `review [instructions]` 子命令，对齐 Codex CLI 已验证的 `--uncommitted`、`--base <BRANCH>`、`--commit <SHA>`、`--title <TITLE>` 与 sole `-` stdin 入口。它面向 CI、脚本和普通 shell，不启动 TUI，并复用现有 exec/session/tool loop，而不是再维护一套 review agent。

## 行为

- `--uncommitted`、`--base` 与 `--commit` 三选一；未指定时默认 uncommitted。`--title` 只允许用于 commit review。option-like、含 NUL 或空白的 Git ref 在 provider 请求前拒绝。
- uncommitted prompt 要求先取 `git_status`、working-tree `git_diff` 与 staged `git_diff`，再读取相关 untracked 文件；base review 使用新的 `git_diff {"base":"..."}`，执行 `git diff --no-ext-diff --merge-base <base> HEAD --`；commit review 使用 `git_show`。
- 可追加多词 instructions；sole `-` 从 stdin 读取。参数与 stdin 两条路径统一限制为 1 MiB，避免把无界输入注入模型上下文。
- review 输出坚持 findings-first，按 severity 排序，要求具体路径/行号、影响与测试缺口，并过滤纯风格或无证据推测。
- 强制覆盖为 `permission_mode=plan`、`high_risk_policy=deny` 与 `workspace_only=true`。即使用户配置为 unrestricted/advisory，`edit_file`、`apply_patch`、`run_command` 等 permission-gated side effect 也不能执行；该边界没有暴露可放宽 flag。
- 复用 exec 的 `--output-format text|json|jsonl`、`-o/--output-last-message`、`--output-schema`、`--model`、`--cd`、`--max-steps` 与 `--ephemeral`。默认 review 保留可恢复 session；ephemeral review 在进程退出时删除临时账本。

## 验证

单元测试覆盖默认 selector、互斥组合、title 约束、危险 ref、stdin/参数大小上限，以及三类 evidence prompt。OpenAI-compatible SSE 回归验证正常 review 返回 `type=result` JSON，请求包含 uncommitted instructions、`git_status` 与 `git_diff` schema；另一回归让模型主动调用 `edit_file`，确认命令以 machine-readable permission error 失败且目标文件保持不变。`git_diff` 的临时 Git 仓库测试确认 merge-base diff 内容正确，并拒绝 staged/base 混用和不安全 ref。

## 已知边界

本轮实现 Codex selector 模型的精简非交互子集，不包含交互式 review picker、云端 review 状态或 PR 平台发布。当前 agent loop 将被拒绝的 tool call 作为本次执行错误终止，不会把 denial 回送模型后继续生成降级回答；JSON/JSONL 仍输出 `type=error` 并保持非零退出码。
