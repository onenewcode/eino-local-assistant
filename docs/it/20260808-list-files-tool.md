# 迭代：list_files 工具

日期：2026-08-08

## 背景

主流 code agent 在探索工作区时需要先建立目录结构；仅依赖 shell 或正则搜索会让模型为了导航执行更多命令，也容易把隐藏文件和超大目录一次性塞入上下文。

## 实现

- 新增工作区受限的 `list_files` 只读工具，支持 `path`、`depth`、`max_entries` 和 `include_hidden`。
- 默认深度为 1、最多返回 200 项；深度上限 5、条数上限 1000，超限返回 `truncated=true` 而不是无界输出。
- 默认跳过 dotfiles/dot-directories，并始终跳过 `.git`；`include_hidden=true` 只打开其他隐藏项。
- 目录遍历不跟随 symlink 目录，返回 `symlink` 类型，路径仍经过现有 workspace boundary 校验。
- 工具已注册到 TUI 和 `exec` 共用的默认 registry。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
