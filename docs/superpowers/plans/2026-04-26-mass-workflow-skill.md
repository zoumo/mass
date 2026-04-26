# mass-workflow Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a `mass-workflow` skill that executes declarative YAML-defined multi-agent pipelines via the massctl task API.

**Architecture:** The skill is invoked with a workflow YAML path; Claude (as orchestrator) parses the YAML, creates workspace + agents via massctl, executes stages in order (serial or parallel), routes between stages via response.status, collects output artifacts, and cleans up. All logic lives in the LLM skill instructions (SKILL.md) plus a standalone poll-task.sh shell script.

**Tech Stack:** Bash, massctl CLI, jq, YAML (human-authored config), Markdown (skill instructions)

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `skills/mass-workflow/SKILL.md` | Create | Main skill: trigger, execution flow, orchestration logic |
| `skills/mass-workflow/scripts/poll-task.sh` | Create | Standalone task polling script |
| `skills/mass-workflow/references/workflow-schema.md` | Create | Complete YAML field reference |

---

## Task 1: Create directory structure

**Files:**
- Create: `skills/mass-workflow/scripts/.gitkeep`
- Create: `skills/mass-workflow/references/.gitkeep`

- [ ] **Step 1: Create directories**

```bash
mkdir -p skills/mass-workflow/scripts
mkdir -p skills/mass-workflow/references
```

- [ ] **Step 2: Verify structure**

```bash
find skills/mass-workflow -type d
```

Expected output:
```
skills/mass-workflow
skills/mass-workflow/scripts
skills/mass-workflow/references
```

- [ ] **Step 3: Commit**

```bash
git add skills/mass-workflow/
git commit -m "feat(mass-workflow): scaffold skill directory structure"
```

---

## Task 2: Create poll-task.sh

**Files:**
- Create: `skills/mass-workflow/scripts/poll-task.sh`

This script is standalone — it does NOT reference or import from mass-pilot. It polls a task until completion, agent error, or timeout.

- [ ] **Step 1: Write the script**

Create `skills/mass-workflow/scripts/poll-task.sh`:

```bash
#!/usr/bin/env bash
# Poll a task until the agent completes it or an error/timeout occurs.
#
# Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval=10] [timeout=1800]
#
# Exit codes:
#   0 — task completed (completed==true), read response.status for routing
#   1 — agent idle but task not completed after max retries
#   2 — agent in error/stopped state
#   3 — timeout

set -euo pipefail

WORKSPACE="${1:?Usage: poll-task.sh <workspace> <agent-name> <task-id> [interval] [timeout]}"
AGENT_NAME="${2:?Missing agent-name}"
TASK_ID="${3:?Missing task-id}"
INTERVAL="${4:-10}"
TIMEOUT="${5:-1800}"

MAX_IDLE_RETRIES=2
idle_retry_count=0
elapsed=0

get_task() {
  massctl agentrun task get -w "$WORKSPACE" --name "$AGENT_NAME" --id "$TASK_ID" -o json 2>/dev/null
}

while true; do
  agent_state=$(massctl agentrun get "$AGENT_NAME" -w "$WORKSPACE" -o json 2>/dev/null \
    | jq -r '.status.state // "unknown"')

  task_json=$(get_task)
  task_completed=$(echo "$task_json" | jq -r '.completed // false')

  if [[ "$task_completed" == "true" ]]; then
    status=$(echo "$task_json" | jq -r '.response.status // "unknown"')
    echo "Task completed. Response status: $status"
    exit 0
  fi

  if [[ "$agent_state" == "error" || "$agent_state" == "stopped" ]]; then
    echo "Agent $AGENT_NAME is in $agent_state state." >&2
    exit 2
  fi

  if [[ "$agent_state" == "idle" ]]; then
    if (( idle_retry_count < MAX_IDLE_RETRIES )); then
      idle_retry_count=$((idle_retry_count + 1))
      echo "Agent idle but task not completed. Retrying ($idle_retry_count/$MAX_IDLE_RETRIES)..." >&2
      massctl agentrun task retry -w "$WORKSPACE" --name "$AGENT_NAME" --id "$TASK_ID" 2>/dev/null || true
    else
      echo "Agent idle, task not completed after $MAX_IDLE_RETRIES retries." >&2
      exit 1
    fi
  fi

  if (( elapsed >= TIMEOUT )); then
    echo "Timeout after ${TIMEOUT}s waiting for task $TASK_ID." >&2
    exit 3
  fi

  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done
```

