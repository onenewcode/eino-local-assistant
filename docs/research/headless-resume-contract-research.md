# 非交互式 durable session resume：业界契约研究

> 状态：研究笔记，不是本仓库的实施计划。
>
> 调研日期：2026-08-03。CLI 的存储、筛选和输出契约会随版本改变；采用任何
> 结论前应重新核验引用的版本与页面。
>
> 范围：非交互式 coding-agent 进程如何选择一个已持久化会话、在其上追加一条
> prompt，并向调用方报告结果。特别考察会话标识和范围、`most recent` 的含义、
> prompt/stdin、并发或中断恢复、持久化、stdout/结构化输出和退出行为。
>
> 不在范围：本仓库实现方案、厂商未披露的 transcript-to-prompt 算法、模型质量、
> 工具副作用的事务性或工作区回滚保证。

## 1. 结论

- **跨产品综合。** 自动化续接应优先使用调用方已保存的**明确、不透明 session
  ID**，再把新的 prompt 作为同一次调用的显式输入。三个产品都提供指定会话的
  路径；相较之下，`most recent` 是依赖产品的时间、目录、worktree 或列表序号的
  选择器，适合人工快捷方式，不是可重放任务的稳定身份。 [O1][C1][G2]
- **跨产品综合。** “会话 ID 可找到”与“可安全并发写入”不是同一保证。Claude
  Code 明确说明：不 fork 而在两个 terminal resume 同一 session，消息会交织到同一
  transcript。公开资料没有显示 Codex 或 Gemini 对此提供 single-writer lease、排队或
  compare-and-swap 语义；不能把 durable transcript 当作并发安全队列。 [C2][O1][G2]
- **跨产品综合。** 非交互式续接应把 `prompt`、是否从 stdin 读入、输出模式和退出码
  一起看作调用契约。Claude 和 Gemini 都把单个 JSON 与 JSONL 流区分开，并公开至少
  零/非零（Gemini 还发布细分码）；当前 Codex `--help` 只说明 `--json` 会把事件作为
  JSONL 写到 stdout，未发布终态记录或错误码的字段契约。 [C1][G1][O1]
- **证据边界。** 三家的公开资料都不足以证明“重新执行一条已中断 turn 一定不会重复
  工具副作用”。Claude 公开处理 SIGTERM 时会 abort in-progress turn 并终止 Bash
  process tree，但没有把此前工具动作描述成可回滚事务；Codex/Gemini 的本来源集也
  未给出同等恢复或幂等保证。 [C1][O1][G1]

## 2. 决策面与不能混同的概念

```text
调用方保存的 session_id + 一条新 prompt
                  |
                  v
    scope resolution (project / cwd / worktree / index)
                  |
                  v
    durable transcript + current launch configuration
                  |
                  v
         one resumed turn -> stdout/stderr + exit status
```

- **指定续接（specific resume）**：调用方给出 ID、名称或 tag。只有当产品公开了
  该标识的查找范围时，才能判断它是否可在当前工作目录使用。
- **最近续接（most recent / continue）**：产品从一个隐含范围选“最近”记录；它不是
  session ID 的别名。范围改变、并行任务或列表顺序改变都可能选到不同会话。
- **持久会话与当前 turn**：持久 transcript 允许以后继续，不等于上一次 agent turn
  已经完成、stdout 已 flush，或外部工具副作用可撤销。
- **最终 JSON 与 JSONL**：前者是一次调用结束后可整体解析的结果；后者是运行中多条
  记录的流。两者在 Claude/Gemini 都是显式不同的模式，不能只因都携带 session
  信息便当作同一种 API。 [C1][G1]

## 3. 已发布产品的证据

### 3.1 Codex CLI 0.146.0：本机 `exec resume --help` 观察

**文档化事实（版本化本机观察）。** 于 2026-08-03 在研究机执行
`codex --version`、`codex exec --help` 和 `codex exec resume --help`；观察到版本为
`codex-cli 0.146.0`。下列内容只描述该二进制给出的 help，不能外推为未列出的持久化
实现或长期 schema。 [O1]

- `codex exec resume [SESSION_ID] [PROMPT]` 接受 UUID 或 thread name；可解析为 UUID
  时 UUID 优先。省略 ID 时必须给 `--last`，它选择最新 recorded session。`--all`
  关闭 cwd filtering，显示所有 sessions。也就是说，`--last` 与“按当前默认范围找
  最近”是两个需要显式选择的控制点。 [O1]
