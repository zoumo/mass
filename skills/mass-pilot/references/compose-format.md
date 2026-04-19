# Compose 格式

## compose apply

`massctl compose apply -f <file>` 声明式创建 workspace + 多个 agentrun。

## compose run（快速启动）

`massctl compose run` 使用当前目录快速启动单个 agent run，无需 YAML 文件。

```bash
# 最简用法
massctl compose run -w my-ws --agent claude

# 指定 run 名称
massctl compose run -w my-ws --agent claude --name my-claude

# 带 system prompt
massctl compose run -w my-ws --agent claude --system-prompt "You are a reviewer"
```

| Flag | 必填 | 说明 |
|------|------|------|
| `-w, --workspace` | 是 | Workspace 名称 |
| `--agent` | 是 | Agent 定义名 |
| `--name` | 否 | AgentRun 名称（默认等于 agent 名称） |
| `--system-prompt` | 否 | 系统提示词 |

workspace 已存在且 ready 时自动复用，否则以 `cwd` 为 local source 新建。

## compose apply YAML 格式

`massctl compose apply -f <file>` 声明式创建 workspace + 多个 agentrun（workspace 必须不存在）。

## 完整格式

```yaml
kind: workspace-compose
metadata:
  name: my-ws                    # Workspace 名称
spec:
  source:
    type: local                  # local | git | emptyDir
    path: /path/to/code          # local 必填
    # url: https://...           # git 必填
    # ref: main                  # git 可选（分支/tag/commit）
  agents:
    - metadata:
        name: agent-name         # AgentRun 名称（workspace 内唯一）
      spec:
        agent: claude            # 内置 agent 定义名
        systemPrompt: |          # 系统提示词
          Your role description...
        permissions: approve_all # approve_all | approve_reads | deny_all
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

## compose 执行流程

1. 创建 workspace → 轮询直到 phase == `ready`
2. 依次创建每个 agentrun
3. 轮询等待所有 agentrun state == `idle`
4. 打印所有 agent 的 socket 路径
