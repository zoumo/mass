# 错误处理

## 前置检查错误

| 错误 | 原因 | 处理 |
|------|------|------|
| `daemon: not running` | daemon 未启动 | 告知用户启动，不要自行启动 |
| 连接被拒绝 | `--socket` 路径错误 | 与用户确认 socket 路径 |

## Workspace 错误

| 错误 | 原因 | 处理 |
|------|------|------|
| phase 停在 `pending` | source 准备慢（大仓库 clone） | 继续轮询。超过 5 分钟让用户检查 daemon 日志 |
| phase 变为 `error` | source 无效：路径不存在、git URL 不可达、ref 不存在 | `workspace delete` → 修正配置 → 重建 |
| 删除失败："workspace has active agents" | 还有 agentrun 挂在上面 | 先 stop + delete 所有 agentrun，再删 workspace |

## AgentRun 错误

| 错误 | 原因 | 处理 |
|------|------|------|
| 创建失败：workspace not ready | workspace 还在 `pending` | 等 workspace `ready` 后重试 |
| 创建失败：agent not found | `--agent` 名称错误 | `agent get` 查看可用 agent 列表 |
| 停在 `creating` 超过 2 分钟 | agent 二进制未安装或 ACP 握手超时 | `agentrun get` 查 errorMessage，让用户检查 agent 二进制可用性 |
| prompt 被拒绝："not idle" | agent 不在 `idle` 状态 | `running` → `cancel` 后等 idle；`stopped`/`error` → `restart` 后等 idle |
| 工作中进入 `error` | 运行时崩溃、OOM、shim 进程死亡 | `agentrun restart`。反复失败则检查 daemon 日志 |
| daemon 重启后 agent `error` | shim 进程未能存活 | `agentrun restart` |
| 删除失败："not stopped" | agent 还在 running 或 idle | 先 `stop`，再 `delete` |

## Agent 间通信错误

| 错误 | 原因 | 处理 |
|------|------|------|
| `workspace send` 失败：agent not found | 目标 agent 名称错误或未创建 | `agentrun get -w <ws>` 查看实际 agent 名称 |
| 消息未送达 | 目标 agent 已 stopped 或 error | restart 目标 agent → 等 idle → 重发 |
| Agent 死锁（双方都在等待） | 两个 agent 互相等对方消息 | `cancel` 其中一个 → 重新 prompt 指示它继续 |

## 决策树

```
Agent 无响应？
├─ agentrun get <name> -w <ws> 查状态
├─ running 太久？
│  └─ cancel → 等 idle → 重新 prompt
├─ error？
│  └─ restart → 等 idle → 重新 prompt
├─ stopped？
│  └─ restart → 等 idle → 重新 prompt
├─ creating 超过 2 分钟？
│  └─ 有 errorMessage → 让用户检查 agent 二进制
│  └─ 无 errorMessage → 继续等待
└─ idle 但 prompt 没反应？
   └─ stop → delete → 重建 → 重新 prompt
```

## 完整重建

部分恢复不行时，全部拆掉重来：

```bash
for agent in $(massctl agentrun get -w my-ws -o json | jq -r '.[].metadata.name'); do
  massctl agentrun stop $agent -w my-ws 2>/dev/null
  massctl agentrun delete $agent -w my-ws 2>/dev/null
done
massctl workspace delete my-ws
# 然后用 compose 重建
massctl compose -f compose.yaml
```
