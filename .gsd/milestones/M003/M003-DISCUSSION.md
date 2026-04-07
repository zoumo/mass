# M003 Discussion Log

## Exchange — 2026-04-07T09:17:48.308Z

### Codex Proof

在 M003 里，你希望 Codex 被推进到什么证明级别？

- **One real path (Recommended)** — 至少跑通一条真实 Codex ACP 路径，作为 stronger validation。
- **Contract plus smoke** — 只做到契约一致和轻量验证，不要求完整真实端到端。
- **Defer to M003** — 这轮先不碰 Codex 的真实验证，留到后一个 milestone。

**Selected:** One real path (Recommended)

### Recovery Bar

恢复/安全这轮的完成标准，你更想把证明打到哪里？

- **Restart-proof (Recommended)** — 重点证明 agentd restart 后的重连、补历史、状态重建、清理安全都成立。
- **Crash-plus-damage** — 除了 restart，还要覆盖损坏日志、异常退出、尾部坏数据等更硬的场景。
- **Spec and tests** — 先把契约和自动化测试做扎实，不强求这轮做更重的运行证明。

**Selected:** Restart-proof (Recommended)

### Meta Store

元数据库这块，M003 里你想怎么处理？

- **Keep SQLite (Recommended)** — 继续用 SQLite，只做恢复和安全加固，不动后端方向。
- **Abstract store** — 保留 SQLite，但顺手抽象 store，为以后切换做准备。
- **Reopen backend** — 把后端选择重新拉回本 milestone，认真评估甚至推动替换。

**Selected:** Keep SQLite (Recommended)

---
## Exchange — 2026-04-07T09:21:22.247Z

### Restart Mode

I’d lean toward making live-shim reconnect the hard requirement for this milestone, because it matches the current socket/state layout. Does that match your intent?

- **Live shim reconnect (Recommended)** — Treat agentd restart + reconnect to still-running shims as the must-prove path; cold resume stays later.
- **Reconnect plus load** — Require both live-shim reconnect and `session/load`-style cold resume where agents support it.
- **Cold resume first** — Bias toward restart via `session/load`, even if live-shim reconnect becomes secondary.

**Selected:** Live shim reconnect (Recommended)

### Codex Bar

For the one real Codex path, what is the minimum bar that would actually count for you?

- **Prompt round-trip (Recommended)** — Codex should complete one real prompt flow, not just bootstrap and status.
- **Bootstrap plus status** — Codex only needs to start cleanly and expose truthful state in this milestone.
- **Full restart proof** — Codex must also survive the same restart/reconnect path this milestone proves for other clients.

**Selected:** Prompt round-trip (Recommended)

### Safety Model

On the safety side, I’d rather fail closed than silently guess. What posture do you want when recovery state is uncertain?

- **Fail closed (Recommended)** — If recovery truth is uncertain, block cleanup or resume and surface explicit degraded state.
- **Best effort plus warnings** — Prefer continuing with strong diagnostics, even if some state may be stale.
- **Mixed by surface** — Fail closed on destructive actions, but allow non-destructive status and attach paths.

**Selected:** Fail closed (Recommended)

---
## Exchange — 2026-04-07T09:30:15.693Z

### Depth Check

Did I capture the depth right?

- **Yes, you got it (Recommended)** — The milestone intent, proof bar, and safety posture are captured correctly.
- **Not quite — let me clarify** — Something important still needs correction before writing.

**Selected:** Yes, you got it (Recommended)

---
