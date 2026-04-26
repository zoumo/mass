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