- `PROMPT` 是恢复后要发送的一条消息；其值为 `-` 时从 stdin 读取。普通
  `codex exec` 也允许初始 prompt 为 `-` 或省略时从 stdin 读；同时传入 prompt 且 stdin
  已 pipe 时，stdin 被追加为 `<stdin>` block。 [O1]
- `resume` 提供 `--ephemeral`（不把 session files 持久化到磁盘）、`--json`（事件作为
  JSONL 写到 stdout）和 `--output-last-message <FILE>`（最后 agent message 写入文件）。
  这些开关表明续接后的持久化和输出须由调用方显式选择，但 help 没有定义 JSONL 的事件
  schema、最终事件、stderr 分配、失败退出码或文件写入的原子性。 [O1]
- help 未声明同一个 session 同时由多个 `exec resume` 进程续接时的排序、拒绝、租约或
  merge 行为；也没有说明进程崩溃/信号中断时最后一条 prompt 或 tool result 的 durably
  committed 边界。这些都是**证据缺口**，不是“默认安全”的证明。 [O1]

### 3.2 Claude Code：headless `-p`、`--continue` 和 `--resume`

**文档化事实。** Claude 的官方 headless 文档把 `claude -p` 定义为非交互式运行，且
明说所有 CLI options 均可与 `-p` 一同使用，包括 `--continue`、工具许可和输出格式。
它示范先以 `--output-format json` 取出 `session_id`，再执行
`claude -p "Continue that review" --resume "$session_id"`。 [C1]

- `--continue` 续接当前目录中最近的会话；`--resume <name-or-id>` 续接指定会话。
  非交互 `-p` 所创会话不会出现在 session picker 中，但可以以 session ID 恢复。ID 查找
  限于启动会话的当前 project directory 和该 repository 的 git worktrees；从其他目录运行
  会得到 `No conversation found with session ID: <session-id>`。 [C1][C2]
- Claude 在工作期间持续把 session 写为本地 JSONL transcript。恢复时公开列举会恢复
  conversation history（含 tool calls/results）、模型（存在覆盖/退役例外）、部分 permission
  mode、active goal 和未到期 scheduled tasks；有些 launch flag（如 `--mcp-config`、
  `--settings`、`--plugin-dir`、`--add-dir`）必须在 resume 时再次传入。该 JSONL 行格式是
  internal 且会随版本变动，官方建议脚本用 `-p` 的结构化输出而非直接解析 transcript。 [C2]
- 同一会话若在两个 terminal 里 resume 且没有 fork，二者的消息会交织进同一 transcript。
  `--fork-session` 则创建独立 session ID；以新进程 fork 时原 session 的一次性权限授予
  不会跟随。这是本来源集中最直接的 active-turn/并发安全边界。 [C2]
- headless 成功为 exit code `0`，失败为非零；无效 flag 在启动前写 stderr，运行内错误
  （例如认证缺失）则作为 stdout 的 result 输出。`--output-format json` 是有 result、
  session ID 和 metadata 的最终 JSON；`stream-json` 是 JSONL，文档称最后一行是含最终
  response、cost、session metadata 的 `result` message。 [C1]
- 若 `claude -p` 收到 SIGTERM，官方说它 abort in-progress turn、结束运行中的 Bash
  process tree、运行 `SessionEnd` hooks，并以 `143` 退出。background Bash 在最终结果和
  stdin 关闭后约五秒被结束；background subagent/workflow 因结果属于最终输出而会等待，
  等待默认上限十分钟。这是进程结束的可观察处理，而不是已执行副作用的回滚保证。 [C1]

### 3.3 Gemini CLI：headless 和 `--resume` 选择器

**文档化事实。** Gemini 的 headless mode 在非 TTY 环境或使用 `-p`/`--prompt` 时触发。
CLI cheatsheet 将 `-p` 定义为强制非交互，且其 prompt 会附加到 stdin input；`--resume`
的参数可为 session ID、`"latest"`（最近）或序号（例如 `--resume 5`）。 [G1][G2]

