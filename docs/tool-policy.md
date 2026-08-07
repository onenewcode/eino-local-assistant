# 工具规则与影响分级

工具授权、命令影响和 OS 隔离是三件不同的事。本产品使用 Codex `execpolicy`
兼容的 `.rules` 前缀规则子集决定 shell 命令是否需要审批；不会把自定义字段写进
规则文件，也不会用 `AGENTS.md` 或 TOML 的 `[permissions]` 充当执行约束。

## 规则来源

需要加载运行配置的启动路径会先初始化用户模板，随后按低到高优先级读取下列目录中所有
`*.rules` 文件：

1. `~/.eino-assistant/rules/`（用户层）；首次启动仅在这里创建
   `~/.eino-assistant/rules/default.rules`，若该文件已存在绝不覆盖。
   内置文件是 Eino 自有的零授权起始模板，仅含说明注释，不含任何 `allow` 规则，也不
   复制 Codex 用户规则或个人审批历史。这是本产品的保守初始化设计，不是“Codex
   默认配置”或业界标准的声明。
2. `<workspace>/.eino-assistant/rules/`（项目层）；仅当用户在
   `~/.eino-assistant/config.toml` 中显式信任该绝对工作区时读取，程序绝不创建项目
   规则或信任记录：

   ```toml
   [projects."/absolute/workspace"]
   trust_level = "trusted"
   ```

   未记录或 `trust_level = "untrusted"` 时，项目规则完全不参与授权，不能由仓库
   内容自行授予 `allow`。

   该信任记录只能来自用户目录中的全局配置文件，不是工作区 `config.toml` 的功能开关。
   工具授权加载器会严格校验该表中的绝对路径和 `trusted` / `untrusted` 值；工作区内容
   即使写出同名表，也不会成为项目规则的信任来源。运行配置与信任记录共用
   `~/.eino-assistant/config.toml`，项目内不存在运行配置覆盖入口。

同一目录按路径字典序加载。每条匹配规则都会参与结果，不是“后一条覆盖前一条”；
最终取最严格的决策：`forbidden > prompt > allow`。加载器会拒绝它实际使用的符号链接：
用户层的规则目录和 `*.rules` 文件，以及项目层的
`<workspace>/.eino-assistant` 控制目录、其 `rules` 目录和 `*.rules` 文件都必须是
常规目录或文件。该检查防止显而易见的路径重定向；它不是针对同权限并发改写的无竞态
文件系统安全边界，常规执行的强制边界仍是 sandbox。

`host_executable` 声明同样按所有已加载规则文件累积；后续规则文件可复用此前声明的
绝对可执行路径，不会因同名声明覆盖此前路径。

项目规则是显式授权配置，不是项目指令。它与项目根目录的 `AGENTS.md` 没有加载或
语义耦合。

## Codex-compatible 语法

规则是 Starlark。内置的 `internal/tools/rules/default.rules` 会被编译进二进制，安装
到新用户目录时原样初始化；因此不依赖其他 agent 的配置目录。当前 shell 授权实现
Codex `execpolicy` 的公开 `prefix_rule` 和 `host_executable` 语义，而非宣称支持其
所有 builtin。

```starlark
prefix_rule(
    pattern = ["git", ["status", "log"]],
    decision = "prompt",  # allow | prompt | forbidden；省略时 allow
    justification = "检查仓库状态前确认。",
    match = ["git status --short", ["git", "log"]],
    not_match = ["git show"],
)

host_executable(
    name = "git",
    paths = ["/usr/bin/git"],
)
```

`pattern` 是有序 argv token；每个 token 可以是字符串或字符串备选列表。`match`
和 `not_match` 是装载时验证示例：前者必须匹配，后者不得匹配。`host_executable`
限制绝对可执行路径何时可回退到 basename 规则。`network_rule` 当前会被拒绝：本产品
没有把它连接到可执行的网络授权点，接受它会错误地暗示网络请求已受该规则约束。网络
可达性只由 sandbox 的域名边界强制。未知 builtin、未知字段以及 `impact`、`tool_rule`、
不属于 `prefix_rule` 的 `ask` / `deny` 决策都会使启动失败，而不是被静默忽略；这不对
Codex 其他 builtin（例如其 `network_rule`）的独立决策词作泛化判断。

