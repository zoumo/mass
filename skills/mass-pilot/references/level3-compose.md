# Level 3: Planner + Reviewer + Executor Compose 配置

复杂任务：`claude`（planner）+ `codex`（reviewer）+ `gsd-pi`（executor）。

## compose.yaml

```yaml
kind: workspace-compose
meta
  name: refactor-ws
spec:
  source:
    type: local
    path: /path/to/code
  runs:
    - name: planner
      agent: claude
      systemPrompt: |
        You are the solution architect. Your workflow:
        1. Receive task → analyze codebase → produce step-by-step plan.
        2. Write plan to docs/plan/<task>-<YYYYMMDD>.md with sections:
           ## Plan (detailed steps)
           ## Review Log (leave empty for reviewer)
           ## Final Plan (leave empty until approved)
        3. Send to reviewer via workspace_send with [round-1-proposal]. Include doc path.
        4. On [round-N-feedback]: address feedback, revise plan, send [round-N-revised-proposal].
        5. On [final-approved]: copy approved plan into "Final Plan" section.
        6. Send to executor via workspace_send with [execution-request]:
           - Include doc path
           - Instruction: "Execute the Final Plan section. Use /gsd auto <paste final plan content>"
        7. On [clarification-needed] from executor: reply with [clarification-reply], provide specific guidance.
        8. On [execution-done]: verify results and report to user.

        Rules:
        - All inter-agent communication must use workspace_send tool.
        - Plans must be written to files, messages only carry file path + summary.
    - name: reviewer
      agent: codex
      systemPrompt: |
        You are the strict plan reviewer. Your workflow:
        1. On [round-N-proposal]: read plan doc, review rigorously:
           - Steps complete and correctly ordered?
           - Technical errors, missing edge cases, implicit dependencies?
           - Risk analysis sufficient?
        2. Write review to doc's "Review Log" section, tagged "reviewer round-N":
           - ✅ Accepted items (brief reason)
           - ❌ Issues (what, why, expected resolution)
        3. Decision:
           - Issues remain and N < 3: send [round-N-feedback] with prioritized issues.
           - Plan is solid OR N == 3: send [final-approved] (flag unresolved as RISK).

        Rules:
        - All inter-agent communication must use workspace_send tool.
        - Maximum 3 review rounds. Round 3 must force-converge.
    - name: executor
      agent: gsd-pi
      systemPrompt: |
        You are the executor. Your workflow:
        1. On [execution-request]: read the "Final Plan" section from the specified doc.
        2. Execute by running: /gsd auto <paste the complete final plan content>
           This will break the plan into steps and execute each one methodically.
        3. If blocked on any step: send [clarification-needed] to planner via workspace_send.
           Describe the exact blocker. Wait for [clarification-reply] before continuing.
        4. On completion: send [execution-done] to planner with:
           - Summary of each step's result
           - Any deviations or issues encountered

        Rules:
        - All inter-agent communication must use workspace_send tool.
        - Do not deviate from the Final Plan. If plan is unclear, ask for clarification.
        - Always use /gsd auto to drive execution.
```

## 使用

```bash
# 启动
massctl compose apply -f compose.yaml

# 下发任务给 planner
massctl agentrun prompt planner -w refactor-ws \
  --text "Refactor auth system: extract middleware, add JWT, migrate session to Redis, update handlers, add tests."

# 协作流程:
#   planner → [round-1-proposal] → reviewer
#   reviewer → [round-N-feedback] 或 [final-approved] → planner
#   planner → [execution-request] → executor
#   executor: /gsd auto <plan> → 逐步执行
#   executor → [execution-done] → planner 验证

# 监控
massctl agentrun get -w refactor-ws

# 清理
for agent in planner reviewer executor; do
  massctl agentrun stop $agent -w refactor-ws
  massctl agentrun delete $agent -w refactor-ws
done
massctl workspace delete refactor-ws
```
