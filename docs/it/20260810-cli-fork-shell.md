# CLI session fork shell entry

日期：2026-08-10

## 目标

补齐交互式会话 fork 的 shell 入口。底层 session/store/TUI 已支持从 committed prefix 创建 child，但根 Cobra 命令树没有可发现的 `fork` 子命令；这使用户必须先打开 TUI 才能分支既有会话。

## 调研依据

新增 [会话分叉业界调研](../research/session-fork-research.md)。本机可观察的 Codex CLI `0.146.0` 提供 `fork [SESSION_ID] [PROMPT]`、默认 picker 和显式 `--last`；Claude Code `2.1.220` 的 `--fork-session` 在恢复时创建新 session ID。本轮采用命令显式、selector 需 opt-in 的精简子集，不虚构尚未实现的 picker 或跨 workspace filter。

## 实现

- 新增 `eino fork [SESSION_ID] [PROMPT]` 与 `eino fork --last [PROMPT]`，两者都只在 TTY 中启动 TUI。没有 picker 时，缺少 ID 和 `--last` 会在运行时初始化前失败。
- `--last` 从当前 durable storage 的最新 session 选择 source。显式 ID 与 `--last` 的参数解析互斥：使用 `--last` 时，全部位置参数都是 prompt。
- runtime 在创建 provider bundle 前读取 source 的 durable model/reasoning identity；未提供 `--model` 时 child 保持 source identity。`--model` 仍是启动期 provider override，不改写 source 或 child 的既有 durable metadata。
- fork 先调用 store 的 source-preserving committed-boundary primitive，再打开已发布 child。parent journal 不会被恢复、追加或改写；source 有 active turn、pending compaction、损坏尾部或不存在 committed boundary 时，启动直接失败，不创建 child。
- 可选启动 prompt 通过 TUI 的 `InitialPrompt` 直接进入 child 的普通 agent turn。以 `/` 开头的文字不会调用 local slash command。
- 成功启动在 stderr 写入 `forked session <child> from <parent>`；退出后的标准 resume hint 指向 child。`--yolo` 作为已有交互 TUI 开关，也允许用于 fork。

## 验证

- CLI 测试覆盖 root/help、显式 ID/`--last` 参数解析、model/yolo 到 `sessionStart` 的传递，以及无 selector 失败。
- TUI 测试覆盖启动 prompt 仅提交一次正常 turn，`/help ...` 不执行 slash parser。
- runtime 测试覆盖 fork source 的 durable model/reasoning 继承，以及 child 创建不改变 source state。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 和 `go tool golangci-lint run ./...`。
