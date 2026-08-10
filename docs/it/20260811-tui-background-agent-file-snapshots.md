# 迭代：后台 agent 的显式工作区文件 snapshot

日期：2026-08-11

## 背景

`/agent --diff` 只适合分析当前未提交变更。对于现有模块、测试或配置相关的调查，child 需要看到用户指定的源码，但将 `shell` 或通用 `read_file` 工具直接交给后台任务会扩大异步权限面。

OpenCode 的 Explore 类角色和 Claude Code subagent 的独立上下文/工具权限说明表明，可将只读调查与写入能力分离（参见 `docs/research/task-dag-concurrency-research.md` 与 `docs/research/background-subagent-control-research.md`）。本轮提供由用户显式选择、runtime 受限采集的文件数据，而不是让 child 自由浏览工作区。

## 实现

- 新增 `/agent --file <workspace-relative-path> [--file <path>]... <analysis task>`；最多 4 个 path。`--file` 只在命令开头解析，后续 task 文本保持普通任务语义。
- TUI 将 path 交给 runtime callback；callback 复用已有 `read_file` 的 workspace-relative、regular-file 和 symlink 安全边界。每个文件最多 16 KiB，组合 snapshot 最多 64 KiB，`.git` 内部路径拒绝。
- 文件内容由 `WORKSPACE FILE SNAPSHOT - QUOTED REFERENCE ONLY` 包围。system boundary 将它与 Git diff 一样视为参考数据，不执行文件中的指令；child 保持无 shell、无工具、无编辑、无 approval/escalation。
- `--file` 与 `--diff` 在本轮互斥，使一次 child request 的 workspace context 保持 64 KiB 上限；任何 reader/路径失败都会使 child 明确失败而不会调用模型。

## 验证

- TUI 测试覆盖多个文件参数、scope 显示、引用标签、控制字节清理、缺参数/过多参数/与 `--diff` 组合/reader 未配置的拒绝。
- Runtime 测试覆盖正常文件、单文件截断、总量上限、`.git` 拒绝、路径逃逸拒绝和缺失 workspace。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