- [ ] **Step 2: Make executable**

```bash
chmod +x skills/mass-workflow/scripts/poll-task.sh
```

- [ ] **Step 3: Smoke test — verify script prints usage on missing args**

```bash
bash skills/mass-workflow/scripts/poll-task.sh 2>&1 || true
```

Expected: prints usage string containing "poll-task.sh <workspace>"

- [ ] **Step 4: Commit**

```bash
git add skills/mass-workflow/scripts/poll-task.sh
git commit -m "feat(mass-workflow): add standalone poll-task.sh script"
```

---

## Task 3: Create workflow-schema.md reference

**Files:**
- Create: `skills/mass-workflow/references/workflow-schema.md`

- [ ] **Step 1: Write the schema reference**

Create `skills/mass-workflow/references/workflow-schema.md`:

```markdown
# Workflow YAML Schema Reference

## Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Workflow identifier |
| `description` | string | no | Human-readable purpose |
| `workspace` | object | yes | Workspace configuration |
| `agents` | map | yes | Agent definitions keyed by name |
| `stages` | list | yes | Ordered list of stages to execute |
| `output` | object | no | Output collection configuration |

---

## `workspace`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | yes | `local` \| `git` \| `empty` |
| `path` | string | conditional | Required for `local` (project path) and `git` (repo URL). Ignored for `empty`. |

---

## `agents`

Map of agent name → agent config. Agent names must be unique and are referenced by stages.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `system_prompt` | string | yes | Full system prompt for this agent |

Example:
```yaml
agents:
  designer:
    system_prompt: "You are a software architect. Analyze requirements and produce a design document."
  reviewer:
    system_prompt: "You are a code reviewer. Review the design for correctness and risk."
```

---

## `stages`

Ordered list. Execution starts at `stages[0]`. Routing via `routes` determines what runs next.

### Serial stage (default)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier, used in `goto` and `input_from` |
| `type` | enum | no | `serial` (default) \| `parallel` |
| `agent` | string | yes | Agent name from `agents` map |
| `description` | string | yes | Semantic task description — LLM interprets this to build the task prompt |
| `input_files` | list | no | Static files to pass to this stage's task |
| `input_from` | list | no | Stage names whose artifacts to collect and pass as task input files |
| `max_retries` | int | no | Max times this stage can be re-entered via `goto`. Default: 3 |
| `routes` | list | yes | Routing rules based on response status |

### Parallel stage

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique stage identifier |
| `type` | enum | yes | Must be `parallel` |
| `tasks` | list | yes | List of parallel sub-tasks (each has `agent`, `description`, `input_from`, `input_files`) |
| `wait` | enum | no | `all` (default) — wait for all sub-tasks \| `any` — proceed when first completes |
| `max_retries` | int | no | Max retries for this stage. Default: 3 |
| `routes` | list | yes | Routing rules using aggregated status values |

Parallel sub-task fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agent` | string | yes | Agent name from `agents` map |
| `description` | string | yes | Semantic task description |
| `input_from` | list | no | Stage names whose artifacts to collect |
| `input_files` | list | no | Static files |

---

## `routes`

List of routing rules evaluated in order. First matching `when` wins.

### Serial stage `when` values

| Value | Meaning |
|-------|---------|
| `success` | `response.status == "success"` |
| `failed` | `response.status == "failed"` |
| `needs_human` | `response.status == "needs_human"` |
| Any string | Custom `response.status` value |

