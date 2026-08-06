# Workspace diff、review 与 verify 表面：行业实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. The cited product documentation is rolling
> documentation; re-verify behavior against a pinned release before relying on
> it.
>
> Decision surface: 部署式 coding agent 如何展示当前 workspace diff，提供
> review/verify 的只读或近只读表面，以及如何把这些表面与 goal/checklist、tool
> transcript 和真实测试结果分开。
>
> Scope: 交互式 CLI/TUI 中的本地 diff viewer、模型辅助 review、应用/测试验证、
> 触发方式、刷新模型、文件副作用、失败/空 diff/非 Git workspace、持久化和安全
> 边界；比较 Codex CLI、Claude Code 和 Aider。
>
> Out of scope: 本仓库实现或命令设计；IDE 专用 diff；GitHub/GitLab 平台本身的
> review 产品；未公开的内部调度、模型上下文拼装、存储 schema 和安全实现。

本文逐条标记证据类型：`[Documented fact / 已文档化事实]` 表示官方文档直接
说明的行为；`[Cross-product synthesis / 跨产品综合]` 表示由多个产品事实归纳的
产品中立模式；`[Evidence gap / 证据缺口]` 表示现有公开材料不足以确认，不能用
另一个产品的行为补齐。

## 1. Conclusions

- **[Cross-product synthesis / 跨产品综合]** “显示 diff”“AI review”“verify/test”
  是三个不同的证据层：diff 是某一时刻的版本控制观察，review 是模型对选定输入
  的判断，verify/test 是实际启动的程序或测试命令的输出。一个 review 通过不等于
  测试通过；一个测试通过也不等于当前 workspace 没有遗漏的 diff。
- **[Documented fact / 已文档化事实]** Claude Code 明确把 `/diff`、`/code-review`、
  `/review` 和 `/verify` 分开：`/diff` 打开当前 Git diff 和按 turn 的 diff，
  `/code-review` 检查当前 diff 并可用 `--fix` 应用发现，`/review` 是对 GitHub PR
  的快速只读 review，`/verify` 则构建、运行并观察应用。[A1]
- **[Documented fact / 已文档化事实]** Aider 的 `/diff` 只展示“自上次消息以来”的
  变化，`/ask` 明确不编辑文件，`/test` 执行 shell 命令并在非零退出时把输出加入
  对话，`/lint` 则是 lint **并修复**；它还默认用 Git commit 保存 AI 编辑。[D1][D2]
- **[Documented fact / 已文档化事实]** Codex CLI reference 将 `/diff` 和 `/review`
  作为不同命令：前者展示当前 Git diff，后者执行代码 review；同一命令参考还列出
  `/plan` 等协作状态命令。[C1] 该页面没有把 `/review` 的完整目标选择、无 diff
  行为、review 结果持久化或 verify 结果 schema 写成稳定合同。
- **[Cross-product synthesis / 跨产品综合]** 只读边界来自“触发的动作和允许的工具”，
  不是来自 `review`、`verify`、`plan` 或 `read-only` 这个标签。Claude 的
  `/code-review --fix`、Aider 的 `/lint` 和 `/run` 都说明同一产品可以在相邻表面
  触发写入或任意 shell；远程 PR comment、云端 review 和测试生成物也可能产生
  workspace 之外的副作用。[A1][D1]
- **[Evidence gap / 证据缺口]** 三个产品的官方页面都没有统一说明 staged、unstaged、
  untracked、ignored、submodule 和外部 worktree 如何组成“current diff”。因此不能
  把“当前 diff”理解成完整的文件系统快照，也不能从空 diff 文本推断工作区完全干净。

## 2. Terms and boundaries

| 对象 | 它直接回答的问题 | 不应从它推断什么 |
| --- | --- | --- |
| workspace diff | Git 或产品自己的变更投影是什么？ | 没有列出的 untracked/ignored 文件不存在；或代码已通过测试。 |
| AI review | 模型根据 diff/上下文发现了哪些可能的问题？ | 发现已被证明、文件已被修改、或 review 覆盖了所有运行时路径。 |
| verify/test | 某个命令或应用在某次运行中的实际结果是什么？ | 未来运行、其他环境、未执行的路径或当前最新文件仍然相同。 |
| goal / plan | 用户想达成什么，或模型提出了什么步骤？ | 这些步骤已经执行、workspace 已变更、或存在活跃进程。 |
| checklist / todo | 进度的人工/模型投影是什么？ | `done` 等于测试通过，`in_progress` 等于仍有 shell/subagent 在运行。 |
| tool transcript | 哪些模型/工具交互曾被记录？ | 记录是当前文件状态、命令成功的独立证明，或能重放所有外部副作用。 |

