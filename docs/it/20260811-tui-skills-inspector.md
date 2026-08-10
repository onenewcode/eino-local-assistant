# 迭代：TUI 项目 skills 检查器

日期：2026-08-11

## 背景

默认 runtime 已向 agent 注册 `list_skills` 和 `read_skill`，但 TUI 尚无用户可见入口。这会让用户无法核对当前 workspace 的可用工作流、路径和摘要，也无法在请求模型使用前检查单个 `SKILL.md` 的内容。

## 实现

- 新增 `/skills`：通过当前 runtime registry 的 `list_skills` 列出项目 skills 的名称、相对路径和摘要；发现为空和 result limit 都有明确状态。
- 新增 `/skills <name>`：通过同一 registry 的 `read_skill` 读取一个已发现的 name 或相对路径。工具仍负责 discovery-only 授权、workspace 作用域和默认 16 KiB / 最大 64 KiB 上限；TUI 不直接读取任意文件路径。
- 两个命令都是只读本地检查：不创建模型 turn、不改 session ledger、usage 或 queue；正常 turn、压缩和 approval 等待期间也立即执行，不会中断前台操作。
- 命令输出二次清理终端控制字节，TUI preview 另有 64 KiB 防线；页面持续标注 skill 内容只是项目数据，不能覆盖系统、安全或权限规则。
- callback 从过滤后的 invocation registry 查找工具。若 `--tools` 排除了 `list_skills` 或 `read_skill`，TUI 明确报告该能力在本次运行不可用，而非绕过用户的 tool selection。

## 参考与取舍

Codex CLI `0.146.0` 把 `skill_search` 标为 stable；Claude Code `2.1.220` 将 skills 作为可以关闭的 slash-command 能力。两者支持将 skills 做成显式、可发现的产品面，但公开行为不足以推断其私有 prompt 注入细节。

本轮采用“列表后再读取”的 inspector，而不是把 `/skills <name>` 隐式作为 agent prompt 或直接执行其中步骤。这样用户能检查项目数据，同时保留已有模型、审批、sandbox 和 tool-filter 边界。业界证据和已知缺口见 `docs/research/on-demand-agent-skills-research.md`。

## 验证

- 新增 TUI 测试覆盖 list/read、callback 缺失或失败、控制字符清理、preview 截断、无 session/queue mutation，以及忙碌时不取消 foreground turn。
- 新增 runtime 集成测试，确认两个 TUI callback 使用默认 registry 的 `list_skills` / `read_skill`，并尊重过滤后工具缺失的边界。
- 更新 slash parser、补全菜单和 busy input 分类测试。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
