# 项目规则 digest 与恢复漂移

## 背景

恢复 session 必须保持历史 system prompt 不变，但当前 workspace 的 `AGENTS` 文件可能已经修改。若没有可见提示，用户容易误以为恢复会话已经使用最新规则。

## 本轮变更

- 对当前加载到的项目规则内容计算 12 位 SHA-256 digest。
- TUI 状态栏和 `/status` 显示 `rules=<digest>`。
- 恢复 session 时若当前规则生成的 system prompt 与 ledger 中的 prompt 不同，显示 `rules=stale`。
- 不自动改写恢复 session；新建 session 和 `/new` 仍使用当前规则。

## 边界

digest 只用于可见性和漂移诊断，不是规则签名或安全证明。当前不提供在原 session 内刷新 immutable system prompt 的操作。