**[Cross-product synthesis / 跨产品综合]** 一个较稳妥的证据链是：

```text
goal / plan / checklist       意图与进度投影
             |
             +--> workspace diff      版本控制状态的某次观察
             |
             +--> tool transcript     模型与工具交互记录
             |
             +--> verify / test       实际命令或应用运行结果
             |
             +--> AI review           模型对选定 diff/上下文的判断
```

这些箭头表示可能的关联，不表示任何一个对象自动证明下一个对象。尤其是 review
可以读取 diff，却不应覆盖 diff；verify 可以产生 transcript，却不应被 checklist
的文字摘要取代。

## 3. Evidence from deployed applications

### 3.1 Codex CLI

#### Diff、review 与触发方式

- **[Documented fact / 已文档化事实]** Codex CLI 的交互命令参考列出 `/diff`，描述为
  展示当前 Git diff；同一参考列出 `/review`，用于 review 代码。[C1] 这提供了一个
  用户显式触发的本地 diff 表面和一个独立的模型辅助 review 入口。
- **[Documented fact / 已文档化事实]** `/diff` 的命令描述是“展示”而不是“请模型
  判断”；页面没有把它描述为发送 prompt、生成建议或修改文件。因此在公开文档层面，
  它属于本地观察命令，而非模型 review。[C1]
- **[Cross-product synthesis / 跨产品综合]** `/diff` 应按调用时的 Git 状态理解为
  快照；除非产品另行说明自动刷新，不能假定它会在另一个终端修改或提交后持续更新。
  Codex reference 没有给出自动刷新、流式 diff 事件或文件变更监听承诺。[C1]
- **[Documented fact / 已文档化事实]** `/review` 是独立于 `/diff` 的 review 命令，
  因而 review 结果不是 Git 命令的原始输出，而是 agent 的分析表面。[C1]
- **[Evidence gap / 证据缺口]** reference 没有在同一页面公开 `/review` 是否固定使用
  read-only tool policy、是否展示“apply findings”确认、是否包含 uncommitted diff
  之外的上下文、以及 review 进行时 workspace 发生变化时采用哪个版本。不能把“review”
  名称扩展为硬性的无写保证。

#### Plan/checklist、transcript 与 verify 边界

- **[Documented fact / 已文档化事实]** `/plan` 被列为独立的交互命令；它属于计划/协作
  阶段，而不是 `/diff` 的结果或测试结果。[C1]
- **[Cross-product synthesis / 跨产品综合]** 即使计划中列出“检查 diff”或“运行测试”，
  checklist 也只是计划的进度投影；它不替代当前 Git 状态、工具输出或测试 exit code。
  Codex reference 没有把 `/plan` item 定义成 workspace assertion。[C1]
- **[Evidence gap / 证据缺口]** 当前命令参考没有为 Codex 提供独立的 `/verify` 结果
  合同，也没有说明 `/review`、`/diff` 和 session transcript 的持久化关系。自然语言
  请求模型运行测试可能会进入普通 agent/tool loop，不能把它当作一个只读 verify 命令。

#### 文件、失败和安全边界

- **[Documented fact / 已文档化事实]** Codex 的官方安全说明把 approval policy、sandbox
  和工具执行权限作为安全控制面；它们与用户在对话中看到的 review 文本是不同层次。[C2]
- **[Cross-product synthesis / 跨产品综合]** `/diff` 本身不应修改文件；但一次后续的
  agent review 或测试请求若允许 shell、patch 或其他工具，必须按该工具的 permission /
  sandbox 规则判断副作用，而不能沿用 `/diff` 的只读直觉。
