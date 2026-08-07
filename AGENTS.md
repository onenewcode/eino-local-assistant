# AGENTS.md


## 架构与包边界（必读）

完整地图见 [docs/architecture.md](docs/architecture.md)。改功能前先对表，避免拆错包或重复造层。

| 关注点 | 包 / 位置 | 说明 |
| --- | --- | --- |
| ReAct、system prompt、**AGENTS.md 加载** | `internal/agent` | `project_instructions.go` 读本文件；**不要**再引入 `internal/rules` |
| 跨会话语义记忆 | `internal/memory` | `.eino/memory/`、`/memory`、candidate；≠ `/resume` |
| 会话账本 / resume / compact | `internal/store` + `internal/chat` | 单一 JSONL 账本；checkpoint 与 artifact 为事件 payload |
| 进程日志 / slog 可观测 | `internal/logging` | 运行时 JSONL 文件（默认 `<data_dir>/logs`）；≠ 会话账本；见 [docs/logging.md](docs/logging.md) |
| 工具实现 | `internal/tools` | shell、apply_patch、memory_* 只读等 |
| 硬权限 / 沙箱 | `permissions` 配置 + `sandbox` | **勿**只靠本文件写「禁止 rm」当执行保证 |
| TUI / 斜杠 | `internal/tui` | |
| 配置 | `internal/config` | `[rules]` = 是否注入 AGENTS；`[memory]` = 语义记忆；`[logging]` = 进程日志 |

## 编码约束

- 新增或升级依赖时使用 `go get <module>@latest`，不得手动指定语义化版本或直接编辑版本号
- 风格问题以主流聚合工具报告为准，不要在本文件硬编码单条 linter 细则
- 新增顶层 `internal/<pkg>` 前确认：是否已有归属包能放下；薄加载器优先挂在消费方（如 AGENTS 加载挂在 `agent`），避免为单文件再开易撞名包

## 新功能实现（必读）

实现本仓库的新能力（CLI 子命令、TUI、会话、工具层、上下文管理、agent 循环、记忆等）时：

1. **对照成熟产品**：主动参考 **Codex CLI** 与 **Claude Code CLI** 的交互、命令模型、会话/恢复、状态展示与中断/排队等行为；优先对齐已验证的 UX 与边界处理，而不是从零臆造。
2. **查 agent 最佳实践**：对 agent 编排、工具调用、上下文裁剪、流式事件、权限/安全边界、规则与记忆分层等，应检索并吸收当前主流最佳实践，再落到本仓库的最小可用设计。
3. **落地原则**：可先做与参考产品同构的精简子集；命名、快捷键、斜杠命令、状态栏语义尽量可预期；偏离参考方案时在 PR/提交说明里写清理由。
4. **改包边界或持久分层时**：同步更新 [docs/architecture.md](docs/architecture.md) 与相关专题文档。

参考入口（按需查阅，不要求全文镜像）：

- Claude Code：产品文档与 CLI 行为（斜杠命令、会话、权限、TUI/终端交互、memory）
- Codex CLI：会话、resume、工具循环、AGENTS.md、local memories
- Agent 实践：工具 schema、ReAct/多步预算、上下文窗口策略、可观测性（usage/cost/status）
- 本仓库地图：`docs/architecture.md`

## 静态检查（仅风格）

本仓库使用 [golangci-lint](https://golangci-lint.run/) 作为主流第三方聚合入口，配置见 `.golangci.yml`。

工具通过 `go tool` 管理：

```sh
go tool golangci-lint run ./...
```

自动修可修复的风格问题：

```sh
go tool golangci-lint run --fix ./...
```


若要加规则，先改 `.golangci.yml` 并保持「风格优先、少而稳」；不要默认打开 gosec/errcheck/staticcheck 全量套件。

## 测试门槛

每次可交付变更至少运行：

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
