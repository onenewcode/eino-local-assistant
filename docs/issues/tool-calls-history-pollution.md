# Issue：tool_calls 历史污染导致会话永久 400

| 字段 | 内容 |
|------|------|
| 状态 | **已修复（代码） / 需验证** |
| 严重程度 | 高（一次坏 turn 后该会话无法再聊） |
| 范围 | ReAct turn 提交 + 模型历史 |
| 首次出现 | 2026-07-15 会话 `20260715-093043-b4cd62` |
| 记录日期 | 2026-07-16 |

## 现象

1. 本应调用工具的一轮（例如用户问当前时间）在 UI 上看起来“成功”了，助手只回了一句类似：“好的，我帮你看一下现在的时间！”。
2. 之后在**同一会话**里再发任何消息，都会在打开流时立刻失败：

```text
start response stream: [NodeRunError] error, status code: 400, status: 400 Bad Request, message: An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)
------------------------
node path: [chat]
```

3. **新建会话**、同一模型/配置则正常。

## 为什么新开会话没问题

| | 被污染会话 | 新会话 |
|--|------------|--------|
| 历史尾部 | `assistant(content + tool_calls)` 后直接接新的 `user` | 正常的 `user` / `assistant(最终文本)` 交替 |
| 工具结果 | 缺少对应 `tool_call_id` 的 tool 消息 | 工具跑完后，最终提交不含未闭合 tool_calls |
| 后续请求 | DeepSeek 拒绝非法 messages | 正常接受 |

新会话并不是修好了配置、密钥或模型，只是**没有继承那条非法消息**。

## 根因

OpenAI 兼容接口（含 DeepSeek）要求：

```text
带 tool_calls 的 assistant
  → 每个 tool_call_id 对应一条 tool 消息
  →（可选）继续 model/tool 循环
  → 最终纯文本 assistant
```

在「小明现在的时间」之后，应用把下面这条**中间态**当成最终 turn 提交了：

```json
{
  "role": "assistant",
  "content": "好的，我帮你看一下现在的时间！",
  "tool_calls": [
    {
      "id": "call_00_NNLIIahOCxlwyRFh1HgK9142",
      "type": "function",
      "function": { "name": "get_current_time", "arguments": "{}" }
    }
  ]
}
```

- `finish_reason` 为 `tool_calls`
- **没有**后续 `role=tool` 消息
- **没有** `tool.started` / `tool.completed` 等 journal 事件
- 却仍记为 `turn.committed` ��� 历史被永久污染

较可能的触发方式：流式响应先吐纯文本 chunk，后继 chunk 才带 `tool_calls`。若 ReAct 只根据**首个 chunk** 判断是否走 tools，可能提前 END，而拼接后的 assistant 仍保留 `tool_calls`。

当时运行时存储路径：`~/.eino-assistant/threads/<id>/journal.jsonl`（旧路径名；现已统一为 `sessions/`）。

## 证据

- 会话 id：`20260715-093043-b4cd62`（文档记录后已于 2026-07-16 删除）
- 原路径：`~/.eino-assistant/threads/20260715-093043-b4cd62/`（旧目录名）
- meta 与状态栏一致：`deepseek-v4-flash`，约 2.5k tokens，约 $0.0028
- 对照正常会话：`20260716-001526-fa5c93` — 工具完整完成，committed 的 assistant 仅为最终答案
- TUI 多行错误可能让 `message:` 看起来像空；journal 中错误正文完整

## 临时规避

- `/new` 或重新启动进入新会话
- 若支持 `/clear`：清空模型上下文到仅 system
- 不要 `/resume` 最后一条 assistant 仍带未闭合 `tool_calls` 的会话
- 可丢弃的污染会话可直接删除 `~/.eino-assistant/sessions/<id>/`

## 建议修复

1. **提交校验**：最终 assistant 仍有 `tool_calls` 且缺少对应 tool 结果时，禁止作为成功 turn 落盘。
2. **发送前消毒**：发给模型前剥离/修复悬空 `tool_calls`，让旧 journal 也能恢复。
3. **ReAct 流式 tool 判定**：跨整个 stream 检测 tool_calls，而不是只看首 chunk。
4. **错误展示**：TUI 完整展示多行上游错误。
5. **测试**：非法历史 fixture；content 后接 tool_calls 的流式 mock。

## 代码修复（2026-07-16）

1. `internal/agent/react.go`：使用全量 stream 的 `contentThenToolStreamToolCallChecker`，覆盖「先文本后 tool_calls」。
2. `internal/chat/session.go`：最终 assistant 仍含 `tool_calls` 时拒绝提交；`turnGroupMessages` 剥离悬空 `tool_calls`，且只重建完整 tool 对。
3. `internal/tui/styles.go`：`renderError` 按终端宽度 hard-wrap 长错误行。

## 验收标准

- [x] 带悬空 `tool_calls` 的 assistant 在提交前被拒绝
- [x] 发送前重建 prompt 时剥离悬空 `tool_calls`
- [x] 流式 checker：先 content 后 tool_calls 识别为需要走 tools
- [x] 错误行按宽度自动换行
- [ ] 手工：对 DeepSeek 问时间后继续 follow-up，不再永久 400

## 已排除

- 空用户输入（会在更早阶段返回本地错误）
- `ctx=0%`（短 prompt 相对大预算，属正常）
- 斜杠命令菜单（不改写对话历史）
- 全局 API key / 模型故障（同配置下新会话正常）
