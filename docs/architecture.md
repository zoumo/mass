# MASS 当前架构

当前产品通过 HTTP JSON-RPC 和实时 SSE 提供本地 Codex 与 codex-remote 会话服务。两者共用 Codex 适配栈；Remote 仅拥有自己的连接，不支持跨连接恢复。没有数据库查询投影、Claude 接入或运行期用户类型注册。

本文描述当前已实现架构；未来扩展方向见 [Runtime 方案](design/runtime-core-architecture.md)，准确产品范围以当前接口合同为准。

## 角色与调用关系

外部协议层校验并接纳请求，Session 管理负责名称预留、持久绑定与生命周期编排，Codex 适配负责原生运行资源和具体连接。命令和归一化事实通过 Runtime 合同传递。

| 角色 | 当前责任 |
|---|---|
| 系统组装 | 读取配置，构造命名运行实例和处理器，协调监听与退出 |
| 外部协议 | 请求、错误和健康响应，以及只订阅未来事件的 SSE |
| Session 管理 | 会话身份、预留、创建回滚、归档、显式恢复与只读查询 |
| Runtime 合同 | 绑定具体 Attachment 的命令，以及完整事实和预览交付 |
| Codex 适配 | 每个 runtimeName 的惰性 app-server、原生会话路由、审批、取消和资源回收 |
| 事件核心 | 提交次序、序号和时间、快照、不可变发布和处理器交付 |
| 持久化 | 原子元信息、刷新后的顺序文件历史与严格恢复 |

Session 生命周期通过 Runtime 合同获得 Agent 返回的真实 ID 和 Attachment，稳定实例绑定与恢复证据保存在终态元信息中。公共事件与适配方版本化私有证据共同保存在单条 JSONL；Codex 独立验证原生证据与公共投影，通用事件核心不解释原生坐标。当前责任及依赖见[模块职责](design/crate-workspace.md)，实现映射另见[实现指南](develop/rust-workspace.md)。

## 资源、提交与失败

配置文件以 runtimes 数组登记 name/type/config，可选 enabled 默认 true。登记及只读发现不激活 Agent。实例稳定身份、类型和配置版本保存在独立台账；遗漏不删除身份，有会话引用时拒绝变更执行配置，身份丢失时拒绝按名称认领。数据目录由进程持有文件锁。详情见[启动配置](../README.md)和[只读接口](http-api.md)。

同一 runtimeName 的 Session 共享一个惰性 app-server，不同名称拥有不同服务。每个命令保持原 Attachment 的身份，不按 Session 名称重找新连接。关闭一个 Session 不停止健康的共享服务。

完整事实先进入有界交付，再由唯一提交核心分配 seq/time、追加并刷新历史，最后向 SSE 和处理器发布同一不可变事件。预览不承担历史或补发职责。写入失败封闭写入者，不发布失败记录。

归档等待结算和释放证据；实例停止关闭连接并回收己方进程。显式恢复先验证全部本地记录，失败不新建替代会话。查询不会激活 Agent，恢复不会重放旧输入或处理器动作。

共享服务故障影响其 Attachment，其他运行实例保持独立。持续背压可能延迟共享服务的其他会话和控制响应。有界计数和字节预算不是进程内存上限，也不保证永久阻塞下仍有进展。停止、取消或己方服务退出不证明所有后台后代都已终止。

精确合同见[原生适配](design/native-runtime-connectors.md)、[外部协议](http-api.md)、[事件](events.md)、[存储](storage.md)、[处理器](handlers.md)和[交互](interactions.md)。验证入口和限制见[测试指南](testing.md)。
