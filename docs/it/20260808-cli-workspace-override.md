# CLI workspace override

本轮为 `chat`、`resume` 和 `exec` 增加 `--cd <dir>`，让一次调用可以切换项目 workspace。

## 行为

- `--cd` 临时覆盖 `tools.run_command.working_dir`，项目规则加载、文件工具和命令工具统一使用该目录。
- 未提供时继续使用配置中的 working directory。
- 覆盖只在当前进程生效，不修改配置文件；恢复已有 thread 时不重写历史 system prompt。
- 目录不存在、不是目录或无法被工具边界接受时，在启动阶段返回错误，不静默回退到原目录。
- `--cd` 与 `--model` 可同时使用，不改变权限、sandbox 或输出协议。

## 示例

```sh
eino-assistant chat --cd /path/to/project
eino-assistant resume 20260715-120000-abc123 --cd /path/to/project
eino-assistant exec --cd /path/to/project "run the tests"
```

## 验证

已覆盖三条命令的 flag 注册和旧执行入口兼容，并运行仓库规定的测试、构建和 lint 门槛。
