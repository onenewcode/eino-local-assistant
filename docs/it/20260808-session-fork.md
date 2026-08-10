# 迭代：会话 fork

日期：2026-08-08

## 背景

主流 code agent 支持在保留当前上下文的同时尝试另一条实现路线。已有 `/new` 只能创建空会话，`/clear` 也会丢弃当前工作视图，无法安全地从一个已完成 thread 分支。

## 实现

- 新增 `/fork [title]`，只允许在 idle 时执行，并切换 TUI 到新建的子 thread。
- `ThreadStore.ForkThread` 从父 thread 读取完整可见消息快照，在新 journal 中以明确的 `fork snapshot` turn 持久化；父 thread 不复制、不改写、不共享 writer。
- 子 thread 保留父 system prompt 和 model 元数据，可继续独立写入、恢复和删除；默认标题为 `Fork of <title-or-id>`。
- 活动 turn 的父 thread 会拒绝 fork，避免半成品工具生命周期进入子会话。
- checkpoint/artifact 的物理文件不共享；子 thread 从可见消息快照重新开始，后续可按自身上下文策略压缩。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
