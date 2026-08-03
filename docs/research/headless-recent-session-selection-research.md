# 非交互式最近会话选择：业界实践

> 状态：研究笔记，不是本仓库的实施计划。
>
> 调研日期：2026-08-03。CLI 行为和会话存储会随版本改变；采用结论前应重新核验
> 引用页面和本机命令帮助。
>
> 范围：coding CLI 在非交互启动中，通过显式 opt-in 的 `last`、`continue`、
> `latest` 或列表序号来选择一个已有会话。考察选择范围、最近的含义、活跃会话和
> 并发风险。
>
> 不在范围：本仓库实现、私有 transcript 格式、厂商未发布的持久化算法、工具副作用
> 回滚或不同 CLI 会话 ID 的互操作性。

## 1. 结论

- **跨产品综合。** 三个产品都让“最近会话”成为用户显式选择的快捷路径：Codex
  `exec resume --last`、Claude `-p --continue` 和 Gemini `--resume latest`。它们同时
  提供指定会话的路径，因此最近选择器不是 durable identity 的替代品。 [O1][C1][G1]
- **跨产品综合。** 最近选择的候选范围并不统一：Codex 的 help 只公开 `--all` 会
  关闭 cwd filtering，Claude 的 `--continue` 限当前目录，Gemini 的 session list 限当前
  project。范围差异足以让同一“最近”请求指向不同的会话，不能投射成共同规则。
  [O1][C1][G1]
- **证据缺口。** 三家都没有在引用资料中规定“最近”的时间字段、相同时间的 tie-break、
  选择发生时是否冻结候选集，或自动选择是否排除活跃会话。`newest`、`most recent` 和
  `latest` 只证明按某种近期性选择，不构成可重放排序契约。 [O1][C1][G1]
- **文档化风险。** Claude 明确说明：不 fork 而在两个 terminal resume 同一 session 时，
  消息会交织进同一 transcript。Codex 和 Gemini 的引用资料未发布对应的 single-writer、
  排队或拒绝语义；不能把未披露误读成并发安全。 [C2][O1][G1]
- **产品中立结论。** 安全的第一版非交互 CLI 应省略自动“最近会话”选择；只接受调用方
  已持有的明确会话标识。等能发布 selection scope、全序、活跃会话策略及 single-writer
  行为后，再以独立、显式 opt-in 的 selector 添加此便捷功能。这是证据驱动的通用建议，
  不是对任何本地代码的映射。

## 2. 已发布产品的证据

| 产品 | 非交互的显式选择器 | 范围的直接证据 | 最近/序号的直接证据 |
| --- | --- | --- | --- |
| Codex CLI 0.146.0 | `codex exec resume --last`；省略 session ID 时必须给 `--last`。 | `--all` 的说明为“Show all sessions (disables cwd filtering)”。help 没有说明该开关是否也改变 `--last` 的候选集。 | `--last` 选择“most recent recorded session (newest)”。可指定 UUID 或 thread name。 [O1] |
| Claude Code | 官方 headless 文档说 `-p` 可搭配所有 CLI options，并直接示例 `claude -p ... --continue`。 | sessions 文档将 `--continue` 定义为“current directory”中最近的 session；指定 ID 的查找范围则是当前 project directory 及其 git worktrees。 | headless 文档称 `--continue` 为最近 conversation，并建议多 conversation 时捕获 `session_id` 后传给 `--resume`。 [C1][C2] |
| Gemini CLI | reference 列出 `--resume "latest"` 和 `--resume 5`；`--prompt` 会强制非交互模式，但引用资料没有给出二者组合的专门示例。 | `--list-sessions` 仅列“current project”的可用 sessions。 | `latest` 为 most recent，数字参数是 index；reference 还示例以 session ID 恢复。 [G1] |

### 2.1 Codex CLI 0.146.0

**文档化事实（版本化本机观察）。** 2026-08-03 在研究机执行
`codex --version` 和 `codex exec resume --help`，版本为 `codex-cli 0.146.0`。
`resume` 的位置参数是 session ID（UUID 或 thread name）和恢复后发送的 prompt；省略
ID 时，help 要求 `--last`。该 flag 选择 “most recent recorded session (newest)”，并且
`--all` 显示所有 sessions、关闭 cwd filtering。 [O1]

**证据边界。** 这份 help 没有定义 recorded 的写入时机、“newest”比较的字段或同值
tie-break；也没有说 `--last` 会不会选到正在运行的会话，或 `--all` 对 `--last` 而非
picker 的具体作用。它同样没有发布同 ID 并发 resume 的锁、排队、拒绝或合并语义。 [O1]

### 2.2 Claude Code

**文档化事实。** 官方 headless 文档将 `-p` 定义为非交互模式，并明确说所有 CLI
options 都可与之使用，其中包括 `--continue`。同页示例连续运行两个
`claude -p ... --continue`，并在“multiple conversations”场景下转而捕获 JSON 的
`session_id`、以 `--resume "$session_id"` 继续。 [C1]

**文档化事实。** sessions 文档说 `claude --continue` 续接当前目录中最近 session；ID
查找限于当前 project directory 及其 git worktrees。其 picker 会显示当前 worktree 的
background sessions（标为 `bg`），但这不是 `--continue` 的 active-candidate 规则。
同页直接警告：在两个 terminal 不 fork 地 resume 同一 session，双方消息会交织到一个
transcript；`--fork-session` 会创建独立 ID。 [C2]

