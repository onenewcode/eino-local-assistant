# 迭代：idle 两阶段 `Esc` backtrack V1

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-08-04 |
| 范围 | TUI 历史 prompt selector、source-preserving child、composer 回填与 session transient state |
| 状态 | 已交付；首个 prompt 与 workspace rollback 保持 V1 非目标 |
| 实现提交 | `92f29b7`、`697b052`、`d7cbc65`、`fadc403`、`4782072`、`65e5be9` |

## 1. 用户合同

idle 时第一次 `Esc` 进入 backtrack armed 状态，第二次 `Esc` 打开历史 prompt selector。selector
只显示已有 committed turn 前缀能够承载的历史 user prompt；`Up` / `Down` 与 `j` / `k` 移动，
`Enter` 确认选择。

确认后，TUI 在选中的 prompt 之前创建 source-preserving child。source ledger 不被修改，child
只包含选中 prompt 之前的 committed prefix；成功切换后，选中的 prompt 回填到 child composer，
但不写入 child transcript，用户可以编辑后再提交。

## 2. 状态与失败处理

- 首个 prompt 当前不可选：现有 store fork V1 不支持空 committed prefix，因此 selector 不会
  提供无法发布的边界。
- busy、compacting、pending approval 或 side question in-flight 时拒绝 backtrack；busy 的
  `Esc` 仍中断 turn，approval 的 `Esc` 仍 deny，slash menu 的 `Esc` 仍关闭菜单。
- fork 失败时 source 继续保持 active，selector 关闭，选中的 prompt 保留在 composer，并显示
  错误；已发布的 child 不因后续 TUI 切换失败而假装回滚。
- `/fork` 原有的 idle-only、无参数、从最新 committed turn 创建 child 的合同继续保留；它与
  backtrack 是两个入口，共用 source-preserving ledger primitive。
- session switch 会清理旧 side request、queue、tool/reasoning/task transient UI；旧 session
  的迟到结果不能污染新 session 的 pending 状态。

## 3. 明确边界

backtrack 是 conversation branch + prompt re-proposal，不是 destructive rewind。它不恢复或
回滚 workspace 文件、Git working tree/index、进程、网络请求、provider usage/cost、权限、
semantic memory 或其他外部系统状态；source-preserving child 也不构成 workspace checkpoint。

它不能直接等同于 OpenCode 的 Git-backed `/undo` + `/redo`，也不能等同于 Gemini CLI 的 shadow-Git
checkpoint/restore；那些产品的文件恢复合同不属于本切片。当前实现与 Codex 风格的历史分支交互
同构，但不宣称实现 Codex 的全部 rewind/checkpoint 语义。

## 4. 验证范围

backtrack focused tests 覆盖 prompt 列表与边界、键盘选择、source 保持不变、child 切换、composer
回填、fork 失败恢复、busy/approval/compacting/side 拒绝、session generation 过滤，以及旧 side
请求在 session switch 后不会影响新 session。仓库级门槛仍以 `go test ./...`、`go build ./...`、
`go tool golangci-lint run ./...` 和 `git diff --check` 为准。
