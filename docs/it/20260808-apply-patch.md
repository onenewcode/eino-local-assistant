# Iteration 20260808: unified patch editing

## Goal

在精确字符串替换之外，补齐 code agent 常用的 hunk 编辑方式，让模型可以提交带上下文的多行修改，并在上下文过时的时候安全重读，而不是继续覆盖文件。

## Reference behavior

Codex CLI 的 `apply_patch` 与 Claude Code 的编辑工作流都把文件修改表达为带上下文的 patch。patch 应先验证，再整体落盘；工具错误应成为 agent 的下一步输入，而不是产生半完成文件。

## Changes

- 新增 `apply_patch` 工具，支持 `*** Begin Patch` / `*** Update File` 格式以及标准 `---` / `+++` unified diff。
- 当前每次调用限定一个工作区内的一个文件，支持多个 hunk。
- 所有 hunk 的上下文和删除行在内存中完整校验；任意 hunk 不匹配时不写文件。
- 通过临时文件和原子 rename 安装结果，并保留原文件权限。
- 复用 `edit_file` 的工作区和路径安全边界。
- 增加成功修改、标准 diff、stale patch 不落盘测试，并更新工具清单。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续仍需覆盖跨平台换行与编码、权限确认策略、git diff/回滚及更细粒度的工具审计；本轮仍只完成单文件 patch 子集。
