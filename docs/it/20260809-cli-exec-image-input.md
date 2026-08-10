# CLI exec image input

本轮为非交互 `exec` 增加可重复的 `-i <file>` / `--image <file>`，对齐 Codex CLI 将本地图片附到 initial prompt 的 multimodal 工作流。图片不是一次性的 provider 参数，而是 user message 的正式内容，因此 session resume、fork、context planning 和 transcript replay 都能保留附件语义。

## 行为

- 支持 PNG、JPEG、GIF 与 WebP；按文件实际内容检测 MIME type，不信任扩展名。
- 单次 prompt 最多 8 张、原始数据总计最多 20 MiB。空路径、目录、非图片和超限输入在创建 session 或请求 provider 前失败。
- 图片在本地读取并编码到 Eino `UserInputMultiContent`，OpenAI-compatible provider 将其转换为 `data:<mime>;base64,...` image URL；不会为了附件启动本地 HTTP server 或把路径暴露给远端。
- multipart user message 作为 `turn.committed` 的原始消息持久化。后续 resume/fork 仍能把图片发送给模型；TUI replay 显示原始文字与附件数量，输入历史只恢复文字 prompt。
- Session 新增 multimodal user-message 入口，仍复用既有 CAS、失败/取消、tool event、usage 和 compaction 生命周期；纯文本 `Ask` API 行为不变。
- 本地 token fallback 对每张图片保守计入 1024 tokens，并继续优先采用 provider 返回的真实 usage。

## 验证

测试覆盖图片类型、base64 内容、数量/大小/regular-file 限制、CLI help、multimodal token 估算、Session provider view 与 durable transcript。真实 OpenAI-compatible SSE 回归还捕获请求 JSON，验证 text + image URL parts 到达 provider，并从 thread store 重新加载带 base64 image part 的 user message。TUI 测试确认恢复时显示 prompt 与附件数量，且输入历史不包含 base64 数据。
