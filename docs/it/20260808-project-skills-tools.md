# 迭代：项目 skills 的发现与按需读取

日期：2026-08-08

## 背景

主流 code agent 会把可复用工作流放在项目级 `SKILL.md` 中，并按需加载，而不是把所有技能全文固定塞进每个 prompt。当前仓库虽然支持项目规则文件，但模型没有明确的 skills 发现/读取契约。

## 实现

- 新增 `list_skills`，bounded 扫描 workspace 下 `.eino-assistant/skills`、`.claude/skills`、`.codex/skills`、`.agents/skills` 和 `skills` 的直接子目录。
- 每个 skill 必须包含 `SKILL.md`；发现结果只返回名称、相对路径和首个标题/摘要，不自动加载全文。
- 新增 `read_skill`，只能读取已发现的 skill，默认 16 KiB、硬上限 64 KiB，支持按名称或发现到的相对路径读取。
- 同名 skill 按约定目录优先级取第一个，避免项目配置不确定；所有路径仍受 workspace root 限制。
- skill 内容作为项目数据交给模型，不得覆盖 system/project security/permission 规则。
- 两个工具均为只读，注册到 TUI、`exec` 和 MCP 共用的默认 registry。
- 增加发现、摘要、按需读取、截断、未知 skill 和优先级测试。

## 约定示例

```text
.claude/skills/review/SKILL.md
```

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
