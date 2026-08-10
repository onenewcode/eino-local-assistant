# TUI `/skills`

本轮为交互式 TUI 增加 `/skills` 只读命令，用于查看当前工作区已发现的项目 skills。

## 行为

- 复用 registry 中的 `list_skills` 工具，确保 TUI 与 agent 使用同一套目录优先级、去重和数量上限。
- 只输出 skill 名称、相对路径和摘要，不自动把 `SKILL.md` 内容注入当前 prompt。
- `/skills` 不接受参数；没有发现 skill 时明确显示空目录状态。
- 当前 turn 忙碌时仍立即执行，不进入 FIFO；skill 详情继续由模型按需调用 `read_skill`。

## 取舍

命令输出使用边界清晰的纯文本目录，避免在 TUI 中重复实现 skill 发现逻辑。skill 文件属于项目数据，读取时仍受既有工具和权限边界约束，不改变系统规则或项目安全规则。

## 验证

已覆盖 slash catalog、参数校验、callback 输出以及 busy 状态下的立即执行分类，并运行仓库规定的测试、构建和 lint 门槛。