### Parallel stage `when` values (aggregated)

| Value | Meaning |
|-------|---------|
| `all_success` | All sub-tasks reported `success` |
| `any_failed` | At least one sub-task reported `failed` |
| `all_failed` | All sub-tasks reported `failed` |
| `any_success` | At least one sub-task reported `success` (used with `wait: any`) |

### `goto` targets

| Value | Meaning |
|-------|---------|
| Stage name | Jump to that stage |
| `__done__` | Successful termination |
| `__escalate__` | Halt with human intervention message |

---

## `output`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `collect_from` | list | no | Stage names whose artifacts to copy to `destination` |
| `destination` | string | no | Local path to copy artifacts to. Default: `./mass-workflow-output/` |
| `summary` | bool | no | Print execution summary on completion. Default: `true` |

When `collect_from` references a parallel stage, artifacts from **all** sub-task agents within that stage are merged into `destination`.

---

## Built-in `--input` flag

```
/mass-workflow workflow.yaml --input file1.md --input file2.md
```

Files passed via `--input` are injected into every stage that has no explicit `input_files` defined.

---

## Complete example

```yaml
name: design-review-implement
description: "设计 → 评审 → 实现"

workspace:
  type: local
  path: ./

agents:
  designer:
    system_prompt: "You are a software architect. Analyze the requirements and produce a detailed design document with clear component boundaries and interfaces."
  security_reviewer:
    system_prompt: "You are a security reviewer. Review the design for security vulnerabilities, authentication gaps, and data exposure risks."
  perf_reviewer:
    system_prompt: "You are a performance reviewer. Review the design for scalability bottlenecks, inefficient data access patterns, and resource contention."
  implementer:
    system_prompt: "You are a senior developer. Implement the design faithfully, following the reviewed design document exactly."

stages:
  - name: design
    agent: designer
    description: "Analyze the requirements and produce a design document"
    input_files:
      - requirements.md
    max_retries: 2
    routes:
      - when: success
        goto: parallel_review
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

  - name: parallel_review
    type: parallel
    wait: all
    tasks:
      - agent: security_reviewer
        description: "Review the design for security issues"
        input_from: [design]
      - agent: perf_reviewer
        description: "Review the design for performance issues"
        input_from: [design]
    max_retries: 2
    routes:
      - when: all_success
        goto: implement
      - when: any_failed
        goto: design
      - when: all_failed
        goto: __escalate__

  - name: implement
    agent: implementer
    description: "Implement the design based on the design document and review feedback"
    input_from: [design, parallel_review]
    max_retries: 2
    routes:
      - when: success
        goto: __done__
      - when: failed
        goto: design
      - when: needs_human
        goto: __escalate__

output:
  collect_from: [implement]
  destination: ./output/
  summary: true
```
```

- [ ] **Step 2: Verify file created**

```bash
wc -l skills/mass-workflow/references/workflow-schema.md
```

Expected: > 100 lines

- [ ] **Step 3: Commit**

```bash
git add skills/mass-workflow/references/workflow-schema.md
git commit -m "feat(mass-workflow): add workflow YAML schema reference"
```

---

## Task 4: Create SKILL.md — Part 1: frontmatter, trigger, prerequisites, YAML loading

**Files:**
- Create: `skills/mass-workflow/SKILL.md`

- [ ] **Step 1: Write SKILL.md Part 1 (frontmatter through workspace+agent creation)**

Create `skills/mass-workflow/SKILL.md` with this content:

````markdown
---
name: mass-workflow
description: |
  通用声明式多 agent 工作流编排。读取 YAML workflow 配置，自动创建 workspace 和 agents，
  按阶段执行 task，通过 response.status 路由，收集输出，清理资源。
  触发：用户运行 /mass-workflow <workflow.yaml>，或提到"运行 workflow"、"执行 workflow 配置"。
version: 0.1.0
---

# mass-workflow — Declarative Multi-Agent Workflow Orchestrator

读取用户提供的 YAML workflow 配置，自动编排多 agent 执行流程。

> **前置依赖**：本 skill 依赖 **mass-guide** skill 进行 workspace 和 agent 生命周期管理。
> 执行前调用 mass-guide 确认 `mass daemon status` 正常。

## 触发格式

```
/mass-workflow path/to/workflow.yaml
/mass-workflow path/to/workflow.yaml --input file1.md --input file2.md
```

`--input` 文件注入到所有未显式配置 `input_files` 的 stage。

完整 YAML 字段说明见 [references/workflow-schema.md](references/workflow-schema.md)。

---

## 执行流程

```
Step 0: 健康检查 + 读取并验证 workflow.yaml
Step 1: 创建 workspace + 所有 agentrun
Step 2: 执行阶段循环（stage loop）
Step 3: 收集输出 artifacts
Step 4: 清理 workspace + agents
Step 5: 打印执行摘要
```

---

## Step 0: 健康检查 + 读取 workflow.yaml

### 0a. 健康检查

```bash
mass daemon status
```

- `daemon: running` → 继续
- 否则 → 停止，告知用户

### 0b. 读取 workflow.yaml

读取用户指定路径的 YAML 文件。提取：
- `name` — workflow 名称，用作 workspace name（加随机后缀避免冲突）：`{name}-{random4hex}`
- `description` — 仅展示用
- `workspace` — type + path
- `agents` — 名称 → system_prompt 的 map
- `stages` — 有序列表
- `output` — 输出配置（可选，缺省见 schema）

### 0c. 前置验证（启动前）

验证失败时立即停止，**不创建任何资源**：

1. `agents` map 不为空
2. `stages` 列表不为空
3. 每个 serial stage 的 `agent` 字段引用了 `agents` map 中存在的 key
4. 每个 parallel stage 的每个 sub-task `agent` 字段引用了 `agents` map 中存在的 key
5. 所有 `routes[].goto` 要么是已知 stage name，要么是 `__done__` / `__escalate__`
6. 所有 `input_from` 引用的 stage name 在 `stages` 中存在
7. `workspace.type` 为 `local | git | empty`

验证通过后，向用户展示解析结果摘要：
```
Workflow: {name}
Workspace: {type} {path}
Agents: {agent1}, {agent2}, ...
Stages: {stage1} → {stage2} → ...
```

等待用户确认（"确认执行？"），确认后继续。

---

## Step 1: 创建 Workspace + Agents

使用 **mass-guide** skill 执行：

### 1a. 创建 workspace

```bash
massctl workspace create {type} --name {workspace-name} [--path {path} | --url {url}]
```

等待 workspace status == ready（轮询 `massctl workspace get {name} -o json`）。

### 1b. 创建所有 agentrun

对 `agents` map 中每个 agent，执行：

```bash
massctl agentrun create -w {workspace-name} --name {agent-key} --agent claude \
  --system-prompt "{agent.system_prompt}"
