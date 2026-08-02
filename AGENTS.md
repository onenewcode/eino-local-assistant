# AGENTS.md

## 编码约束

- 新增或升级依赖时使用 `go get <module>@latest`，不得手动指定语义化版本或直接编辑版本号
- 风格问题以主流聚合工具报告为准，不要在本文件硬编码单条 linter 细则

## 新功能实现（必读）

实现本仓库的新能力（CLI 子命令、TUI、会话、工具层、上下文管理、agent 循环等）时：

1. **对照成熟产品**：主动参考 **Codex CLI** 与 **Claude Code CLI** 的交互、命令模型、会话/恢复、状态展示与中断/排队等行为；优先对齐已验证的 UX 与边界处理，而不是从零臆造。
2. **查 agent 最佳实践**：对 agent 编排、工具调用、上下文裁剪、流式事件、权限/安全边界等，应检索并吸收当前主流最佳实践（官方文档、成熟开源 agent、社区共识），再落到本仓库的最小可用设计。
3. **落地原则**：可先做与参考产品同构的精简子集；命名、快捷键、斜杠命令、状态栏语义尽量可预期；偏离参考方案时在 PR/提交说明里写清理由。

参考入口（按需查阅，不要求全文镜像）：

- Claude Code：产品文档与 CLI 行为（斜杠命令、会话、权限、TUI/终端交互）
- Codex CLI：会话、resume、工具循环与终端 UX
- Agent 实践：工具 schema、ReAct/多步预算、上下文窗口策略、可观测性（usage/cost/status）

## 静态检查（仅风格）

本仓库使用 [golangci-lint](https://golangci-lint.run/) 作为主流第三方聚合入口，配置见 `.golangci.yml`。

当前**只启用风格类检查**，避免正确性/安全/复杂度规则拖慢迭代：

- formatters：`gofmt`、`goimports`
- linters：`misspell`、`whitespace`、`revive`（轻量 style 规则子集）

工具通过 `go tool` 管理：

```sh
go tool golangci-lint run ./...
```

自动修可修复的风格问题：

```sh
go tool golangci-lint run --fix ./...
```

新增或升级：

```sh
go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

若要加规则，先改 `.golangci.yml` 并保持「风格优先、少而稳」；不要默认打开 gosec/errcheck/staticcheck 全量套件。

## 测试门槛

每次可交付变更至少运行：

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