- cheatsheet 分别给出 `gemini -r "latest"`、`gemini -r "latest" "query"` 和
  `gemini -r "<session-id>" "query"`：前者续接最近 session，后两者展示在恢复时追加
  prompt 与按 ID 恢复。`--list-sessions` 仅列出当前 project 的可用 sessions；
  `--delete-session` 也按其 index 操作。换言之 `latest` 和数字都不是跨 project 的 durable
  identity。 [G2]
- `--output-format` 明确有 `text`、`json`、`stream-json`。headless 文档称 `json` 为一个
  JSON object，包含 `response`、`stats` 和可选 `error`；`stream-json` 为 JSONL，可出现
  `init`、`message`、`tool_use`、`tool_result`、非致命 `error` 和最终 `result`。 [G1][G2]
- Gemini 发布 headless exit codes：`0` 成功、`1` 一般/API 错误、`42` 输入错误、`53`
  超过 turn limit。与 Claude 相比，这给脚本额外的失败分类，但其 `error` stream event
  本身仍不等于最终失败。 [G1]
- **证据边界。** 这些官方页面没有明确给出 `gemini -p --resume <id>` 的组合示例，也
  没有说明 headless 新 session 何时生成/输出 session ID、恢复时究竟重载哪些配置、同一
  session 并发 resume 的写入规则，或 SIGTERM/崩溃后的重试边界。因此不能由各个独立 flag
  的存在推断这些保证。 [G1][G2]

## 4. 对照：调用方能依赖什么

| 决策面 | Codex CLI 0.146.0（本机 help） | Claude Code（官方 docs） | Gemini CLI（官方 docs） |
| --- | --- | --- | --- |
| 指定会话 | UUID 或 thread name；UUID 优先。 [O1] | `--resume <session-id/name>`；`-p` 可传 ID。 [C1][C2] | `--resume <session-id>`。 [G2] |
| 最近选择 | 省略 ID 时显式 `--last`；`--all` 关 cwd filter。 [O1] | `--continue` = 当前目录最近 session。 [C2] | `latest` 或 index；sessions 为当前 project。 [G2] |
| 新 prompt / stdin | resume 的 `PROMPT=-` 从 stdin。 [O1] | `-p` prompt；示例为 `-p --resume <id>`。 [C1] | `-p` prompt 附加 stdin；文档例子用 `-r <id> "query"`。 [G2] |
| 持久化 | `--ephemeral` 表示不写 session files；余下存储规则未在 help 说明。 [O1] | 持续本地 JSONL；可用 `--no-session-persistence` 抑制一次 headless 写入。 [C2] | 当前 project session list；本来源集未证明 headless session ID 产生/存储细节。 [G2] |
| 同 session 并发 | 未公开。 [O1] | 不 fork 会消息交织；fork 得新 ID。 [C2] | 未公开。 [G1][G2] |
| 机器输出 / exit | `--json` 为 stdout JSONL；schema/exit 未公开。 [O1] | JSON final / JSONL stream；成功 0、失败非零、SIGTERM 143。 [C1] | JSON final / JSONL stream；0/1/42/53。 [G1] |

## 5. 跨产品综合：durable resume 的最小边界

以下为**产品中立综合**，不是任一家厂商的实现指令或逐字段 API。

### 5.1 身份与选择必须分离

调用方应在一次成功的可持久运行后保存产品返回或列出的 opaque ID，并在后续自动化中
传回该 ID。`latest`/`--continue`/index 只表达“在某个隐含范围内的最近记录”：Codex 的
范围可被 `--all` 改变，Claude 受 cwd/project/worktree 约束，Gemini 则把列表限定在当前
project 且还接受易变 index。上述事实共同支持“将选择器与身份分离”，但不构成跨厂商
ID 可互换的标准。 [O1][C2][G2]

调用方记录的最小关联信息应是自己的任务键、产品标识、opaque session ID、创建时的
scope（至少 project/cwd/worktree）和上次已知完成状态。这里的 scope 是为避免在错误
workspace resume 的调用方元数据；它不暗示厂商用相同的内部键。

### 5.2 恢复后的输入必须有明确归属

