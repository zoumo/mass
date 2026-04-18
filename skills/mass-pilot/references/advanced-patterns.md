# Level 4: 高级编排模式

自定义编排的常见模式和设计原则。

## 设计原则

1. **按能力拆分** — 每个 agent 职责清晰不重叠
2. **claude 做协调者** — 理解上下文最好，路由工作给专家
3. **codex 守所有审查关卡** — 它能发现别人遗漏的问题
4. **gsd-pi 做所有执行** — 用 `/gsd auto <计划>` 驱动
5. **审查最多 3 轮** — 第 3 轮强制收敛，标记 RISK
6. **每条消息加 Tag** — agent 据此判断下一步动作
7. **计划写文件** — 消息中传文件路径，不传全文
8. **一个 workspace 对应一个完整任务** — 不混杂无关工作

## 模式一：并行执行

多个 executor 同时处理独立子任务。

```yaml
agents:
  - meta { name: planner }
    spec:
      agent: claude
      systemPrompt: |
        Split the task into independent subtasks.
        Send each subtask as [execution-request] to a different executor.
        Wait for all [execution-done] messages, then verify and report.
  - metadata: { name: reviewer }
    spec: { agent: codex, systemPrompt: "..." }
  - meta { name: executor-api }
    spec:
      agent: gsd-pi
      systemPrompt: "Execute API-related subtasks via /gsd auto."
  - metadata: { name: executor-db }
    spec:
      agent: gsd-pi
      systemPrompt: "Execute database-related subtasks via /gsd auto."
  - metadata: { name: executor-test }
    spec:
      agent: gsd-pi
      systemPrompt: "Execute test-writing subtasks via /gsd auto."
```

Planner 分发 `[execution-request]` 给不同 executor，各自独立执行，完成后回报。

## 模式二：流水线

每个 agent 处理一个阶段，结果传递给下一个。

```
Agent A (生成) → Agent B (转换) → Agent C (验证)
```

用 Tag 标记流水线阶段：`[stage-1-done]` → `[stage-2-done]` → `[stage-3-done]`

## 模式三：多仓库协调

每个仓库一个 workspace，用 CLI 脚本跨 workspace 下发任务。

```bash
# 分别创建 workspace
bin/massctl workspace create local --name api-ws --path /path/to/api
bin/massctl workspace create local --name frontend-ws --path /path/to/frontend

# 各自启动 agent
bin/massctl agentrun create -w api-ws --name api-worker --agent claude --system-prompt "..."
bin/massctl agentrun create -w frontend-ws --name fe-worker --agent claude --system-prompt "..."

# 协调：先改 API，再改前端
bin/massctl agentrun prompt api-worker -w api-ws --text "Add new endpoint..." --wait
bin/massctl agentrun prompt fe-worker -w frontend-ws --text "Update client to use new endpoint..." --wait
```

## 自定义 Agent 定义

内置 agent 不够用时，创建自定义 agent：

```bash
bin/massctl agent apply -f my-agent.yaml
```

```yaml
metadata:
  name: my-custom-agent
spec:
  command: /path/to/custom-agent-binary
  args: ["--flag", "value"]
  env:
    - "API_KEY=xxx"
  startupTimeoutSeconds: 60
```

创建后即可在 agentrun create 或 compose 中引用 `--agent my-custom-agent`。