- **[Evidence gap / 证据缺口]** reference 没有明确写出空 diff 的显示/退出码、非 Git
  workspace 的错误文本、Git 不可用时 `/review` 的失败分类、未跟踪文件是否被收集，或
  review 输出是否单独持久化。以上情况只能要求重新核验，不能从命令名称补齐。

### 3.2 Claude Code

#### 当前 diff：刷新 viewer，不是模型判断

- **[Documented fact / 已文档化事实]** Claude Code `/diff` 打开交互式 diff viewer，
  同时提供 uncommitted changes 和各个 Claude turn 的 diff；左右键切换当前 Git diff
  与单个 turn，上下键浏览文件，Enter 查看文件 diff，Esc 返回列表。[A1]
- **[Documented fact / 已文档化事实]** 当前命令文档说明，自 v2.1.198 起，打开的 viewer
  会在 repository 的 Git 状态被另一个终端改变时自动刷新，例如 branch switch 或
  commit。[A1] 因而 Claude 的 viewer 明确比一次性文本快照更接近实时观察，但它仍然
  是 Git 状态的刷新投影，不是模型对每个变化的连续 review。
- **[Cross-product synthesis / 跨产品综合]** “当前 Git diff”和“某个 Claude turn 的
  diff”是两个不同 target：前者反映当前仓库状态，后者反映 session 中一次 agent
  变更。两者可能因用户手工编辑、commit、rebase 或其他 agent 而不同。[A1]
- **[Evidence gap / 证据缺口]** 页面没有在 `/diff` 描述中完整定义 staged、untracked、
  ignored、submodule 或符号链接的纳入规则，也没有承诺 viewer 可以替代 `git status`
  或完整文件系统清单。

#### Review、verify 与确认边界

- **[Documented fact / 已文档化事实]** `/code-review [level] [--fix] [--comment]
  [target]` 检查当前 diff 的 correctness bugs 和 cleanup opportunities；`--fix`
  会应用发现，`--comment` 会向 GitHub PR 发布 inline comments，`ultra` 会运行云端
  deep review。[A1]
- **[Documented fact / 已文档化事实]** `/review [PR]` 是按 PR 编号进行的快速、单次、
  read-only GitHub pull request review；无参数时先列出可选 open PR。[A1] 它不是本地
  workspace `/diff` 的别名，不能用“read-only PR review”证明本地文件 review 也有
  同样边界。
- **[Documented fact / 已文档化事实]** `/verify` 是 bundled skill，用于 build project
  app、运行它并观察结果，而不是只依赖 tests 或 type checks。[A1][A2] 这是执行真实
  程序的验证表面，不是一个静态 diff viewer。
- **[Cross-product synthesis / 跨产品综合]** Claude 的确认边界相对显式：只看 diff
  不需要“应用发现”的确认；`/code-review --fix` 明确跨过文件修改边界；`--comment`
  明确跨过 GitHub 外部写入边界；`/review` 的 read-only 只适用于它的 PR target。
- **[Evidence gap / 证据缺口]** `/verify` 的命令描述没有给出所有项目的统一“绝不修改
  workspace”保证。build、run、hooks、dev server 和测试框架可能产生生成物、缓存或
  外部副作用；页面也没有给出 verify 失败时的统一结构化结果或回滚承诺。

#### Goal/checklist、transcript 与失败边界

- **[Documented fact / 已文档化事实]** Claude commands 页面把 `/plan` 作为 plan mode，
  `/tasks` 作为当前 session 的后台工作列表，`/status` 作为设置/状态界面；这些命令
  与 `/diff` 和 `/code-review` 分列。[A1]
- **[Documented fact / 已文档化事实]** common workflows 说明 plan mode 会读取文件并提出
  plan，在用户批准前不编辑；也说明可以用 `Shift+Tab` 切换到 plan mode。[A2] 这使
  proposed plan 的确认点与之后的 edit/tool 执行点可见地分开。
- **[Cross-product synthesis / 跨产品综合]** `/tasks`、transcript 和 todo/checklist
  能解释“模型做了什么或声称做了什么”，但不能取代 `/diff` 的最新 Git 观察。某个
  task 显示 done 也不意味着当前 branch 没有后续手工改动。
