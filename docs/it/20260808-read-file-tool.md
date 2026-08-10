# workspace read_file 工具

## 背景

主流 coding agent 通常把文件读取作为一等工具，而不是要求模型通过 shell 拼接 `cat`、`head` 或重定向。项目已有 edit/patch 的工作区边界，但缺少对应的只读文件入口。

## 本轮变更

- 新增 `read_file`，只接受工作区相对路径，并复用 edit_file 的路径、symlink 和 workspace boundary 校验。
- 支持 `offset` / `max_bytes` 分页读取，默认 16 KiB、最大 64 KiB，并返回 `has_more` / `truncated`。
- UTF-8 内容直接返回；非 UTF-8 内容以 base64 返回并标注 `encoding`。
- 注册到默认 ReAct 工具集；工具本身只读，不触发 permission request。

## 边界

这是 bounded 文件读取，不是任意目录浏览或沙箱；shell 工具仍保持独立的权限和执行策略。大文件应使用 offset 分页，模型不能假设一次读取获得完整内容。