前缀规则只适用于有 argv 的 shell 命令。`apply_patch` 是结构化文件编辑工具，不
伪造为一条 shell 规则；它遵循当前审批模式、工作区路径约束、符号链接防护和
sandbox。

## 未命中与审批

规则未命中时，授权层先使用内置的已知只读命令判断；仍无法证明安全的调用会进入
普通审批流。`approval_policy = "never"` 不会绕过匹配到的 Codex
`decision = "prompt"`：该调用会被结构化拒绝，因为没有可显示给用户的审批通道。
它只保留未命中的普通审批回退，以配合受限 sandbox 的非交互自动执行。

成功匹配的 `forbidden` 规则在每种审批模式下都会被拒绝；会话内的“允许本次/本会话”
只可复用用户对普通审批回退的确认，不能覆盖 `forbidden` 或 `prompt` 规则。
`hardShellSafetyDeny` 另有一小组基于字符串的命令检查，用作纵深防护；它不是 shell
解析器，不能识别全部引用、转义或间接执行形式，尤其不能在 yolo 下被视作宿主安全
边界。

## 内置影响分类

`ToolImpact` 是 agent 内部、不可配置的派生分类，供任务状态与 plan 模式使用：

| 等级 | 含义 | 示例 |
| --- | --- | --- |
| `read_only` | 能证明每个简单命令段均为只读 | `git status`、受限 `find`、`rg`、`go version` |
| `workspace_write` | 可能修改工作区或无法证明只读 | `touch`、重定向写入、`git diff --output`、复杂 shell 语法 |
| `external_side_effect` | 已识别的远程或发布行为 | `git push`、`curl`、`ssh`、`npm publish` |

分类器可拆分 `&&`、`||`、`;`、`|` 组成的简单命令，并取最高影响。变量展开、命令
替换、后台执行、输入/输出重定向、未知 wrapper 或危险选项均保守地归为
`workspace_write`；已识别的外部行为取最高等级。`apply_patch` 始终报告
`workspace_write`。

这一分类不授予授权。它和 Codex 规则决策并列但不混合：规则回答“能否执行”，分类
回答“已执行或计划执行的命令可能产生什么影响”。

旧 TOML `[permissions]` 表没有兼容模式。启动会先在用户目录初始化零授权
`default.rules`，再明确提示删除该表：shell 前缀授权迁移到用户层 `.rules`；
`apply_patch` 的旧 allow/deny 没有一对一的规则等价物，应使用 `approval_policy`、
`[sandbox].protected_paths` 与现有 workspace 路径边界重新表达。旧
`runtime.max_react_steps` 也须改为 `runtime.max_model_steps`；前者的图执行步数语义不再
暴露，后者只计算 tools-enabled 模型响应，实际工具执行由 `runtime.max_tool_calls` 单独
限制。静默接受旧字段会造成“配置看起来生效、实际没有授权边界”的误解，因此不采用。

## Plan、任务账本与执行边界

plan 模式仅允许同时满足三项的 shell 调用：授权层为 allow、影响为 `read_only`、且
存在实际 enforced 的 OS 只读 sandbox。`apply_patch` 在 plan 模式一律拒绝。任何
未知或复杂调用都 fail-closed，不会退回 host 执行。

每个 shell 结果都会保存分类。任务控制器和崩溃恢复只把完成的
`impact = "read_only"` shell 调用视为不改变工作区；旧记录、缺失分类、异常记录和
`apply_patch` 都保持保守处理。这避免纯检查命令被错误地当作写入，从而不必要地触发
“先建计划”的任务门槛。

启用的 OS sandbox 才是常规模式的强制执行边界；工作区 cwd/path 钳制、符号链接检查、
受保护路径和字符串级命令检查是额外防护。规则文件和模型提示都不能覆盖已启用的
sandbox 或工具路径校验。yolo 会跳过 OS sandbox 和普通审批，只保留这些尽力而为的
检查，因此必须被视为不安全的宿主执行旁路，而不是受硬安全保证的模式。
