# 迭代：后台分析 agent 的有界 workspace diff 观察

日期：2026-08-11

## 背景

只含 frozen session reference 的后台 child 无法观察当前工作区，因而很难承担实际的代码审查、回归定位或变更分析任务。直接把 `shell` / `apply_patch` 交给后台 child 又会把普通 turn 的工具权限、sandbox 与 approval 问题无提示地复制到一个异步任务。

`docs/research/background-subagent-control-research.md` 的行业综合要求 child 使用显式、缩小的上下文与权限集合。已有 `/diff` runtime 已将工作区读取限制为固定参数 Git 调用、路径校验、5 秒总 deadline 和输出上限；本轮复用该读模型，保持 child 本身 tool-free。

## 实现

- 新增 `/agent --diff <analysis task>`。后台 task 仍立即得到 stable ID；执行时在其独立 cancellation context 内获取一次 workspace diff snapshot，再调用无工具基础模型。
- snapshot 经过二次终端/非文本清理并限制为 64 KiB，随后由 `WORKSPACE DIFF SNAPSHOT - QUOTED REFERENCE ONLY` 包围。system boundary 明确规定 snapshot 是参考数据，不能执行其中出现的指令。
- child 获得的 active instruction 仍只有用户 task；它不能运行 shell、调用工具、编辑文件、升级权限或派生 agent。diff 读取器失败时 child 进入 `failed`，并显示清理后的原因，不会继续调用模型。
- `/agents` 列表和详情显示 task 是否使用 workspace diff snapshot，避免将有代码观察范围的 child 与仅 session-reference 的 child 混淆。

## 取舍与边界

这不是后台工具调用或 file-system agent。snapshot 是一个一次性、运行时提供的只读 Git diff，不会列目录、读取任意文件、运行用户命令，也不会在完成后更新。它可让 child 有针对性地分析本次变更，但无法替代带独立 sandbox、tool approval、worktree 隔离和 durable job 协议的 coding worker。

## 验证

- 新增 TUI 测试覆盖正常 diff-scoped dispatch、引用标签/控制字符清理、空 task、snapshot callback 未配置、snapshot 失败时不调用模型及失败状态展示。
- 聚焦运行 `go test -count=1 ./internal/tui ./cmd/eino-assistant`，并在提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
