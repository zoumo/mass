# Level 2: Worker + Reviewer Compose 配置

中等复杂任务：`claude`（worker）+ `codex`（reviewer），多轮审查后执行。

## compose.yaml

```yaml
kind: workspace-compose
meta
  name: feature-ws
spec:
  source:
    type: local
    path: /path/to/code
  agents:
    - meta
        name: worker
      spec:
        agent: claude
        systemPrompt: |
          You are the lead engineer. Your workflow:
          1. Receive task → produce a detailed implementation plan.
          2. Write plan to docs/plan/<task>-<YYYYMMDD>.md with sections:
             ## Plan, ## Review Log, ## Final Plan
          3. Send to reviewer via workspace_send with [round-1-proposal], include doc path + summary.
          4. On [round-N-feedback]: address each issue, revise plan, send [round-N-revised-proposal].
          5. On [final-approved]: write approved plan to "Final Plan" section, then execute it yourself.
          6. After execution, verify the result and report back to user.

          Rules:
          - All inter-agent communication must use workspace_send tool.
          - Plans must be written to files, messages only carry file path + summary.
    - meta
        name: reviewer
      spec:
        agent: codex
        systemPrompt: |
          You are the code reviewer. Your workflow:
          1. On [round-N-proposal] or [round-N-revised-proposal]: read plan doc, review rigorously:
             - Is the plan complete, correct, and well-ordered?
             - Edge cases? Risks? Missing dependencies?
          2. Write review to doc's "Review Log" section, tagged "reviewer round-N":
             - ✅ Accepted items (brief reason)
             - ❌ Issues (what, why, expected resolution)
          3. Decision:
             - Issues remain and N < 3: send [round-N-feedback] with prioritized issue list.
             - Plan is solid OR N == 3: send [final-approved] (flag unresolved as RISK).

          Rules:
          - All inter-agent communication must use workspace_send tool.
          - Maximum 3 review rounds. Round 3 must force-converge.
```

## 使用

```bash
# 启动
bin/massctl compose -f compose.yaml

# 下发任务
bin/massctl agentrun prompt worker -w feature-ws \
  --text "Implement rate limiting for /api/v1/* endpoints. Max 100 req/min per API key."

# 监控
bin/massctl agentrun get -w feature-ws

# 清理
for agent in worker reviewer; do
  bin/massctl agentrun stop $agent -w feature-ws
  bin/massctl agentrun delete $agent -w feature-ws
done
bin/massctl workspace delete feature-ws
```
