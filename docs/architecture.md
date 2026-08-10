# 架构地图

本项目是一个本地交互式编程助手。包边界以“谁拥有状态、谁负责持久化、谁展示给用户”为准；不要为了单个入口新建平行层。

## 运行路径

```text
cmd/eino-assistant
  -> internal/config              用户级 TOML 与启动配置
  -> internal/agent               system prompt、ReAct 与项目指令组合
  -> internal/chat                会话模型、turn 生命周期与上下文工作集
  -> internal/tools + sandbox     工具实现、授权决策与 OS 隔离
  -> internal/store               JSONL 账本、artifact、checkpoint、resume 投影
  -> internal/tui                 Bubble Tea 交互、斜杠命令与状态展示
```

`internal/logging` 是独立的进程诊断通道；它不参与会话恢复，也不替代账本。

## 包边界

| 关注点 | 主包 / 位置 | 规则 |
| --- | --- | --- |
| ReAct、system prompt、项目 `AGENTS.md` 加载 | `internal/agent` | 项目软指令在此加载和组合；不要引入平行的 `internal/rules`。 |
| 跨会话语义记忆 | `internal/memory` | 保存 `.eino/memory/` 的用户事实与 candidate；这不是 `/resume`。 |
| 会话账本、恢复、压缩 | `internal/store` + `internal/chat` | JSONL 账本是真源；checkpoint 与 artifact 作为事件 payload 保存。 |
| 进程日志与 `slog` 可观测性 | `internal/logging` | 默认写入 `<data_dir>/logs`，仅用于运行诊断。 |
| 工具实现 | `internal/tools` | `shell`、`apply_patch`、memory 只读工具及其运行时护栏归属这里。 |
| 硬权限与沙箱 | `approval_policy`、rules + `internal/sandbox` | 软指令不是执行保证；隔离与路径保护必须在运行时生效。 |
| TUI、斜杠命令、状态栏 | `internal/tui` | 只持有 UI 状态和展示投影，不定义会话账本格式。 |
| 配置 | `internal/config` | `[rules]` 控制 AGENTS 注入，`[memory]` 控制语义记忆，`[logging]` 控制进程日志，`[ui]` 保存状态栏字段。 |

## 状态与持久化

```text
用户输入
  -> TUI 选择命令或开始 turn
  -> agent/chat 执行模型与工具循环
  -> store 追加会话事件和 usage/artifact/checkpoint
  -> TUI 读取会话投影并渲染

logging 旁路记录进程诊断，不回流到会话账本。
memory 仅在新会话的 prompt 组合阶段进入上下文，不改写既有账本。
```

## 修改指南

- 新的持久会话数据先判断是否应成为 `internal/store` 事件，再由 `internal/chat` 管理其工作集投影。
- 新工具放入 `internal/tools`，并通过既有权限、sandbox 与 runtimeguard 边界执行；不要让 TUI 直接构造 shell 输入。
- 新 TUI 交互放入 `internal/tui`，回调应返回稳定 DTO，不泄漏 store 或 provider 内部实现。
- 新配置放入 `internal/config` 并由启动层传入消费包；若改变本图中的边界或持久化分层，同步更新本文件与相关专题文档。
