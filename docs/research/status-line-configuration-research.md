# 状态栏配置：行业实践研究

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: coding-agent CLI 的状态栏字段选择、运行中活动提示和配色开关。
> Out of scope: 本仓库实现、provider API、模型推理内容的展示策略。

## 1. Conclusions

本次无法从已发布的一手材料确认 Codex、Claude Code 或 OpenCode 当前对“状态栏字段可选”和“配色可选”的具体交互合同。因此不把未验证的产品细节作为事实，也不根据截图推断其内部实现。

## 2. Evidence from deployed applications

截至研究日，以下一手入口被确定为相关，但当前运行环境无法完成读取：

- Codex 的官方手册入口在建立连接时失败；该失败不构成产品行为证据。
- Claude Code 的状态栏官方文档入口在连接超时前未返回正文；该失败不构成产品行为证据。
- OpenCode 的公开 TUI 状态组件源码入口同样未在受限网络中获取；未据此作出实现推断。

## 3. Mechanisms and tradeoffs

**Evidence gap:** 没有可验证的外部材料时，无法比较各产品对运行中活动、上下文使用量和颜色主题的默认项、可配置范围或持久化方式。任何此类比较都需要在可访问原始文档或固定源码提交后重新完成。

## 4. Cross-product synthesis

本次没有足够的跨产品证据形成综合结论。

## 5. Pitfalls and evidence gaps

- 不应把“状态栏存在”推导为“每个字段或颜色均可配置”。
- 不应把 TUI 截图的视觉效果推导为底层主题、持久化或工具循环的实现细节。
- 在恢复网络访问并重启已配置文档 MCP 后，应重新核验上述产品的现行文档和版本快照。

## References

- OpenAI, [Codex manual](https://developers.openai.com/codex/codex-manual.md), access attempted 2026-08-10 (unavailable in this environment).
- Anthropic, [Claude Code status line](https://code.claude.com/docs/en/statusline), access attempted 2026-08-10 (unavailable in this environment).
- OpenCode, [TUI status component](https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src/cli/cmd/tui/component), access attempted 2026-08-10 (unavailable in this environment).
