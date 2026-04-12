# 为什么 OAR 没有 runa

OCI 生态里 runc 是核心组件。OAR 没有对应的 runa。本文说明原因。

## runc 存在的前提

runc 能作为独立的一次性 CLI 存在，依赖三个前提：

**前提 1：容器进程启动后可以独立存活**

runc 启动容器进程后可以退出。容器继续运行，由 containerd-shim 持有 stdio。
runc 是一次性的启动器，不需要常驻。

**前提 2：启动和 stdio 持有可以分离**

runc 负责 fork/exec，containerd-shim 负责持有 stdio。
两件事由两个进程完成，职责清晰。

**前提 3：runc 的核心工作是内核隔离**

namespace、cgroups、seccomp、pivot_root——这些是真正复杂的工作，
值得封装成独立组件并规范化为 OCI Runtime Spec。
runc 的价值在这里。

## agent 进程不满足这些前提

**前提 1 不满足：agent 进程无法独立存活**

ACP 是 JSON-RPC over stdio。agent 进程依赖 stdio 的另一端——ACP client——来接收
prompt 和响应 fs/terminal 请求。如果 ACP client 退出，agent 下次写 stdout 会收到
SIGPIPE，进程崩溃。

agent 进程和 ACP client 的生命周期是绑定的，"启动后独立存活"不成立。

**前提 2 不满足：启动和 stdio 持有无法分离**

谁 fork/exec agent 进程，谁就持有它的 stdin/stdout pipe。
要把 stdio 移交给另一个进程，需要通过 SCM_RIGHTS 传递文件描述符，
这等于变相实现了一个 shim 协议，复杂度不低于直接在 shim 里 fork。

更根本的问题：stdio 的读端必须有人持续消费，否则 agent 写满 pipe buffer 会阻塞。
这个"持续消费"就是 ACP client 的职责，它必须常驻，不能是一次性 CLI。

**前提 3 不满足：agent 不需要内核隔离**

agent 是进程，不需要 namespace、cgroups、seccomp。
runc 的核心价值在 agent 场景下不存在。
剩下的只有 fork/exec，这不值得一个独立组件。

## 职责归属

| runc 的职责 | OAR 中的归属 |
|-------------|-------------|
| fork/exec 进程 | agent-shim（启动 agent 进程，同时持有 stdio） |
| 持有 stdio | agent-shim（唯一的 ACP client） |
| 内核隔离 | 不需要 |
| 写 state.json | agent-shim（维护 session 状态） |
| 生命周期操作（create/kill/delete） | agent-shim RPC（`session/prompt`、`session/cancel`、`runtime/stop`、`runtime/status`） |

OAR 的结论是：**在 agent 世界里，"启动进程"和"持有 stdio 作为 ACP client"
必须由同一个常驻进程完成。这个进程是 agent-shim，没有 runa 的位置。**

## 和 containerd-shim 的对比

containerd 生态：

```
containerd → containerd-shim（持有 stdio）→ runc（fork/exec）→ 容器进程
                                                  ↑
                                           可以退出，职责完成
```

OAR 生态：

```
agentd → agent-shim（fork/exec + 持有 stdio + ACP client）→ agent 进程
              ↑
         必须常驻，因为它是 ACP client
```

runc 的存在是因为 containerd-shim 和 runc 可以分工。
agent-shim 把这两者合并了，因为在 ACP 协议约束下分工没有意义。

## OAR Runtime Spec 的定位

`config.json`（OAR Runtime Spec）不消失，它的定位是：

**agentd 生成、agent-shim 消费的 session 配置描述。**

它解决了 agentd 和 agent-shim 之间的接口问题——agentd 不需要知道 agent-shim 的命令行
参数细节，只需要把所有信息写进 config.json，agent-shim 读取并执行。

这和 OCI config.json 的作用完全一致，只是消费方是 agent-shim 而不是 runc。

详见 [config.md](config-spec.md)。