```

等待每个 agentrun state == idle。

若任意 agentrun 创建失败：立即停止，执行完整清理（见 Step 4），报告错误。

---
````

- [ ] **Step 2: Verify file exists and has correct frontmatter**

```bash
head -15 skills/mass-workflow/SKILL.md
```

Expected: shows `---`, `name: mass-workflow`, `version: 0.1.0`

- [ ] **Step 3: Commit**

```bash
git add skills/mass-workflow/SKILL.md
git commit -m "feat(mass-workflow): add SKILL.md Part 1 — setup, validation, workspace creation"
```

---

## Task 5: Extend SKILL.md — Part 2: stage execution loop

**Files:**
- Modify: `skills/mass-workflow/SKILL.md`

- [ ] **Step 1: Append stage execution section to SKILL.md**

Append the following to `skills/mass-workflow/SKILL.md`:

````markdown
## Step 2: Stage Execution Loop

从 `stages[0]` 开始，按 routes 跳转执行，直到到达 `__done__` 或 `__escalate__`。

维护内存状态（本 session 内）：
- `current_stage` — 当前 stage name
- `retry_counters` — map: stage_name → retry_count（初始化为全 0）
- `stage_artifacts` — map: stage_name → list of artifact file paths（执行后填充）

### 2a. 执行 Serial Stage

**① 构建 task input files 列表**

```bash
input_files=()

