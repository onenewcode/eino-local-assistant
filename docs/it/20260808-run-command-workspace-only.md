# run_command workspace_only cwd 约束

## 背景

研究文档要求区分 workspace 范围与 OS sandbox。此前 `run_command` 只解析默认 cwd，模型可以把 `working_dir` 指向工作区外；同时 shell 本身没有文件系统或网络隔离。

## 本轮变更

- 新增 `tools.run_command.workspace_only`。
- 开启后，以配置的 `working_dir`（或进程 cwd）作为 workspace root，拒绝工具启动 cwd 在 root 外的请求。
- `RunCommandOutput` 返回 `workspace_only`，便于 agent 和审计日志了解当前约束。
- `personal-dev` 与 `ci-readonly` preset 自动开启该选项。

## 明确边界

这不是 OS sandbox：`sh -c` 内部仍可执行 `cd`、访问绝对路径和网络。README 与配置示例明确标注这一点；真正的 read-only/workspace-write sandbox 仍需 bwrap、容器或平台后端支持。