- **[Documented fact / 已文档化事实]** `/security-review` 以当前 branch 与 origin
  默认 branch 的 diff 为输入，并要求存在 `origin` remote；官方命令说明还提示
  `ambiguous argument` 失败路径。[A1] 这是一个具体的 target/环境前置条件，而不是
  所有 review 命令的通用错误模型。
- **[Evidence gap / 证据缺口]** `/diff` 在空 diff、非 Git workspace、Git 命令失败时
  的 UI/退出码没有在命令页给出；`/code-review` 在这些场景如何结束、是否重试、是否
  仍调用模型也未形成公开稳定合同。

#### Persistence and security

- **[Documented fact / 已文档化事实]** common workflows 说 Claude Code 保存 conversation
  locally，可用 `--continue` / `--resume` 继续；但这描述的是 conversation/session
  恢复，不等于 `/diff` viewer、review finding 或实时进程都是独立持久资源。[A2]
- **[Documented fact / 已文档化事实]** `/code-review ultra` 运行 cloud review，
  `/comment` 可写入 GitHub；这些是与本地只读 diff 不同的数据和副作用边界。[A1]
- **[Evidence gap / 证据缺口]** 公开页面没有说明 review 输入在 session transcript、云端
  review 或日志中的逐字段保留/脱敏期限，也没有说明 Git 状态在 review 运行中改变时的
  一致性策略。不能把“只读文件”扩大为“不会离开本机”或“不会留下远程记录”。

### 3.3 Aider

#### Diff、ask 与实际命令

- **[Documented fact / 已文档化事实]** Aider `/diff` 展示自上次消息以来的 file changes；
  这是 Git/chat 边界上的本地展示命令，不是模型评审。[D1][D2]
- **[Documented fact / 已文档化事实]** `/ask` 用来询问 code base 而“不编辑任何文件”；
  `/code` 则请求修改代码。两者是模型对话模式的显式分叉。[D1]
- **[Documented fact / 已文档化事实]** `/test` 运行 shell command，并在非零退出时把
  output 加入 chat；`/run` 运行 shell command 并可选择把 output 加入 chat；`/lint`
  对 in-chat files 或 dirty files 执行 lint 并修复。[D1]
- **[Cross-product synthesis / 跨产品综合]** Aider 的 `/test` 把“真实命令输出”和
  “后续模型是否处理该输出”分开：命令先产生 exit/output，失败输出进入 chat 并可能
  影响后续 agent turn；`/lint` 则不能被归入只读 verify，因为其描述本身包含 fix。
- **[Evidence gap / 证据缺口]** Aider command page 没有单独的 `/review` 或 `/verify`
  合同；“review”通常需要把 `/diff`、`/ask`、自然语言 prompt 或 Git 工具组合起来，
  但其输入范围、review 结果格式和无写保证没有统一文档定义。

#### Git、失败与持久化

- **[Documented fact / 已文档化事实]** Aider 的 Git 文档说它最好在 Git repo 中运行；
  在没有 repo 的目录启动时会询问是否创建 repo；每次 AI 编辑默认以描述性 commit
  保存；检测到预先存在的 dirty files 时，默认先 commit 它们以隔离用户编辑。[D2]
- **[Documented fact / 已文档化事实]** Aider 提供 `/diff`、`/undo`、`/commit` 和
  `/git`；也提供 `--no-auto-commits`、`--no-dirty-commits` 和 `--no-git` 选项，
  其中 `--no-git` 完全关闭 Git integration。[D2]
- **[Cross-product synthesis / 跨产品综合]** Aider 的 diff 语义不能直接与 Claude 的
  “current Git diff”或 Codex 的同名 viewer 等同：Aider 明确按“上次消息”切分，并且
  默认 commit 会改变 Git 历史边界。它提供更强的历史恢复能力，同时也让“当前 dirty
  diff”与“agent 最近一次改动”需要分别解释。[D1][D2]
- **[Evidence gap / 证据缺口]** Aider 文档没有明确 `/diff` 在 `--no-git`、空 diff、
  非 Git 命令失败或 untracked-only workspace 下的确切显示与退出码；“询问创建 repo”
  也不等于所有 diff/review 命令都能在非 Git workspace 工作。

#### Security and model boundary

