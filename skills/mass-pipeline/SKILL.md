---
name: mass-pipeline
description: |
  声明式多 agent pipeline 编排。读取 YAML pipeline 配置，自动创建 workspace 和 agents，
  按阶段执行 task，通过 .status 路由，收集输出，清理资源。
  触发：用户运行 /mass-pipeline，或提到"用 pipeline 执行"、"多 agent 协作完成任务"。
  内置标准开发流程：plan → review → execute → code review → fix（使用 dev-pipeline 模板）。
version: 0.2.0
---

# mass-pipeline — Declarative Multi-Agent Pipeline Orchestrator

读取 YAML pipeline 配置，自动编排多 agent 执行流程。内置标准开发 pipeline 模板。

> **前置依赖**：本 skill 依赖 **mass-guide** skill 进行 workspace 和 agent 生命周期管理。
> 执行前调用 mass-guide 确认 `mass daemon status` 正常。

## Orchestrator Boundary Rules

**You are the conductor, not the performer.**

### DO
- Create tasks for agents via `massctl agentrun task do`
- Poll task completion via `scripts/poll-task.sh`
- Read `.status` and route to the next stage
- Pass artifacts between stages as `--input-files` inputs
- Call scripts (`validate-pipeline.sh`, `poll-task.sh`) for deterministic operations
- Make routing decisions (which stage to run next, when to escalate)

### DO NOT
- Write code, documents, or designs yourself — that is agent work
- Analyze content in agent artifacts — pass them to the next agent
- Make judgment calls about whether a design is "good" — the reviewer agent does that
- Retry agent work differently — create a new task with updated context instead
- Skip stages because you think the result is obvious

**If you catch yourself about to "help" by doing a task directly instead of delegating it — stop. Create a task for the agent.**

---

## 触发格式

### 内置 coding-pipeline（推荐）

直接描述任务，使用内置标准开发流程（plan → review → execute → code review → fix）：

```
/mass-pipeline
用 pipeline 实现 [任务描述]
```

Orchestrator 自动使用：
- compose: `skills/mass-pipeline/templates/coding-compose.yaml`
- pipeline: `skills/mass-pipeline/templates/coding-pipeline.yaml`

每个审查循环最多 3 轮收敛，超限后 escalate。

### 自定义 pipeline

```
/mass-pipeline /path/to/pipeline.yaml
/mass-pipeline /path/to/pipeline.yaml --input file1.md --input file2.md
```

`--input` 文件注入到所有未显式配置 `input_files` 的 stage。

**自定义文件写入规则**：当 orchestrator 需要生成自定义 compose 或 pipeline 文件时，写到临时目录：

```bash
TMPDIR=$(mktemp -d /tmp/mass-pipeline-XXXXXX)
compose_file="$TMPDIR/compose.yaml"
pipeline_file="$TMPDIR/pipeline.yaml"
```

完整字段说明：
- Compose YAML: [references/compose-schema.md](references/compose-schema.md)
- Pipeline YAML: [references/pipeline-schema.md](references/pipeline-schema.md)

内置模板参考:
- `templates/coding-compose.yaml` — workspace-compose with planner/reviewer/worker agents
- `templates/coding-pipeline.yaml` — plan → review → execute → code review → fix

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

## Step 0: 健康检查 + 读取 pipeline + 确定 compose

### 0a. 健康检查

```bash
mass daemon status
```

- `daemon: running` → 继续
- 否则 → 停止，告知用户

### 0b. 读取 pipeline + 确定 compose 文件

从 pipeline YAML 提取：
- `name` — pipeline 名称，用作 workspace name 前缀：`{name}-{random4hex}`
- `description` — 仅展示用
- `stages` — 有序列表
- `output` — 输出配置（可选）

**Compose 文件由 orchestrator 单独决定**，不在 pipeline YAML 中引用：
- 内置 coding pipeline → 直接使用 `skills/mass-pipeline/templates/coding-compose.yaml`
- 自定义 pipeline → orchestrator 自行生成 compose 文件写到临时目录

生成 workspace 名：
```bash
WORKSPACE_NAME="{pipeline.name}-$(openssl rand -hex 2)"
```

### 0c. 前置验证（启动前）

只验证 pipeline 字段（stages/routes）：

```bash
skills/mass-pipeline/scripts/validate-pipeline.sh {pipeline_file}
```

Exit code 0: validation passed. Show summary to user.
Exit code 1: validation failed, prints errors. Report and stop.
Exit code 2: missing dependency. Report and stop.

