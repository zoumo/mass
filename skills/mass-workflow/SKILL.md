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

默认 `--agent claude`。等待每个 agentrun state == idle。

若任意 agentrun 创建失败：立即停止，执行完整清理（见 Step 4），报告错误。

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
