# 迭代：run_command 硬权限（P1）

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-07-17 |
| 分支 | `feat/run-command-permissions` |
| 状态 | **已交付**（本迭代闭环） |
| 调研依据 | [cli-command-permissions-research.md](../research/cli-command-permissions-research.md) Phase P1；[cli-rules-research.md](../research/cli-rules-research.md) §6 / Phase 2 |
| 产品说明 | [command-policy.md](../command-policy.md) |

## 1. 目标

为 AI 调用 `run_command`（`sh -c`）增加**硬权限闭环**：策略引擎强制 `deny | ask | allow`，TUI 审批（once / session / deny），拒绝以软 tool result 回模型，权限状态可见。

**非目标（本迭代）**：AGENTS.md 软规则加载、OS 沙箱、always 落盘、会话模式切换（plan/auto/yolo）、复杂 shell 全解析。

## 2. 产品决策

| 项 | 选择 |
| --- | --- |
| 默认姿态 | `profile: cautious` + `approval: on_request` |
| Allow 集合 | 极小 L0（`pwd`/`ls`/`git status|diff|log`/`rg`/`grep`） |
| 审批 | once / session / deny（无 always 写盘） |
| 工作区 | `workspace_only: true`（路径 symlink 规范化） |
| 逃生口 | `approval: never`（硬 deny 仍生效） |

**行为变更**：从「默认无审批执行」变为「默认询问」。

## 3. 交付内容

### 3.1 策略与执行

- `internal/tools/policy*.go`：deny > ask > allow；YAML prefix/regex
- **opaque-shell**：含 `&&` `||` `;` `|` `$()` 等元字符时，allow **降级为 ask**（防 `ls && payload`）
- `internal/tools/command.go`：授权先于 command timeout；soft deny + `decision`/`reason`/`stop_retrying`
- `DenyStreak`：同 rule_key 连续 deny ≥3 → `stop_retrying` 提示（抑制盲 R2）
- 稳定 reason：`policy_denied` / `user_denied` / `approval_timed_out` / `approval_cancelled` / …

### 3.2 审批 UX

- `internal/tui/approval.go`：Bridge 串行化、request id、modal（1/2/3、Esc deny）
- 状态栏 `cmd=ask|auto`
- `/permissions`（`/policy`）只读视图

### 3.3 配置

```yaml
tools:
  run_command:
    approval: on_request   # never | on_request
    workspace_only: true
    workspace_root: ""
    profile: cautious
    policy: []             # 可选附加规则
```

见 `config.example.toml`。

### 3.4 文档

| 文档 | 角色 |
| --- | --- |
| `docs/command-policy.md` | 用户/运维：策略、审批、重试语义、局限 |
| `README.md` | 入口说明 + 配置片段 |
| 本文 | 迭代记录与验收 |
| 调研文附录/阶段标记 | 标明 P1 已实现，避免「现状」过期 |

## 4. 重试语义（本迭代约定）

| 层 | 行为 |
| --- | --- |
| R1 同参自动重试 | **不做**（shell 非幂等） |
| R2 模型改参再调 | soft deny 启用；受 `MaxStep` 约束 |
| 用户 deny | 立刻 `stop_retrying` + 禁止等价绕过文案；同 rule_key 写入 session deny（不再弹窗） |
| 连续同前缀 policy deny | `stop_retrying` 提示（≥3） |

## 5. 验收

- [x] 默认 cautious + on_request
- [x] 硬 deny / L0 allow / 默认 ask
- [x] 复合命令不因 L0 前缀 auto-allow
- [x] once / session / deny + 状态栏 + `/permissions`
- [x] soft deny 不崩 ReAct；reason 可观测
- [x] `go test ./...` · `go build ./...` · `go tool golangci-lint run ./...`

## 6. 已知局限（诚实边界）

1. 无 OS 沙箱；allow 命令仍是完整用户权限。
2. 字符串/前缀策略可被复杂 shell 绕过；元字符降级是防呆不是边界。
3. 无 always 持久化、无项目 policy 自动加载。
4. Journal 未单独记 `policy.changed` 事件（决策在 tool result 内可见）。

## 7. 后续候选（P2+）

- `personal-dev` 预设（扩大 allow，如 `go test`）
- Always 写入 `~/.eino-assistant/policy`
- `/permissions auto|plan` 会话模式
- 简单 `&&`/`|` 拆分后分别评估
- OS sandbox（bwrap / Seatbelt）
- 软规则 loader（AGENTS.md）— 见 cli-rules Phase 1

## 8. 关键路径

| 路径 | 说明 |
| --- | --- |
| `internal/tools/policy.go` | 引擎 + opaque-shell |
| `internal/tools/policy_builtin.go` | cautious 内置 |
| `internal/tools/approval.go` | Approver / session / deny streak |
| `internal/tools/command.go` | 授权接入 |
| `internal/tools/workspace.go` | workspace clamp |
| `internal/tui/approval.go` | Bridge + modal |
| `internal/config/config.go` | 配置字段 |
| `cmd/eino-assistant/run_tui.go` | 装配 |
| `docs/command-policy.md` | 产品说明 |