新 prompt 是一个新的 turn，不是命令行 wrapper 自动重发上一条未完成 prompt。Codex
明确把 resume 的第二 positional argument 定义为恢复后发送的 prompt；Claude 示范把
`-p` prompt 与 `--resume` 组合；Gemini 示范 resume 后以 query 继续。stdin 规则也不同，
因此自动化不能把“prompt absent”默认为空消息或把 pipe 内容当作同一个字段。 [O1][C1][G2]

对于潜在重复执行，调用方还应自行关联 invocation ID 与自己的幂等键。公开资料证明
Claude 会在 SIGTERM abort 当前 turn，但没有任一来源承诺“恢复会识别并跳过先前已部分
执行的工具调用”；`exit != 0` 只说明当前进程未成功结束，而不能倒推出工作区或外部系统
没有改变。 [C1][O1][G1]

### 5.3 active turn 需要单写者语义，而非 transcript 的存在

Claude 的交织警告直接表明 durable transcript 不是多个调用者的事务日志。产品中立地，
一个自动化协调者应为每个 session 维护一个逻辑 writer：只有收到前次运行的明确终态
（最终 JSON/result 和退出状态）或确认其已终止后，才决定启动下一次 resume。若需要探索
不同分支，应使用产品的 fork/branch 能力或创建新 session，而非并发追加同一 ID。这个是
从 Claude 的已知风险与其 fork 机制得到的**综合**；Codex/Gemini 的公开资料尚未足以证明
相同的厂商行为。 [C2][O1][G2]

### 5.4 输出与退出共同决定一次调用的终态

适合脚本的调用不应仅靠“stdout 有文字”或“进程已退出”认定完成。Claude 说明运行内
失败可能作为 stdout result 输出但进程非零；Gemini 把最终对象/stream 最终 `result` 与
具体 exit code 都公开；Codex 当前 help 只足以确认 JSONL events 这一点。因此调用方应按
所选厂商/版本的规范共同检查输出模式、可解析终态和 exit status，对无法 flush 的中断
标记为“结果未知”，而不是自动重试为同一 session 的另一条 prompt。 [C1][G1][O1]

## 6. 陷阱与证据缺口

- **`latest` 非稳定指针。** Codex 的 `--last`、Claude 的 `--continue` 和 Gemini 的
  `latest` 都存在，但查找范围和选择入口不同；并行任务下它们可能选到不是调用方先前
  创建的会话。 [O1][C2][G2]
- **scope 不只是目录文字。** Claude 明确把 ID 查找范围扩展到当前 project 和该 repo
  的 worktrees，而 Gemini 明确把列表限于当前 project；当前 Codex help 只暴露 `--all`
  可禁用 cwd filtering。不能将某一种 scope 规则移植给另一 CLI。 [O1][C1][C2][G2]
- **持久 transcript 不等于稳定读取 API。** Claude 特别说明 JSONL entry format 是
  internal 且可变；Codex 本机 help 和 Gemini 本来源集没有发布可兼容的 transcript schema。
  外部自动化应依赖公开 CLI 输出/ID，不应解析私有 session 文件来拼 resume。 [C2][O1][G1]
- **中断不是回滚。** Claude 所说的终止 Bash process tree 和 `143` 退出可帮助监督程序
  判断进程状态，但不足以让调用方安全地重跑有外部副作用的 prompt。Codex 和 Gemini 的
  本来源集没有填补这一语义空白。 [C1][O1][G1]
- **未验证的头端组合。** Gemini 文档分别定义 headless、`--resume`、JSON 输出和 exit
  codes，却没有在本来源集中给出 `-p --resume`、session ID 输出、并发 resume 或 crash
  recovery 的完整契约；这些必须通过未来官方说明或版本化实验单独核验。 [G1][G2]

## References

All sources accessed 2026-08-03.

- **[O1] Local observation:** `codex --version`, `codex exec --help`, and
  `codex exec resume --help` on the research machine; observed version
  `codex-cli 0.146.0`. This is a reproducible observation of that installed
  binary, not a public schema specification.
- **[C1] Anthropic, “Run Claude Code programmatically”** (official Claude Code
  documentation): https://code.claude.com/docs/en/headless.md
- **[C2] Anthropic, “Manage sessions”** (official Claude Code documentation):
  https://code.claude.com/docs/en/sessions.md
- **[G1] Google, “Headless mode reference”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/headless.md
- **[G2] Google, “Gemini CLI cheatsheet”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/cli-reference.md
