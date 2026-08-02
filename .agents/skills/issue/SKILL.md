---
name: issue
description: >
  仅用于本仓库（eino-local-assistant）的故障 issue。用户明确要求写 issue、记 issue、
  记根因或记录本仓库故障时使用。禁止在用户未要求时擅自创建 docs/issues。
  默认排障只对话说明并修代码。
---

# issue（本仓库故障记录）

**Issues 只有用户。** 用户未明确要求时，不要创建或修改 `docs/issues/*`。

只记录 **本仓库 agent** 相关故障（会话账本、ReAct/工具、TUI、provider、上下文等），不是通用 issue 模板站。

## 何时写

仅当用户明确说例如：

- 写 issue / 记 issue
- 记根因 / 记录这个问题
- 把故障写成文档

## 怎么写

1. 路径：`docs/issues/<slug>.md`
2. 一个问题一个文件
3. 建议结构：现象、根因、影响、规避、修复/验收（尽量点到本仓库路径/模块）
4. 中文优先；可参考 `docs/issues/tool-calls-history-pollution.md`

## 默认排障（无 issue）

用户只是报错或让修 bug 时：

- 对话里说明原因
- 直接修代码
- **不**自动开 issue
