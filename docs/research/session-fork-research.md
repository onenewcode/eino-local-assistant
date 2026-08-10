# Agent CLI 会话分叉：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-10；已于 2026-08-11 本机重新核验。产品和实现会演进，采用前应再次核验。
>
> 范围：已部署 coding-agent CLI 从已有交互会话创建独立分支时的 selector、首条输入、持久化与失败边界。
>
> 不在范围：各产品未公开的 session 文件格式、服务端同步、TUI picker 实现，以及任何本仓库映射。

## 1. 结论

- **综合：** 分叉应是新的持久化会话，而非给原会话增加可撤销指针。用户需要在启动前明确 source，启动后的首条 prompt 只写入 child。
- **综合：** CLI 应把 source selector 与 prompt 分开：显式 session ID 给稳定身份，`--last` 是显式接受“最新”这一易变选择的快捷方式；没有 selector 时，交互 picker 是常见选择。
- **综合：** 以 `/` 开头的 shell positional prompt 不应意外执行 TUI local command。它是新 child 的正常用户消息，而不是一个隐式控制平面。
- **综合：** source 不能在 fork 过程中被恢复、重写或追加。若 source 正在执行或没有稳定 committed boundary，失败比猜测边界更安全；child 已发布但随后 UI 打开失败时，明确告知 child ID 比试图反向删除更可恢复。

## 2. 已部署应用证据

### Codex CLI：可选 ID 与 prompt，省略 ID 时使用 picker 或 `--last`

**事实（本机已部署产品观察）：** Codex CLI `0.146.0` 的 `codex fork --help` 报告用法为 `codex fork [OPTIONS] [SESSION_ID] [PROMPT]`。它说明省略 ID 时默认打开 picker，`--last` 绕过 picker 选择最近会话，`--all` 禁用 cwd filtering。`codex resume --help` 使用同一组 session selector 和可选 prompt。观察日期：2026-08-10，并于 2026-08-11 重新核验；官方入口为 [Codex CLI reference](https://developers.openai.com/codex/cli/reference/)，该环境对该网页返回 HTTP 403，因此此处不从网页内容推断额外行为。

**事实（本机已部署产品观察）：** `codex --help` 将 `fork`、`resume`、`archive`、`delete` 与 `unarchive` 并列为 session lifecycle commands，表明 fork 是 shell 层可发现的会话操作，而非仅 TUI 内动作。观察日期：2026-08-10；公开项目为 [openai/codex](https://github.com/openai/codex)。

### Claude Code：恢复时显式切换到新的 session ID

**事实（本机已部署产品观察）：** Claude Code `2.1.220` 的 `claude --help` 将 `--fork-session` 描述为“when resuming, create a new session ID instead of reusing the original”，并要求配合 `--resume` 或 `--continue`。同一帮助还说明 `--continue` 选择当前目录最近会话，`--resume` 接受 session ID 或打开交互 picker。观察日期：2026-08-10，并于 2026-08-11 重新核验；官方入口为 [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)，该环境连接被重置，因此此处不从网页内容推断未在 CLI help 中出现的行为。

## 3. 机制与取舍

| 机制 | 用户可见效果 | 主要取舍 |
| --- | --- | --- |
| 显式 ID | 精确指定 parent，脚本和事故排查可复现 | 用户需要先列出或记住 ID |
| picker | 可发现、少记 ID | 需要 TTY；自动化和非交互路径不能依赖它 |
| `--last` | 少一次列表查询，适合连续工作 | “最新”随并发会话和工作目录变化，必须显式 opt-in |
| fork-on-resume flag | 保持单一 resume command | selector、prompt、恢复与 child identity 的组合更难解释 |
| 独立 `fork` command | shell help 明确表达“不会写 parent” | 命令树增加一个可维护 surface |

**综合：** Codex 选择独立 `fork` command，Claude Code 把分叉放入 resume flag；两者共同的稳定边界是“目标为新的 session ID”。因此产品不必复制其中一套命令树，但不能把 fork 伪装成普通 resume。

## 4. 跨产品综合

**综合：** 一个可靠的 fork 流程可分为四个阶段：

1. 解析一个 source selector，并在无法选定 source 时停止；
2. 在 source 的可验证 committed boundary 创建 child 与 provenance；
3. 打开 child，以 child identity 而不是 source identity 进入 UI；
4. 如提供首条 prompt，只向 child 调度一个正常 agent turn。

阶段 2 与 3 之间存在不可完全回滚的持久化边界。已发布 child 是有效的可恢复工作，因此 open 失败的正确用户体验是清晰报告错误和 child identity，而不是尝试删除可能已被其他进程看到的 journal。

## 5. 风险与证据缺口

- **证据缺口：** 当前环境未安装 OpenCode，且其公开文档站点无法在本次访问中建立可核验连接；不对 OpenCode 的 fork UX 作事实断言。
- **证据缺口：** 本次能直接核验的是已部署 CLI help，而不是实际运行到 picker 或远程同步场景；picker 搜索、取消和并发 session 排序仍应在版本升级后重新观察。
- **风险：** 将 positional `/...` 送进 local slash parser 会把来自 shell 的任务意图变成 UI 管理操作，可能没有任何模型调用或 child turn。
- **风险：** “latest” 若未说明 scope，会在并行 terminal、归档 session 或跨项目存储下产生难以解释的 parent 选择。

## References

- Codex CLI `0.146.0`: `codex --help`, `codex resume --help`, `codex fork --help`，本机观察于 2026-08-10，并于 2026-08-11 重核；[Codex CLI reference](https://developers.openai.com/codex/cli/reference/)。
- Claude Code `2.1.220`: `claude --help`，本机观察于 2026-08-10，并于 2026-08-11 重核；[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)。
- OpenCode: [sessions documentation](https://opencode.ai/docs/sessions/)，本次访问未能核验，记录为证据缺口而非行为来源。
