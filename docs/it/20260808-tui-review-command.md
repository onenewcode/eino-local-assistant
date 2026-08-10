# 迭代：TUI `/review` 变更审阅

日期：2026-08-08

## 背景

主流 code agent 通常提供快捷 review workflow：用户不必手写“请审阅当前修改”，即可让 agent 读取 diff、区分严重程度并给出带路径/行号的 findings。仓库已有 `git_diff`，但此前只有查看 diff 的 `/diff`，缺少完整的 agent review 入口。

## 实现

- 新增 `/review [focus]`，在 idle 时启动一个正常 Session turn。
- 生成的内部 prompt 要求模型先使用 `git_diff`/`git_status`，按严重程度优先报告 findings，尽量携带文件和行号，并明确禁止修改文件。
- 可选 `focus` 会附加到审阅任务，例如 `/review authentication`。
- review 走普通 model/tool 循环，因此保留上下文、权限、MCP、tool journal、取消和 usage 统计语义。
- busy 时 `/review` 被视为 mutative/turn-start 命令而拒绝，不会排队或插入当前 turn。
- `/help`、slash 补全和 README 同步更新。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
