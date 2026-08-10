# CLI high-risk policy override

本轮为 `chat`、`resume` 和 `exec` 增加单次 `--high-risk-policy` 覆盖，补齐运行时风险策略控制。

## 行为

- 接受 `advisory`、`confirm` 和 `deny`，沿用现有风险分类与 permission handler。
- CLI 显式覆盖在 permission profile 展开后生效，不修改配置文件。
- `chat` / `resume` 的 `confirm` 只对 high-risk 请求进入现有 TUI 审批流程。
- 非交互 `exec` 拒绝 `confirm`，不会因缺少审批界面而自动放行。
- `plan` permission mode 仍是硬上限，会强制 high-risk deny。

## 验证

已覆盖三条命令的 flag/help、exec confirm 拒绝和非法 risk policy 拒绝，并运行仓库规定的测试、构建和 lint 门槛。
