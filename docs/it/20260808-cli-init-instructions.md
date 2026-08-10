# CLI project instruction init

本轮增加 `init` 子命令，用于在新项目中建立项目级 agent 指令文件。

## 行为

- `eino-assistant init` 默认创建当前目录的 `AGENTS.md`。
- `eino-assistant init path/to/file.md` 支持显式指定目标路径。
- 使用独占创建，目标已存在时明确拒绝覆盖，不提供隐式 force 行为。
- 模板只包含通用工作约定和验证提示；项目负责人可以在创建后按仓库实际规则编辑。
- 命令不读取模型配置、不启动模型，也不修改已存在的项目规则文件。

## 取舍

仓库规则加载器已经支持 `AGENTS.md`、override 和 local 文件；`init` 只负责安全生成起点，不试图猜测项目语言、测试命令或权限策略。

## 验证

已覆盖 root/command help、模板创建、内容检查和重复执行拒绝覆盖，并运行仓库规定的测试、构建和 lint 门槛。
