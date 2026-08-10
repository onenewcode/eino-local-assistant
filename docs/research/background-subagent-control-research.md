# 后台子 agent 的启动、监督与隔离：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-11；产品和实现会演进，采用前应再次核验。
>
> 范围：coding-agent CLI 将独立分析/执行任务放到后台时的启动、身份、资源/权限继承、状态检查、取消与结果回收边界。
>
> 不在范围：各产品未公开的调度队列、远端执行基础设施、计费聚合、session 文件格式，以及任何本仓库映射。

## 1. 结论

- **综合：** 后台 agent 必须有稳定任务身份和可查询状态，不能只在父 agent 的流式文本里隐式出现。启动、活跃、完成/失败/取消是至少需要向用户区分的生命周期。
- **综合：** 子 agent 的任务输入、工具与权限应是显式的、缩小的快照；它不应因为父会话能编辑或升级权限而自动得到相同能力。对第一个实现，纯分析、无工具的子 agent 是比“继承全部写权限”更安全的起点。
- **综合：** 子 agent 的结果应作为带来源的观察显式交付或由父 agent 显式获取，而非后台完成后悄悄写入父会话上下文。取消必须停止子任务，但不能误取消父 turn 或其他子任务。

## 2. 已部署应用证据

### Claude Code：显式后台启动与 agent 视图

**事实（本机已部署产品观察）：** Claude Code `2.1.220` 的 `claude --help` 提供 `--bg, --background`，描述为“Start the session as a background agent and return immediately (manage with `claude agents`)”；同一帮助提供 `--agents`（定义 custom agents）、`--agent`、`--allowed-tools`、`--disallowed-tools`、`--permission-mode` 与 `--effort`。观察日期：2026-08-11。

**事实（本机已部署产品观察）：** `claude agents --help` 将它描述为“Manage background agents”，支持 `--json` 输出 active sessions，`--all` 同时包含 completed sessions，并可为 dispatched sessions 指定 cwd、additional dirs、model、permission mode、MCP config、plugins、settings 和 effort。观察日期：2026-08-11。该帮助没有公开本地取消协议、结果注入时机或进程崩溃后的恢复细节；不作推断。官方入口为 [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)。

### Codex CLI：稳定 multi-agent 能力，但公开 CLI help 不泄露调度细节

**事实（本机已部署产品观察）：** Codex CLI `0.146.0` 的 `codex features list` 将 `multi_agent` 列为 `stable true`。`codex --help` 本身未把本地子 agent 命令列为顶层 command。观察日期：2026-08-11。这证明已部署产品包含稳定 multi-agent feature flag，但不足以推断 task schema、并发策略、权限继承或结果合流方式。公开入口为 [Codex documentation](https://developers.openai.com/codex/) 与 [openai/codex](https://github.com/openai/codex)。

### OpenCode：角色、subagent mode、权限和 step budget 分离配置

**事实（公开文档）：** OpenCode 的 [Agents documentation](https://opencode.ai/docs/agents/) 将 agent 定义为可配置角色；文档展示 `mode: subagent` 以及 `permission` 中单独拒绝 `edit`/`bash` 的示例。它也记录每个 agent 的 `steps` 上限：达到上限时要求 agent 总结工作和剩余建议。访问日期：2026-08-11。文档没有在本次证据中证明后台状态列表、取消 API 或父会话结果注入顺序。

## 3. 机制与取舍

| 机制 | 用户可见效果 | 主要取舍 |
| --- | --- | --- |
| 启动立即返回 ID | 前台可继续工作，状态可单独追踪 | 需要 list/status surface，不能把 ID 隐藏在日志中 |
| 状态机（working/completed/failed/cancelled） | 用户能区分仍在运行、可消费结果与终止原因 | 需要处理完成和取消的竞态 |
| 每个任务独立 cancellation context | 取消只影响目标 task | 必须在 parent/process shutdown 时一起释放 |
| 只读、无工具 child | 不会意外改工作区或触发 approval | 能力较窄；后续需有专门的权限/沙箱模型 |
| 结果显式显示或显式获取 | 来源与影响范围清楚 | 父 agent 不会自动获知，需用户或协调器主动使用 |
| 并发数量与输出上限 | 成本、资源和 UI 保持可预期 | 过低会排队，过高会争抢模型/网络配额 |

## 4. 跨产品综合

**综合：** 一个可控的后台子 agent 生命周期至少有以下阶段：

1. 以用户或父 agent 可见的任务文本创建 child，并立即返回稳定 ID；
2. 为 child 捕获最小、标明为 reference 的上下文与独立权限/工具集合；
3. 在 parent 可继续操作时发布 working 状态；
4. 在 completed、failed 或 cancelled 后保留有限结果/错误供查看；
5. 只有明确的用户或协调器动作，才把结果带入后续父会话工作。

这与“在同一 turn 中调用一次辅助模型”不同：后台 child 需要独立身份、取消域和可观测状态。也与完整 worktree-isolated 编码 worker 不同：后者还需要 Git worktree、文件冲突、工具审批、资源配额与跨进程恢复协议。

## 5. 风险与证据缺口

- **风险：** 把父 agent 全部工具、session allow 和 yolo 旁路隐式复制给 child，会让一次只读委派扩大为后台写入或外部副作用。
- **风险：** 后台结果如果在未经确认时直接追加到父模型上下文，可能造成 prompt injection、过期观察或难以审计的决策来源。
- **风险：** 使用一个进程级 cancel 或共享 turn deadline 会使取消一个 child 意外中止主会话；任务必须有独立 cancel handle，同时受进程退出约束。
- **证据缺口：** 本次直接观察无法确认 Codex stable multi-agent feature 的公开控制面；不将 feature flag 当作实现行为证据。
- **证据缺口：** Claude `agents --help` 暴露了查询与 dispatched-session options，但没有公开 task persistence、cancel protocol 或 completed-result retention 语义；版本升级后应重新核验。

## References

- Claude Code `2.1.220`: `claude --help`、`claude agents --help`，本机观察于 2026-08-11；[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)。
- Codex CLI `0.146.0`: `codex --help`、`codex features list`，本机观察于 2026-08-11；[Codex documentation](https://developers.openai.com/codex/)；[openai/codex](https://github.com/openai/codex)。
- OpenCode: [Agents documentation](https://opencode.ai/docs/agents/)，访问于 2026-08-11。
