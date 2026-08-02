# Eino 本地对话助手

这是一个基于 Eino 的本地命令行编程对话助手。第一阶段仅提供连续的、流式输出的基础对话能力：它会保留当前进程内的上下文，直到输入 `/exit`、收到 EOF 或被中断。

当前不包含命令执行、文件修改、工具调用、多会话管理或聊天记录落盘。

## 准备条件

- 可用的 Go 工具链。
- 一个支持 OpenAI Chat Completions 流式 SSE 接口的模型服务，以及其访问地址、模型名称和 API Key。

## 配置

所有应用配置均使用 YAML。先复制示例文件，再填写自己的服务信息：

```sh
cp config.example.yml config.yml
```

`config.yml` 包含密钥，已被 Git 忽略，绝不能提交、粘贴到 Issue 或输出到日志。可提交的 `config.example.yml` 只能保留占位符。

配置字段如下：

```yaml
model:
  base_url: "https://your-compatible-endpoint/v1"
  api_key: "replace-with-your-api-key"
  name: "your-model-name"
  timeout_seconds: 60

assistant:
  system_prompt: "你是一个严谨、实用的编程助手。"
```

- `model.base_url`：OpenAI 兼容服务的基础 URL。
- `model.api_key`：访问密钥，仅保存在本地 `config.yml`。
- `model.name`：服务端提供的模型名称。
- `model.timeout_seconds`：单次模型请求的超时秒数，必须为正整数。
- `assistant.system_prompt`：每轮对话携带的系统提示词。

## 运行

```sh
go run ./cmd/eino-assistant --config config.yml
```

在提示符后输入问题即可开始对话。模型回复会逐块显示；输入 `/exit` 退出。空输入不会发送请求。

## 开发与测试

```sh
go test ./...
go build ./...
```

测试使用本地伪造的 OpenAI 兼容 SSE 服务，不会读取真实 API Key 或调用真实模型端点。真实联调只使用未提交的 `config.yml`，至少验证两轮连续对话和流式显示。

## 依赖策略

新增或升级 Go 依赖时只使用 `@latest`，例如：

```sh
go get github.com/cloudwego/eino@latest
```

不要在命令、文档或源码中手写依赖版本。`go.mod` 与 `go.sum` 中由 Go 自动解析出的版本和校验和必须提交，以便每个已验收迭代可以复现。每次迭代开始时升级所需依赖到最新版本，并完整运行测试与构建验证。
