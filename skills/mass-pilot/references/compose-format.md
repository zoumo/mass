# Compose YAML 格式

`massctl compose -f <file>` 声明式创建 workspace + 多个 agentrun。

## 完整格式

```yaml
kind: workspace-compose
meta
  name: my-ws                   # Workspace 名称
spec:
  source:
    type: local                  # local | git | emptyDir
    path: /path/to/code          # local 必填
    # url: https://...           # git 必填
    # ref: main                  # git 可选（分支/tag/commit）
  agents:
    - meta
        name: agent-name         # AgentRun 名称（workspace 内唯一）
      spec:
        agent: claude            # 内置 agent 定义名
        systemPrompt: |          # 系统提示词
          Your role description...
        permissions: approve_all # approve_all | approve_reads | deny_all
        restartPolicy: always_new # try_reload | always_new
```

## 字段说明

### source

| type | 必填字段 | 说明 |
|------|----------|------|
| `local` | `path` | 挂载本地目录，mass 不管理其生命周期 |
| `git` | `url`，可选 `ref` | 克隆 git 仓库，mass 管理目录 |
| `emptyDir` | 无 | 创建空目录，mass 管理 |

### agents[].spec

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `agent` | 是 | — | 引用的 agent 定义名（claude / codex / gsd-pi 或自定义） |
| `systemPrompt` | 否 | — | 该 agentrun 实例的系统提示词 |
| `permissions` | 否 | `approve_all` | 文件/终端权限策略 |
| `restartPolicy` | 否 | `always_new` | `try_reload`：尝试恢复会话；`always_new`：总是新建 |

## compose 执行流程

1. 创建 workspace → 轮询直到 phase == `ready`
2. 依次创建每个 agentrun
3. 轮询等待所有 agentrun state == `idle`
4. 打印所有 agent 的 socket 路径
