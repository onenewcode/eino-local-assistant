# 迭代：显式 plan 只读模式

日期：2026-08-08

## 背景

主流 code agent 会把“规划/审阅但不修改工作区”作为可识别的 permission mode。仓库已有 `deny` handler，能够阻止副作用，但 `deny` 更像通用安全开关，无法表达用户当前是在 plan 模式，也容易让状态栏和自动化配置失去语义。

## 实现

- 新增 `tools.permission_mode: plan`，复用 deny handler 拒绝所有 permission-gated side effect。
- 读取类工具（`read_file`、`search_files`、`list_files`、`glob_files`、`git_diff`、`git_status` 等不请求权限的工具）继续可用。
- `run_command`、文件编辑/patch、`git_restore` 和 MCP `tools/call` 会被拒绝；TUI 不会因为 plan 模式弹出确认框。
- plan 模式强制 high-risk policy 为 `deny`，防止配置中的宽松风险策略重新打开副作用路径。
- TUI 状态栏和 `/status` 显示 `approval=plan`；非交互 `exec` 接受 plan，不需要审批 UI。
- 保留 `deny` 模式兼容现有配置；`ci-readonly` preset 的行为不变。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
