# 迭代：结构化 `git_show` 工具

日期：2026-08-08

## 背景

`git_log` 能定位相关提交，但审阅历史变更还需要查看某个 commit 的实际 patch。把 `git show` 留给 shell 会让提交标识、路径和输出大小缺少统一边界；主流 code agent 的 Git 探索工具通常支持 bounded 的 commit detail。

## 实现

- 新增 `git_show`，输入必填 `commit`，可选 workspace-relative `path` 和 `max_bytes`。
- 默认输出上限 64 KiB，硬上限 1 MiB，超限返回 `truncated=true`。
- 拒绝空 commit、以 `-` 开头的选项和包含空白的多值 ref；路径仍经过 workspace boundary 校验。
- 使用直接参数调用 `git show --no-ext-diff --no-color`，只读、不经过 shell、不执行权限敏感操作。
- 注册到默认工具集，TUI、`exec`、review 和 MCP 共用同一能力。
- 增加真实临时 Git 仓库测试，覆盖 HEAD patch、路径筛选和不安全输入。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