After successful validation, ask the user: "确认执行？" Wait for confirmation before proceeding.

---

## Step 1: 创建 Workspace + Agents

```bash
massctl compose apply -f {compose_file} --workspace {workspace_name}
```

`--workspace` 覆盖 compose 文件中的 `metadata.name`，等待 workspace ready + 所有 agent idle。失败时执行 Step 4 清理并报告错误。

---

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
STAGE_OUTPUT_DIR=".mass/{workspace}/{stage.agent}/output/{stage.name}"
mkdir -p "$STAGE_OUTPUT_DIR"

task_id=$(massctl agentrun task do -w {workspace} --run {stage.agent} \
  --prompt "{stage.prompt}" \
  --output-dir "$STAGE_OUTPUT_DIR" \
  $(for f in "${input_files[@]}"; do echo "--input-files $f"; done) \
  | jq -r '.id')
```

**③ 轮询等待**

```bash
skills/mass-pipeline/scripts/poll-task.sh {workspace} {stage.agent} {task_id}
poll_exit=$?
```

| poll exit | 处理 |
|-----------|------|
| 0 | 读取 .status，执行路由 |
| 1 | agent idle retry 用尽 → 视为 `failed`，走 routes 路由 |
| 2 | agent error/stopped → 停止，人工介入（不走 routes，直接 escalate） |
| 3 | 超时 → 视为 `failed`，走 routes 路由 |

**④ 收集 artifacts**

```bash
# artifacts 由 agent 写入 --output-dir 指定的目录
stage_artifacts[{stage.name}]=$(find "$STAGE_OUTPUT_DIR" -type f 2>/dev/null)
```

**⑤ 读取 .status 并路由**

```bash
task_json=$(massctl agentrun task get -w {workspace} --run {stage.agent} {task_id} -o json)
response_status=$(echo "$task_json" | jq -r '.reason // "unknown"')
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
  SUB_OUTPUT_DIR=".mass/{workspace}/${sub_task.agent}/output/{stage.name}"
  mkdir -p "$SUB_OUTPUT_DIR"
  task_id=$(massctl agentrun task do -w {workspace} --run {sub_task.agent} \
    --prompt "{sub_task.prompt}" \
    --output-dir "$SUB_OUTPUT_DIR" \
    $(for f in "${sub_task_input_files[@]}"; do echo "--input-files $f"; done) \
    | jq -r '.id')
  sub_task_ids[{sub_task.agent}]=$task_id
done
```

**② 并发轮询**

对每个 sub-task 并发运行 poll-task.sh（后台进程），等待结果：

```bash
declare -A sub_poll_exits
for agent in "${!sub_task_ids[@]}"; do
  (
    skills/mass-pipeline/scripts/poll-task.sh {workspace} "$agent" "${sub_task_ids[$agent]}"
    echo $? > /tmp/poll_exit_{workspace}_{agent}
  ) &
done
wait  # 等待所有后台 poll 完成（wait: all）
# wait: any — 使用 wait -n 等第一个完成，cancel 其余（若 massctl 支持 task cancel）
```

**③ 聚合状态**

读取每个 sub-task 的 .status，计算聚合结果：

| 聚合规则 | 触发条件 |
|---------|---------|
| `all_success` | 所有 sub-task .status == success |
| `all_failed` | 所有 sub-task .status == failed |
| `any_failed` | 至少一个 failed（且非 all_failed） |
| `any_success` | 至少一个 success（用于 wait: any 场景） |

**④ 收集所有 sub-task artifacts**

```bash
# artifacts 由各 agent 写入 --output-dir 指定的目录
for agent in "${!sub_task_ids[@]}"; do
  SUB_OUTPUT_DIR=".mass/{workspace}/${agent}/output/{stage.name}"
  stage_artifacts[{stage.name}]+=$(find "$SUB_OUTPUT_DIR" -type f 2>/dev/null)
done
```

**⑤ 路由** — 与 serial stage 相同逻辑，使用聚合 status 匹配 `when`。

---

## Step 3: Collect Output Artifacts

仅在 `__done__` 时执行（escalate 时跳过，保留 artifacts 供 debug）。

```bash
destination="${output.destination:-./mass-pipeline-output/}"
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
=== mass-pipeline execution summary ===
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
| 无匹配 route | 按 .status 语义判断；无法判断 → __escalate__ |
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
| `mass-pipeline` | 本 skill：声明式配置驱动的通用 orchestrator |
