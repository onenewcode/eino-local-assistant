# CLI permission mode override

本轮为 `chat`、`resume` 和 `exec` 增加单次 `--permission-mode` 覆盖，对齐主流 code agent 在启动时选择审批/只读模式的能力。

## 行为

- 接受 `unrestricted`、`confirm`、`plan` 和 `deny`，沿用现有 permission handler 语义。
- CLI 显式覆盖在 permission profile 展开后生效，因此用户本次选择不会被配置 preset 静默覆盖。
- `chat` / `resume` 可使用 `confirm` 并进入现有 TUI 审批流程。
- 非交互 `exec` 明确拒绝 `confirm`，不会因为没有 TTY 而自动放行。
- `plan` 继续强制 high-risk deny；该 flag 不修改配置或历史 thread。

## 验证

已覆盖三条命令的 flag/help、exec confirm 拒绝和非法模式拒绝，并运行仓库规定的测试、构建和 lint 门槛。