- **[Documented fact / 已文档化事实]** Aider 的 Git 文档说明，为生成 commit message，
  weak model 会收到 diffs 和 chat history；这直接证明 Git 集成中的某些动作会把 diff
  和历史发送给模型。[D2]
- **[Cross-product synthesis / 跨产品综合]** `/ask` 的“不编辑文件”只说明文件编辑边界，
  不说明 code context 不会发送到模型、不说明 shell 不会运行，也不说明模型输出不会
  影响之后的 `/code` turn。文件只读、模型只读、网络只读是三个不同命题。
- **[Evidence gap / 证据缺口]** 该产品文档没有给出类似统一 sandbox/approval matrix
  的 review/verify 安全合同，也没有说明 chat history、diff 和 lint/test output 的
  默认保留、脱敏和跨进程恢复策略。

## 4. Mechanisms and tradeoffs

### 4.1 Trigger and data source

| 表面 | Codex CLI | Claude Code | Aider |
| --- | --- | --- | --- |
| 当前 diff | `/diff`；当前 Git diff。[C1] | `/diff`；当前 Git diff + per-turn diff，外部 Git 状态可自动刷新。[A1] | `/diff`；自上次消息以来的变化。[D1] |
| 模型 review | `/review`；命令参考将其列为代码 review。[C1] | `/code-review [level] [target]` 评当前 diff；`/review [PR]` 评 GitHub PR。[A1] | 没有单独的 documented review command；常用 `/ask`/prompt 与 `/diff` 组合。[D1] |
| verify/test | reference 未列出独立 `/verify`；可由 agent 请求工具运行。[C1] | `/verify` 构建、运行、观察 app；自然语言也可请求测试。[A1][A2] | `/test` 运行 shell；`/lint` 运行并修复。[D1] |

**[Cross-product synthesis / 跨产品综合]** 这张表显示“同名的 diff”也有三个 target
   维度：当前 Git 状态、agent turn 边界、或 branch/PR 比较基准。review 入口还可能
   读取远程 PR，而不是当前本地 workspace；调用方必须显示 target，不能只显示“review
   passed”。

### 4.2 Snapshot, refresh and real-time output

- **[Documented fact / 已文档化事实]** Claude `/diff` 是明确会随外部 Git 状态刷新且能
  在 current Git diff 与 turn diff 间切换的 viewer。[A1]
- **[Documented fact / 已文档化事实]** Codex `/diff` 和 Aider `/diff` 的官方命令描述
  只说明展示当前/最近变化，没有给出 live filesystem watcher 或 JSON diff event
  contract。[C1][D1]
- **[Cross-product synthesis / 跨产品综合]** diff viewer 的“实时”最多表示观察窗口
  会重读版本控制状态；它不表示模型 review 会随每个文件变化重新运行。AI review 的
  token 可能流式显示，但判断仍属于一次模型运行；verify 的 stdout 可能实时打印，
  但最终证据仍是某次命令的 exit/output。
- **[Evidence gap / 证据缺口]** 这些页面没有统一说明 review/verify 是否在运行期间锁定
  diff、发现 Git 状态变化后是否告警、或结果是否带 revision/commit identity。不能把
  viewer 的刷新能力转移给模型 review。

### 4.3 File modification and confirmation

| 动作 | 默认可认为的边界 | 必须单独核验的越界 |
| --- | --- | --- |
| diff viewer | **[Cross-product synthesis]** 展示本地状态，本身不需要模型批准，也不应写文件。 | viewer 是否读取 untracked/ignored；是否输出到持久日志。 |
| AI review | **[Documented fact]** Claude 明确提供无 `--fix` 的 review 与带 `--fix` 的修改分支；Aider 把 `/ask` 与 `/code` 分开。[A1][D1] | Codex `/review` 的工具 policy；review 是否可发 comment、调用 shell 或自动应用建议。 |
| verify/test | **[Documented fact]** Claude `/verify` 运行 app；Aider `/test`/`/run` 运行 shell。[A1][D1] | build/test 生成物、hooks、server、网络、数据库和测试数据的副作用。 |
| review comment | **[Documented fact]** Claude `/code-review --comment` 会写 GitHub inline comments。[A1] | 远程写入是否需要再次确认、失败后是否重试/重复评论。 |