**证据边界。** “most recent”未规定是创建、最后消息、完成还是其他时间；也未发布同一
时刻候选的顺序、`--continue` 是否排除 bg/active session，或跨进程单写者仲裁。直接的
交织警告证明并发续接可以产生不安全的 transcript 结果，但不证明最近选择器必然会挑中
一个活跃会话。 [C1][C2]

### 2.3 Gemini CLI

**文档化事实。** Gemini CLI reference 将 `--resume` 定义为恢复已有 session，参数可为
`"latest"`（most recent）或 index number（例 `--resume 5`）。该页的命令表给出
`gemini -r "latest" "query"` 和以 `<session-id>` 续接的例子；`--list-sessions` 则只列
当前 project 的可用会话。 [G1]

**文档化事实。** Gemini 的会话教程也将 bare `gemini -r` 描述为恢复最近工作，并把
“active or past session”列为前提；它未阐明 `-r`/`latest` 对 active session 的候选规则。
这页与 reference 的参数形式并存，表明脚本不应假设一个未文档化的等价解析规则。 [G2]

**证据边界。** 引用资料没有定义 project 内 sessions 的排序键、index 的稳定性或
tie-break，也没有提供 `--prompt` 与 `--resume latest/index` 联用的专门无终端示例。更
没有发布针对同一 session 的并发 resume、写入锁、活跃 turn 排除或中断恢复边界。 [G1][G2]

## 3. 选择、活跃和并发的判定边界

| 决策面 | 已有证据 | 不能据此断言 |
| --- | --- | --- |
| Scope | Codex 暴露 cwd filtering 与 `--all`；Claude 对 `--continue` 是 current directory，对 ID 是 project/worktrees；Gemini list 是 current project。 [O1][C1][C2][G1] | 这三种 scope 相同，或 Codex `--all` 必然扩展 `--last`。 |
| Tie-breaking | Codex 说 newest，Claude 说 most recent，Gemini 说 latest。 [O1][C1][G1] | 具体时间字段、相等时间的全序、列表 index 的稳定性，或选择与执行之间候选集不变。 |
| Active session | Claude picker 标出 background sessions；Gemini 教程承认 active 或 past session；Codex 只说 recorded session。 [C2][G2][O1] | 任一“最近”selector 是否包含、跳过或锁住 active session。 |
| Concurrency | Claude 直接说明同 session 双 terminal resume 会 interleave；其 worktree 文档建议把并行 sessions 放入独立 worktrees，使 edits 不冲突。 [C2][C3] | Codex/Gemini 有相同问题或有内部保护；worktree 隔离等于 transcript 单写者。 |

## 4. 跨产品综合与安全门槛

“指定 session ID”与“从历史中猜出一个 session”应视为两个独立控制面。前者由调用方
携带稳定身份；后者依赖一个随 cwd/project/worktree、并行任务和历史更新而变动的候选集。
Claude 的 headless 文档在多会话时选择捕获 ID，正是这一差异的直接产品证据。 [C1]

因此，**安全 v1 应省略最近选择器**。这个结论不是说成熟产品不提供该便利功能，而是说
公开资料还没有为可靠自动化给出充分、可验证的契约：

- 所有候选边界须可公开说明，且不能由调用方未察觉的目录或 worktree 改变；
- “最近”须指定可观察的排序字段、平局规则和选择快照；
- 活跃会话必须有公开策略（排除、拒绝、排队或可验证 lease），而不是让第二个进程静默
  追加；
- 需要并行分支时，须创建独立 session/工作区，而不是对同一 transcript 并发写入。

这些是从范围差异、排序缺口以及 Claude 明确的 interleave 风险得出的**产品中立综合**；
它们不是厂商未披露内部机制的断言，也不是本仓库的实现方案。 [O1][C1][C2][C3][G1][G2]

## 5. 陷阱和证据缺口

- **“显式 opt-in”不等于可重放。** 用户写出 `--last`、`--continue` 或 `latest` 只表示
  愿意使用当时的历史选择器；它不会固定将被选中的会话。 [O1][C1][G1]
- **目录范围不能被抹平。** Claude 在 `--continue`、ID lookup 和 worktree resume 中给出
  不同层次的范围规则；Codex/Gemini 的公开描述又不同。应按产品、命令和版本逐项核验。
  [O1][C1][C2][C3][G1]
- **Claude 的并发警告不能外推。** 它是一个具体产品的直接风险证据；Codex/Gemini 未
  发布等价机制，故只能记为未知，而不是宣称它们会 interleave 或已经安全。 [C2][O1][G1]
- **Gemini 文档的表述存在粒度差异。** reference 记录 `latest/index`，教程演示 bare
  `-r`。没有额外版本化实验或正式兼容声明时，不应把其中一个页面扩写为另一个调用形式
  的完整非交互契约。 [G1][G2]

## References

All sources accessed 2026-08-03.

- **[O1] Local observation:** `codex --version` and `codex exec resume --help`
  on the research machine; observed version `codex-cli 0.146.0`. This is a
  reproducible observation of that binary's help, not a claim about unstated
  persistence or concurrency behavior.
- **[C1] Anthropic, “Run Claude Code programmatically”** (official Claude Code
  documentation): https://code.claude.com/docs/en/headless.md
- **[C2] Anthropic, “Manage sessions”** (official Claude Code documentation):
  https://code.claude.com/docs/en/sessions.md
- **[C3] Anthropic, “Run parallel sessions with worktrees”** (official Claude
  Code documentation): https://code.claude.com/docs/en/worktrees.md
- **[G1] Google, “Gemini CLI cheatsheet”** (official Gemini CLI documentation):
  https://geminicli.com/docs/cli/cli-reference/
- **[G2] Google, “Manage sessions and history”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/tutorials/session-management/
