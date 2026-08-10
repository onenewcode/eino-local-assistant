# 迭代：默认运行时注册项目 skills 工具

日期：2026-08-11

## 背景

项目级 `list_skills` 与 `read_skill` 已有受 workspace 限制的实现和独立测试，但默认 runtime registry 未注册它们。因此模型的默认 tool surface、`--tools default` 和 `/permissions` 描述与既有项目 skills 能力不一致，实际无法按需调用。

## 实现

- `DefaultWithOptions` 现在在默认 registry 中注册 `list_skills` 和 `read_skill`；这两个工具只读，并复用 shell workspace root 作为唯一发现作用域。
- 读取仍先经过发现结果：扫描 conventional skill roots 的直接子目录，数量最多 100 个；内容默认最多 16 KiB、硬上限 64 KiB，并返回截断状态。
- invocation-scoped tool filter 保持原有语义：`--tools` 可显式只允许少数工具，`--tools default --disable-tools ...` 也能排除 skill 工具；skill 可发现不等于权限或沙箱边界放宽。
- `/permissions` 与 README 的 tool surface 增列这两个工具，避免用户看到的能力清单与实际 registry 脱节。

## 参考与取舍

Codex CLI `0.146.0` 将 `skill_search` 列为 stable；Claude Code `2.1.220` 将 skills 作为可关闭的 slash-command 能力。这支持“发现、再读取”的独立能力边界，但两者均未公开其完整内部加载细节。

本轮不自动把全部 `SKILL.md` 注入每个 prompt，也不新增写入、执行或越过 workspace 的能力；模型必须先发现，再读取一个有明确字节上限的 skill。详见 `docs/research/on-demand-agent-skills-research.md`。

## 验证

- 新增默认 registry 集成测试：实际调用 `list_skills` 与 `read_skill`，确认二者使用 runtime workspace root。
- 更新 tool filtering 测试，确保默认 surface 包含 skills，显式 allow/deny 仍只影响本次调用。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