**[Cross-product synthesis / 跨产品综合]** “只读 review”最好理解为“对被审阅的
   workspace 文件不自动应用编辑”，而不是“整个操作没有副作用”。Claude 的 PR
   `/review` 是显式 read-only，但 `/code-review --comment` 仍会写远端；Aider 的
   `/ask` 不编辑文件，但 `/run` 可以执行任意 shell。[A1][D1]

### 4.4 Goal/checklist and transcript are not verification

- **[Cross-product synthesis / 跨产品综合]** goal/plan 是意图，checklist 是进度，
  transcript 是过程，diff 是版本控制观察，test result 是运行证据，review 是模型
  判断。它们可以互相引用，但不能互相替代。
- **[Documented fact / 已文档化事实]** Claude 把 `/plan`、`/tasks`、`/diff`、`/code-review`
  和 `/verify` 分列；Aider 把 `/ask`、`/code`、`/test`、`/lint` 分列；Codex 也将
  `/plan`、`/diff`、`/review` 分列。[A1][C1][D1]
- **[Evidence gap / 证据缺口]** 公开命令页没有统一定义 checklist item 与具体命令的
  绑定、tool transcript 是否包含所有 shell stdout/stderr、或 resume 后能否重建当时
  的 diff/test revision。产品 UI 中的绿色勾选不能作为跨产品的真实测试证明。

### 4.5 Real test result versus model summary

- **[Documented fact / 已文档化事实]** Aider `/test` 明确运行 shell 并关注非零退出；
  Claude `/verify` 明确运行和观察应用，而非只依赖 test/type-check。[D1][A1]
- **[Cross-product synthesis / 跨产品综合]** 可靠的 verify 记录至少应能区分命令文本、
  工作目录/目标 revision、开始结束时间、exit status、stdout/stderr 和 agent 后续
  总结。模型说“tests pass”只是 transcript 中的一句话，除非同时能找到实际命令结果，
  不应升级为独立事实。
- **[Evidence gap / 证据缺口]** 本次官方命令资料没有给 Codex、Claude、Aider 共同的
  结构化 test result schema，也没有给出测试超时、被中断、部分 suite、缓存命中或
  命令自行修改文件时的统一终态语义。

## 5. Failure, empty diff and non-Git workspace

| 场景 | 已知事实 | 证据缺口/边界 |
| --- | --- | --- |
| 空 diff | Claude `/diff`、Codex `/diff`、Aider `/diff` 都是 diff 展示入口。[A1][C1][D1] | 三家命令页都没有统一规定空 diff 的文案、退出码、是否仍可调用 model review；空 diff 不能证明没有 untracked-only changes。 |
| 非 Git workspace | Aider 文档说无 repo 启动时会询问创建 repo，并可用 `--no-git` 关闭 Git integration。[D2] | Codex/Claude `/diff` 在非 Git 目录的确切行为、Claude review 的 fallback、以及 no-git 下 Aider `/diff` 的输出未公开。 |
| 缺失 remote/base | Claude `/security-review` 要求 `origin`，文档列出 `ambiguous argument` 失败路径。[A1] | 该前置条件不能扩展为 `/diff`、`/code-review` 或其他产品的统一规则。 |
| 测试失败 | Aider `/test` 在非零退出时把输出加入 chat；Claude `/verify` 的目的包含实际运行 app。[D1][A1] | 失败后是否自动重试、自动修复、继续执行或变更文件，依赖后续 agent/tool policy；没有统一 result schema。 |
| Git 状态在 review 中变化 | Claude diff viewer 会自动刷新外部变化。[A1] | review 是否锁定输入、是否重新读取、是否拒绝 stale result，三个产品均未在这些页面中说明。 |
| untracked/ignored-only changes | Git diff 这个词通常带有版本控制 target 语义。[C1][A1][D2] | 官方页面没有逐产品说明 untracked/ignored 纳入规则；不能把 diff viewer 当成完整 workspace inventory。 |

**[Cross-product synthesis / 跨产品综合]** 错误处理应至少区分：无变化、目标无法
   解析、Git 命令失败、模型 review 失败、真实命令非零和用户取消。产品文档只对其中
   少数路径给了文字说明；把所有情况压成“review 无发现”会掩盖输入不完整或执行失败。

