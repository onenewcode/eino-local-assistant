# CLI sandbox override

本轮为 `chat`、`resume` 和 `exec` 增加单次 sandbox/workspace boundary 覆盖，与 permission mode 保持正交。

## 行为

- `--sandbox` 接受 `off`、`bwrap-read-only` 和 `bwrap-workspace-write`。
- `--workspace-only` 将工具工作目录限制到本次选择的 workspace；它只能收紧，不能通过 flag 关闭配置已有边界。
- bwrap 模式必须同时具备 `workspace_only=true`，非法组合在 provider 初始化前失败。
- sandbox backend 不可用时沿用工具构建阶段的明确错误，不会静默降级到 `off`。
- CLI 覆盖在 permission profile 展开后生效，不修改配置文件或历史 thread。

## 示例

```sh
eino-assistant chat --workspace-only --sandbox bwrap-workspace-write
eino-assistant exec --permission-mode plan --workspace-only --sandbox bwrap-read-only "inspect the project"
```

## 验证

已覆盖三条命令的 flag/help、非法 sandbox 和缺失 workspace boundary 的前置拒绝，并运行仓库规定的测试、构建和 lint 门槛。