# 1. --input 全局注入（仅当 stage 无显式 input_files 时）
if [[ ${#stage.input_files[@]} -eq 0 ]]; then
  input_files+=("${global_inputs[@]}")
fi

# 2. 显式 input_files
for f in "${stage.input_files[@]}"; do
  input_files+=("$f")
done

# 3. input_from: 收集上游 stage artifacts
for upstream_stage in "${stage.input_from[@]}"; do
  for artifact in "${stage_artifacts[$upstream_stage][@]}"; do
    input_files+=("$artifact")
  done
done
```

**② 创建 task**

```bash
massctl agentrun task create -w {workspace} --name {stage.agent} \
  --description "{stage.description}" \
  $(for f in "${input_files[@]}"; do echo "--file $f"; done)
```

提取返回的 `task.id`：
```bash
task_id=$(massctl agentrun task create ... -o json | jq -r '.id')
```

**③ 轮询等待**

```bash
skills/mass-workflow/scripts/poll-task.sh {workspace} {stage.agent} {task_id}
poll_exit=$?
```

| poll exit | 处理 |
|-----------|------|
| 0 | 读取 response.status，执行路由 |
| 1 | agent idle retry 用尽 → 视为 `failed`，走 routes 路由 |
| 2 | agent error/stopped → 停止，人工介入（不走 routes，直接 escalate） |
| 3 | 超时 → 视为 `failed`，走 routes 路由 |

**④ 收集 artifacts**

```bash
artifact_dir=".mass/{workspace}/{stage.agent}/artifacts/"
if [[ -d "$artifact_dir" ]]; then
  stage_artifacts[{stage.name}]=$(find "$artifact_dir" -type f)
fi
```

**⑤ 读取 response.status 并路由**

```bash
task_json=$(massctl agentrun task get -w {workspace} --name {stage.agent} --id {task_id} -o json)
response_status=$(echo "$task_json" | jq -r '.response.status // "unknown"')
```

按 `stage.routes` 顺序匹配 `when == response_status`，找到第一个匹配的 `goto`：

- `goto` 是 stage name：
  - `retry_counters[goto]++`
  - 若 `retry_counters[goto] > stage.max_retries`（默认 3）：→ `__escalate__`
  - 否则：`current_stage = goto`，继续循环
- `goto: __done__`：进入 Step 3
- `goto: __escalate__`：打印执行路径 + response.description，停止

无匹配 `when`：按语义判断最接近的 route；若无法判断，视为 `needs_human` → `__escalate__`。

---

### 2b. 执行 Parallel Stage

**① 并发创建所有 sub-task**

对每个 `tasks[i]` 按 2a 的 ① 逻辑构建 input files，并发执行：

```bash
# 为每个 sub-task 创建 task，收集 task_id
declare -A sub_task_ids
for sub_task in "${stage.tasks[@]}"; do
  task_id=$(massctl agentrun task create -w {workspace} --name {sub_task.agent} \
    --description "{sub_task.description}" \
    $(for f in "${sub_task_input_files[@]}"; do echo "--file $f"; done) \
    -o json | jq -r '.id')
  sub_task_ids[{sub_task.agent}]=$task_id
done
```

**② 并发轮询**

对每个 sub-task 并发运行 poll-task.sh（后台进程），等待结果：

```bash
declare -A sub_poll_exits
for agent in "${!sub_task_ids[@]}"; do
  (
    skills/mass-workflow/scripts/poll-task.sh {workspace} "$agent" "${sub_task_ids[$agent]}"
    echo $? > /tmp/poll_exit_{workspace}_{agent}
  ) &
done
wait  # 等待所有后台 poll 完成（wait: all）
# wait: any — 使用 wait -n 等第一个完成，cancel 其余（若 massctl 支持 task cancel）
```

**③ 聚合状态**

读取每个 sub-task 的 response.status，计算聚合结果：

| 聚合规则 | 触发条件 |
|---------|---------|
| `all_success` | 所有 sub-task response.status == success |
| `all_failed` | 所有 sub-task response.status == failed |
| `any_failed` | 至少一个 failed（且非 all_failed） |
| `any_success` | 至少一个 success（用于 wait: any 场景） |

**④ 收集所有 sub-task artifacts**

```bash
for agent in "${!sub_task_ids[@]}"; do
  artifact_dir=".mass/{workspace}/$agent/artifacts/"
  if [[ -d "$artifact_dir" ]]; then
    stage_artifacts[{stage.name}]+=$(find "$artifact_dir" -type f)
  fi
done
```

**⑤ 路由** — 与 serial stage 相同逻辑，使用聚合 status 匹配 `when`。

---
````

- [ ] **Step 2: Verify the section was appended**

```bash
grep -n "Step 2" skills/mass-workflow/SKILL.md
```

Expected: shows line number with "## Step 2: Stage Execution Loop"

- [ ] **Step 3: Commit**

```bash
git add skills/mass-workflow/SKILL.md
git commit -m "feat(mass-workflow): add SKILL.md Part 2 — stage execution loop (serial + parallel)"
```

---

## Task 6: Extend SKILL.md — Part 3: output collection, cleanup, summary, error table

**Files:**
- Modify: `skills/mass-workflow/SKILL.md`

- [ ] **Step 1: Append output/cleanup/error section to SKILL.md**

Append the following to `skills/mass-workflow/SKILL.md`:

````markdown
## Step 3: Collect Output Artifacts

仅在 `__done__` 时执行（escalate 时跳过，保留 artifacts 供 debug）。

```bash
destination="${output.destination:-./mass-workflow-output/}"
mkdir -p "$destination"

for stage_name in "${output.collect_from[@]}"; do
  for artifact in "${stage_artifacts[$stage_name][@]}"; do
    cp "$artifact" "$destination"
  done
done

echo "Output collected to: $destination"
ls "$destination"
```

---

## Step 4: 清理

**无论成功、失败、escalate，均执行清理。** 失败时保留 `.mass/{workspace}/` artifacts（不删 workspace 内文件，只停止 agent 进程和删除 agentrun 记录）。

使用 **mass-guide** skill 顺序执行：

```bash
# 1. 停止所有 agentrun
for agent in all_agent_names; do
  massctl agentrun stop "$agent" -w {workspace}
done

# 2. 删除所有 agentrun
for agent in all_agent_names; do
  massctl agentrun delete "$agent" -w {workspace}
done

# 3. 删除 workspace（成功时删除；失败/escalate 时询问用户是否删除）
massctl workspace delete {workspace}
```

清理失败时记录警告，继续清理其余资源，不中断流程。

---

## Step 5: 打印执行摘要

```
=== mass-workflow execution summary ===
Workflow:   {name}
Status:     done | escalated
Duration:   {elapsed}s

Stage execution path:
  design          → success  (1 attempt)
  parallel_review → all_success (1 attempt)
  implement       → success  (2 attempts)

Output: {destination}
```

escalate 时额外打印：
```
=== ESCALATION ===
Stage: {stage_name}
Reason: {response.description}
Retry count: {n}/{max_retries}

Next steps:
  - Review artifacts at: .mass/{workspace}/{agent}/artifacts/
  - Re-run with adjusted workflow or fix the issue manually
```

---

## 错误处理速查

| 场景 | 行为 |
|------|------|
| YAML 文件不存在 | 立即停止，报告路径错误，不创建任何资源 |
| YAML 验证失败 | 立即停止，报告具体字段错误，不创建任何资源 |
| workspace 创建失败 | 立即停止，不创建 agentrun |
| agentrun 创建失败 | 停止，清理已创建的 agentrun + workspace |
| poll exit 2 (agent error) | 不走 routes，直接 __escalate__ |
| poll exit 1/3 (idle/timeout) | 视为 `failed`，走正常 routes 路由 |
| retry 超限 | 强制 __escalate__，不管 routes 配置 |
| `__escalate__` | 打印完整上下文，保留 artifacts，清理进程资源 |
| 无匹配 route | 按 response.status 语义判断；无法判断 → __escalate__ |
| cleanup 失败 | 记录警告，继续清理其余资源 |

---

## 设计原则

1. **YAML 是语义描述，不是模板** — orchestrator（LLM）读取 `description` 字段并自行判断如何构建 task prompt，不 hardcode 模板
2. **Agent 间不直接通信** — 所有协调经 orchestrator via task API
3. **失败时保留 artifacts** — 供 debug，不自动删除
4. **验证前置** — YAML 问题在启动前暴露，不在执行中途失败
5. **cleanup 保证** — 任何终止路径都执行清理

---

## 与其他 skill 的关系

| Skill | 职责 |
|-------|------|
| `mass-guide` | 前置依赖：workspace/agent 生命周期原语 |
| `mass-pilot` | 保留：手写复杂 orchestrator 逻辑 |
| `mass-workflow` | 本 skill：声明式配置驱动的通用 orchestrator |

复杂条件分支、动态角色选择、跨 session 持久化 → 使用 `mass-pilot` 手写。
````

- [ ] **Step 2: Verify final structure**

```bash
grep "^## Step" skills/mass-workflow/SKILL.md
```

Expected:
```
## Step 0: 健康检查 + 读取 workflow.yaml
## Step 1: 创建 Workspace + Agents
## Step 2: Stage Execution Loop
## Step 3: Collect Output Artifacts
## Step 4: 清理
## Step 5: 打印执行摘要
```

- [ ] **Step 3: Commit**

```bash
git add skills/mass-workflow/SKILL.md
git commit -m "feat(mass-workflow): add SKILL.md Part 3 — output, cleanup, error handling, summary"
```

---

## Task 7: Register skill in CLAUDE.md (if needed)

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Check if skills table in AGENTS.md needs updating**

```bash
grep -n "mass-workflow\|mass-pilot" AGENTS.md
```

- [ ] **Step 2: Add mass-workflow to skills table**

In `AGENTS.md`, in the Skills table (near the existing `mass-pilot` and `mass-guide` rows), add:

```markdown
| mass-workflow | [skills/mass-workflow/SKILL.md](skills/mass-workflow/SKILL.md) | Declarative YAML-driven multi-agent workflow orchestrator |
```

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: register mass-workflow skill in AGENTS.md"
```

---

## Task 8: Final verification

- [ ] **Step 1: Verify all files exist**

```bash
find skills/mass-workflow -type f | sort
```

Expected:
```
skills/mass-workflow/SKILL.md
skills/mass-workflow/references/workflow-schema.md
skills/mass-workflow/scripts/poll-task.sh
```

- [ ] **Step 2: Verify SKILL.md frontmatter is valid**

```bash
head -10 skills/mass-workflow/SKILL.md
```

Expected: correct YAML frontmatter with `name`, `description`, `version`.

- [ ] **Step 3: Verify poll-task.sh is executable**

```bash
ls -la skills/mass-workflow/scripts/poll-task.sh
```

Expected: `-rwxr-xr-x`

- [ ] **Step 4: Check skill is referenced in AGENTS.md**

```bash
grep "mass-workflow" AGENTS.md
```

Expected: entry in skills table.

- [ ] **Step 5: Final commit if any loose changes**

```bash
git status
```

If clean: done. If dirty: stage and commit remaining changes.