## 6. Persistence and security limits

- **[Documented fact / 已文档化事实]** Claude conversation 可以本地保存并 resume；
  Aider 的 AI 编辑默认通过 Git commits 保存，且提供 `/undo` 和 Git 操作；这些是
  不同的持久化机制。[A2][D2]
- **[Cross-product synthesis / 跨产品综合]** Git commit、当前工作树、session
  transcript、review finding、test artifact 和远程 PR comment 应视为不同记录。恢复
  conversation 不自动恢复一个仍在运行的测试进程；保存 commit 也不保存当时的模型
  判断或测试环境。
- **[Documented fact / 已文档化事实]** Claude 的 `/code-review ultra` 使用 cloud
  review，`--comment` 写 GitHub；Aider 的 Git 文档明确说明某些 commit-message 操作
  会把 diff 和 chat history 给 weak model；Codex 另有 approval/sandbox 安全说明。[A1][D2][C2]
- **[Cross-product synthesis / 跨产品综合]** “review 文件不写入”与“内容不会离开
  本机”是两条不同保证。只读的本地 viewer 可以不调用模型；模型 review、commit
  message 生成、云端 PR review 和 shell verify 都可能扩大数据边界。
- **[Evidence gap / 证据缺口]** 这组官方页面没有提供三产品可比的 retention、redaction、
  加密、日志访问、模型输入字段或 review artifact 删除合同；不应据此声称任一产品
  默认保存或默认不保存完整 diff/transcript。

## 7. Pitfalls and evidence gaps

- **[Evidence gap / 证据缺口]** “current diff”没有跨产品统一范围。必须明确是工作树、
  staged diff、某个 agent turn、branch base 还是 PR head；否则同一用户在 `/diff`、
  `/code-review`、`/review` 和 `git diff` 之间切换时会比较不同集合。
- **[Cross-product synthesis / 跨产品综合]** read-only 最可靠的表面是本地 diff viewer
  和明确不编辑文件的 ask/review 分支；一旦加入 `--fix`、`--comment`、`/run`、`/lint`、
  测试修复或云端任务，就应重新显示确认和副作用边界。
- **[Evidence gap / 证据缺口]** review 的模型调用、工具调用、上下文文件、重试和
  结果持久化在命令文档中通常只描述到用户体验层，不足以证明“完全没有写权限”或
  “只读输入已经被固定”。
- **[Cross-product synthesis / 跨产品综合]** 真实测试结果应携带运行身份和实际输出；
  transcript/plan/checklist 只能作为解释层。将模型摘要或 checklist 勾选当成 test
  result，是这类 UI 最容易造成的语义混淆。
- **[Evidence gap / 证据缺口]** 本研究以官方产品文档为主，没有对所有版本、操作系统、
  Git 状态和权限模式做黑盒复现；访问日期只固定证据快照，不能替代版本化验收。

## References

All sources accessed 2026-08-05.

- [C1] OpenAI, [Codex CLI reference](https://developers.openai.com/codex/cli/reference/),
  official command reference for `/diff`, `/review`, `/plan` and related CLI behavior.
- [C2] OpenAI, [Agent approvals and security](https://developers.openai.com/codex/agent-approvals-security),
  official description of approvals, sandbox and tool execution boundaries.
- [A1] Anthropic, [Claude Code commands](https://code.claude.com/docs/en/commands),
  official command reference for `/diff`, `/code-review`, `/review`, `/verify`,
  `/security-review`, `/plan` and `/tasks`.
- [A2] Anthropic, [Claude Code common workflows](https://code.claude.com/docs/en/common-workflows),
  official workflows for plan-before-editing, testing, session persistence and local work.
- [D1] Aider, [In-chat commands](https://aider.chat/docs/usage/commands.html),
  official command reference for `/ask`, `/code`, `/diff`, `/git`, `/lint`, `/run` and `/test`.
- [D2] Aider, [Git integration](https://aider.chat/docs/git.html), official documentation
  for repository setup, automatic commits, dirty-file handling, diff/undo and `--no-git`.
