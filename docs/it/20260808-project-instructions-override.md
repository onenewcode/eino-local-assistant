# 项目规则 override

## 背景

仅有祖先链 `AGENTS.md` 无法表达目录级替换和未提交的本地规则。Codex 风格项目常用 `AGENTS.override.md` 做同目录替代，并用 gitignored local 文件保存个人补充。

## 本轮变更

- 同一目录存在 `AGENTS.override.md` 时，不再加载该目录的 `AGENTS.md`。
- `AGENTS.local.md` 在该目录的主规则之后追加，适合本地未提交规则。
- 祖先目录到当前目录的顺序、128 KiB 单文件限制和新建/恢复 session 语义保持不变。

## 边界

规则文件只影响模型上下文，不改变 permission handler 或 workspace 约束；项目规则不能授予额外权限。
