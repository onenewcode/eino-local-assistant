# workspace search_files 工具

## 背景

文件读取已经是一等工具，但 agent 仍需要通过 shell `rg`/`grep` 才能定位代码，结果解析依赖终端文本且容易超出上下文。主流 coding agent 通常提供带路径和行号的搜索工具。

## 本轮变更

- 新增 `search_files`，在配置工作区内执行正则搜索。
- 支持可选 `path`、文件 `glob`、`max_results` 和 `context_lines`；默认最多 100 条，最多 1000 条，上下文最多各 5 行。
- 返回相对路径、1-based 行号、匹配文本、扫描文件数和 `truncated`。
- 命中行可附带 bounded 的前后上下文，减少模型为理解命中位置而重复读取文件。
- 默认跳过 `.git` 目录；超长匹配行会 bounded 截断并标记 `text_truncated`。
- 注册到默认 ReAct 工具集，保持只读，不触发 permission request。

## 边界

这是受结果数量和行文本限制的工作区搜索，不是完整 ripgrep 替代品；复杂 glob/二进制分析仍可通过 `run_command` 补充，但模型应使用 `read_file` 获取命中上下文。
