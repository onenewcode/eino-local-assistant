# CLI model override

本轮为 `chat`、`resume` 和 `exec` 增加单次 `--model` 覆盖，贴近主流 code agent 的模型切换体验。

## 行为

- `--model <name>` 只替换本次 provider 创建使用的模型名，配置文件保持不变。
- `resume --model` 只影响本次恢复进程使用的模型；历史 thread 的 system prompt、事件账本和原有内容不被改写。
- 未提供 `--model` 时继续使用配置中的 `model.name`，保持兼容。
- `exec` 的 text/json/jsonl 输出协议不变；该 flag 不改变权限、sandbox 或工具注册。

## 示例

```sh
eino-assistant chat --model coding-model
eino-assistant resume 20260715-120000-abc123 --model fast-model
eino-assistant exec --model coding-model "run the tests"
```

## 验证

已覆盖三条命令的 flag 注册和既有执行入口兼容，并运行仓库规定的测试、构建和 lint 门槛。
