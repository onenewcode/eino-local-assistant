# 迭代：bubblewrap sandbox capability

日期：2026-08-08

## 背景

此前 `workspace_only` 只限制 `run_command` 的启动 cwd，并不是 OS sandbox。主流 code agent 会把“审批策略”和“执行隔离”分开，因此需要一个显式、可检测的最小隔离后端，避免用户把 cwd 约束误认为文件系统或网络隔离。

## 实现

- 新增 `tools.run_command.sandbox`：`off`、`bwrap-read-only`、`bwrap-workspace-write`。
- bwrap 模式要求 `workspace_only=true`，并在工具构造时检测 `bwrap`；后端不可用时直接报错，不静默回退到宿主 shell。
- sandbox 进程只挂载必要的系统运行时目录、`/proc`、`/dev` 和临时目录；网络 namespace 默认关闭。
- `bwrap-read-only` 将工作区以只读方式挂载，`bwrap-workspace-write` 仅允许工作区写入；两者都保留既有 timeout、输出上限和进程组取消逻辑。
- 工具输出、TUI 状态栏和 `/status` 显示实际 sandbox 档位；默认 `off` 保持兼容行为。
- 增加 bwrap 可用时的读写边界测试；未安装 bwrap 的平台会跳过运行时测试，但配置不会伪装成已隔离。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
