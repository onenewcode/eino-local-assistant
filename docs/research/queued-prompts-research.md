# 排队提示词：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-10；采用前应重新核验。
>
> 范围：交互式 coding agent 在一个 turn 运行期间如何暂存、展示、选择发送和移除后续提示词。
>
> 不在范围：provider 的服务端排队协议、多客户端一致性保证、以及任何本仓库的实现映射。

## 1. 结论

- **事实：** Grok Build 的开源 TUI 有单独的 queue pane，用编号行展示待发提示词；行保留选择状态，而不是把每个待发项写作普通聊天记录。[queue pane source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/views/queue_pane.rs)（访问日期：2026-08-10）。
- **事实：** Grok Build 为被选中的队列项提供移除和 `send now` 流程；其 `force_interject_queue_row` 注释将运行中发送定义为 cancel-and-send，即先取消当前 turn，再运行该项。[agent queue source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/app/agent_view/queue.rs)（访问日期：2026-08-10）。
- **综合：** 待发内容和已提交聊天记录应是不同状态。队列操作应先改变队列，只有真正开始处理的提示词才进入普通会话流；这避免“已发送”的错误暗示。
- **综合：** 立即发送是明确的抢占操作，应让用户看见它会影响正在运行的工作；删除则必须保证该项不再被传输或自动 drain。

## 2. 已部署应用的证据

### Grok Build：队列是可选择的独立面板

- **事实：** `queue_pane.rs` 定义 `QueuedPromptEntry`，以稳定 ID、1-based position、完整文本和首行预览构建列表项；多行提示词带额外行数提示。该结构支持选择、搜索/复制和按行操作，而不仅是单行 toast。[source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/views/queue_pane.rs)（访问日期：2026-08-10）。
- **事实：** `try_send_now_queued_from_prompt` 在运行 turn 和空 composer 的条件下，选取可见队列的首项，并调用同一条强制发送路径；说明快捷操作与面板操作共享相同的状态转换。[source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/app/agent_view/queue.rs)（访问日期：2026-08-10）。
- **事实：** Grok Build 的 PTY 端到端测试在运行 turn 时移除队列项，并断言它既不继续显示，也不抵达用户消息 wire；这是“取消”不是只隐藏 UI 的可观察契约。[test](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/tests/pty_e2e/removed_queued_prompt_never_sent.rs)（访问日期：2026-08-10）。

## 3. 机制与权衡

- **事实：** Grok Build 将本地 pending prompt 与服务端 shared queue 区分来源；移除和重排会根据来源走不同通道，并维护稳定选择 ID。[agent queue source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/app/agent_view/queue.rs)（访问日期：2026-08-10）。
- **综合：** 单进程 FIFO 可以只维护本地状态，但仍应在选中项删除、自动发送或清空后归一化选择，避免光标指向已不存在的行。
- **综合：** cancel-and-send 的好处是优先级明确，代价是丢弃当前 turn 的未完成工作；它不应伪装成无副作用的普通 FIFO 重排。
- **综合：** 队列面板占用垂直空间，窄终端需要截断预览、限制可见行数，并让滚动 transcript 仍保持可用。

## 4. 跨产品综合

- **综合：** 可操作队列的核心信息是顺序、选中项和后果：用户需要知道哪一项会先发送、`send now` 是否抢占、以及取消是否永久阻止发送。
- **综合：** 队列显示在 composer 邻近位置可以把它清晰地表达为“尚未提交的输入”；把它显示在 transcript 中会混淆输入生命周期和会话历史。
- **综合：** 直接键盘操作应有可发现的 focus 入口，并保留既有的中断键优先级；运行中 Esc 的含义不能因队列可见而发生隐式改变。

## 5. 陷阱与证据缺口

- **事实：** Grok Build 需要同时处理本地和服务端队列、异步回显与版本化移除；这些协议细节不能从其 pane 外观推断到其他应用。[agent queue source](https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/app/agent_view/queue.rs)（访问日期：2026-08-10）。
- **证据缺口：** 本次无法取得 Codex 官方文档站的可核验队列交互资料（访问时 TLS 失败）；因此不声称 Codex 有相同的待发消息面板或快捷键。
- **证据缺口：** OpenCode 的公开文档检索未提供足以确认其运行中提示词队列、选择与抢占语义的一手材料；不以缺失文档推断其不存在该功能。
- **综合：** 测试应覆盖已取消项永不开始、立即发送前不进入 transcript、以及自动 drain 与用户选择并发时的顺序，而不只断言面板文字。

## References

- xAI, Grok Build `queue_pane.rs`, commit `75e73f3d6ac0350d211f12ae7d57c2c0aad72576`, accessed 2026-08-10: <https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/views/queue_pane.rs>
- xAI, Grok Build `agent_view/queue.rs`, commit `75e73f3d6ac0350d211f12ae7d57c2c0aad72576`, accessed 2026-08-10: <https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/src/app/agent_view/queue.rs>
- xAI, Grok Build `removed_queued_prompt_never_sent.rs`, commit `75e73f3d6ac0350d211f12ae7d57c2c0aad72576`, accessed 2026-08-10: <https://github.com/xai-org/grok-build/blob/75e73f3d6ac0350d211f12ae7d57c2c0aad72576/crates/codegen/xai-grok-pager/tests/pty_e2e/removed_queued_prompt_never_sent.rs>
