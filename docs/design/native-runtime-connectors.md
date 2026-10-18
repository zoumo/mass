# 当前原生运行适配合同

当前产品支持本地 Codex 0.160.1 和同版本 codex-remote。Claude、ACP、数据库和在线实例管理均未交付。配置文件登记及只读实例发现见[启动指南](../../README.md)。[Runtime 终态架构](runtime-core-architecture.md)已批准，其他合同按票实施。

本文描述原生协议的适配责任和可观察行为，不绑定实现语言。源码、固定客户端选择、补丁、工具链及按阶段积累的证据保留在[原生接入实现记录](../develop/native-runtime-record.md)。旧 Claude 探针和规划也在该记录中，不作为当前能力。

## 资源和路由

每个本地 runtimeName 惰性拥有一个 app-server，多个原生会话共享它，不同本地名称不共享进程。Remote 名称只拥有独立连接，不保证远端服务隔离。原生 Agent 负责模型、工具、上下文和原生认证；MASS 负责准入、绑定、控制代际、事实归一化与持久化编排。

原生握手、请求响应关联和服务端回调关联属于适配责任。整数和字符串请求标识不能混同；客户端响应标识与服务端回调标识分别管理。通知只路由到明确所属 Attachment，未知会话不能落入任意 Session。

公共命令及处理器保留原 Attachment，不按名称寻找新连接。服务身份、连接身份和取消代际分别检查。参数校验和容量拒绝发生在交付原生协议之前，已经交付的操作继续按真实结果结算。

创建先预留目标名称、稳定实例和 operationId，不在原生打开期间阻塞所有 Session。原生创建返回的 sessionId 原样成为公共身份；MASS 不预分配会话 ID。30 秒初始化观察期限之后仍保留迟到打开所有权；未公开的会话必须结算并释放，释放未知时保留路由和名称，不能停止健康共享服务来掩盖单会话失败。

## 输入与完整事实

当前输入是非空有界文本块数组，不排队、合并或转向已有工作。同一会话有未结算输入时返回 busy；其他会话仍可使用共享服务。

适配向 turn/start 提交 clientUserMessageId，只有成功响应与匹配的完整 userMessage/clientId 都已观察到，才返回 native_recorded。两者可任意顺序到达。该回执不等于模型消费或 Journal 已提交，普通输入副本不能代替原生回显。

接受观察期限为 30 秒。明确原生拒绝映射为 MASS rejected；超时或失联返回 acceptance_unknown，保留 operationId、submissionId 及真实 sent/nativeAccepted/nativeRecorded 证据。sent 表示已观察到原生响应或事实，不是本地入队或直接写 socket 证明。未知结果保持准入限制，不自动重试；请求方消失不放弃已接纳操作。

完整消息、思考和工具事实映射见[当前事件协议](../events.md)。原生 item/started 提供完整工具调用投影，不证明执行已经开始；item/completed 提供完整结果。调用与结果共享公共条目身份，各有独立 seq。原生完整对象、来源与调用上下文按当前外部格式保留，恢复验证不得丢弃它们。

显式空输出是已提供的空内容，缺失输出是不可用。保留文本不代表无限完整输出。当前仅在固定版本的 unifiedExecStartup 成功 ASCII 输出具有已知 512 KiB 头部、正数省略标记和 512 KiB 尾部形状时判为截断；不按长度猜测。原生 diff/processId 不能冒充 stdout 或系统 PID。

## 审批与取消

<a id="approval-and-cancellation"></a>

当前会话使用 workspace-write、untrusted、user 审批选项，只对受支持 commandExecution 请求回答一次 decision=accept，不附加长期规则。其他控制请求明确拒绝。工具或回调上下文按原主机、连接、目标工作和条目归属保存，注册时及实际答复前均检查有效性。已解决请求立即失效，迟到决策不能重新批准。

普通输入与优先控制各有独立容量。每 Attachment 最多一个取消轮次，重复取消共享该轮结果，目标不能转向后续工作。取消关闭准入并使旧的未交付命令或许可失效。控制动作与向上游交付具有单一先后次序；已经交付的输入或许可不能追溯声称被本地阻止。

turn/interrupt 成功响应只证明中断控制已确认。实际工作结束、先前输入结算、服务端回调、延迟观察、已接纳事实交付和延迟策略全部结算后，才能重新开放输入。确认与终止通知可以任意顺序到达；期限后未知结果不被迟到确认改写为成功。原生工作身份迟到时仍由原取消责任继续处理，不能因 HTTP 期限而放弃。

完全未发送的输入可被本地取消，无需伪造原生中断。已交付但尚未得到响应的输入继续等待真实身份和结算。未完成工具可在真实工作结束后释放上下文，不因此生成工具成功结果。中断、断线或停止均不把预览补成完整历史。

精确公共错误、观察证据与先后要求见[取消 wire 合同](../http-api.md#native-cancellation-ticket-04)。

## 关闭与显式恢复

归档先阻止新输入和新许可，再结算目标输入、取消、回调、延迟决策和已接纳事实，最后解除订阅。真实 thread/closed 或 unsubscribe 的 notLoaded 证明释放；成功 ACK、notSubscribed 或空闲本身都不证明释放。己方服务已被确认回收可另外证明其资源已消失，EOF 本身不够。

closeTimeoutMs 为 1..300000，默认 30000，覆盖整个关闭过程。超时返回 close_unknown，保持 closing、禁用恢复并继续拥有原操作；迟到释放才能完成离线。单会话超时不能杀共享服务，也不能开始替代 Attachment。

本地恢复先重读未变化元信息，再验证全部已提交记录的格式、身份、来源、投影和 seq。全部成功后才能修未提交尾部并打开同一原生会话。没有新建兜底。新连接延续公共身份和下一 seq，不重放旧输入、工具或处理器动作；旧命令不改绑。

当前本地恢复的 Codex 绑定和证据规则由 Runtime 适配方校验，通用层只校验公共合同。Remote 恢复不支持，见下文限制。文件合同见[存储与恢复](../storage.md)。

## 容量、背压与失败归属

<a id="native-ticket-06-bounded-core-delivery-and-shared-transport-pressure"></a>

以下保留当前产品的逻辑容量，不是进程 RSS 或实际堆占用上限。配置名和数值属于当前运行合同，不因实现语言更换而自动改变。

| Boundary | Product limit |
|---|---|
| Ordinary / priority control requests | 32 each, including official RPCs awaiting responses; reservations survive upstream handoff and release only at actual response/disconnection settlement |
| Command parameters / prompt text blocks | 1 MiB serialized each; excess fails explicitly before native handoff |
| Opening or attached threads | 128 per host, including late provisional opens; release requires actual thread close or owned host reap |
| Normalized core lane | `factLaneCapacity` 1..64, default 64, including the executing fact; 32 MiB payload/key bytes per attachment |
| Tool / approval / full-fact fingerprint contexts | 128 each per attachment; 32 MiB shared context reservations per attachment; unfinished tool/approval settlement deadline 300 s |
| MASS host payload pool | 128 MiB shared across command parameters, core lanes and retained tool/approval/completion/deferred contexts |
| Official remote consumer | At most 32 events and `sdkEventBytes` 1 KiB..32 MiB (default 32 MiB), shared by initialization pending events, queued and consumer in-flight events |
| Official RPC result/error | 2 MiB serialized per response in MASS; at most 64 outstanding ordinary/control responses, hence at most 128 MiB of retained response payloads |
| Official WebSocket | 128 MiB maximum frame and assembled message; one current raw frame and decoded object are additional transient storage |
| Current normalization / encoding | One event is being normalized at a time in the host reader; temporary copies are bounded by that event/context and command/frame limits, separately from queue permits |
| Coordinates / acceptance gates | 4 KiB native item/turn coordinates; at most 64 delayed terminal/closed observations and 128 delayed incoming observations, also byte-budgeted |
| Core consumers | SSE 256 events per broadcast lane; each Handler has its configured finite count plus one executing delivery; immutable references share the committed object |

预算不能相加后当成进程总内存：当前帧及解码对象、归一化副本、编码临时内容和核心消费者保留各有额外成本；受信任扩展也能自行分配内存。

预览可丢且为完整事实保留空间。完整事实按顺序等待容量，不静默跳过，饱和 Attachment 的普通命令以 backpressure 拒绝。永久积压会传到共享连接，其他会话、响应和审批也可能延迟，有限内存不能保证无限生产下继续进展。

单个过大事实或 300 秒结算期限结束会报告历史不完整并关闭对应 Attachment。实际追加失败封闭写入者，修好路径不自动恢复；必须显式关闭和严格恢复。释放与核心排空都完成后，才允许归档成功或同服务替代绑定。释放请求若仅因本地容量拒绝，可重试，但每 Attachment 最多一个在途尝试。

初始化超出交付计数或字节限额时明确失败。常态完整事实等待容量。过大响应、损坏协议、非法二进制帧或连接大小/读取失败归为共享服务故障。合法未知通知只记录诊断，不按已知事实解析。已知方法的损坏内容只按明确、有限且属于该方法的原生 threadId 归属；不能定位时归服务故障，不能从错误文本猜会话或截断后继续猜帧。

## 停止与己方资源回收

共享服务故障使其 Attachment 失联并按未知处理在途工作，已提交历史保留。其他名称的服务不受牵连。只有之后的显式新建或恢复才在旧己方服务清理后激活新服务，只打开请求的会话，不重放输入。

停止先关闭新准入，排空已接纳核心事实，再关闭原生连接并回收己方子进程。排空期间新收到但未接纳的原生事件只作诊断，不作为历史或审批动作。当前原生连接关闭使用 5 秒总期限；进程清理保留终止与强制回收后备，并核对 PID 与启动身份。清理信号不是取消成功证明。

之后按处理器总期限排空并尝试关闭，超时不保证关闭完成。SSE 消费已排队发布后结束。永久阻塞文件 I/O、不协作扩展或永不读取的网络对端不在有限整进程停止保证内。停止不证明所有后台后代终止。

当前可执行能力见[API](../http-api.md)和[交互合同](../interactions.md)。验证入口见[测试指南](../testing.md)，固定源码与适配实现见[实现记录](../develop/native-runtime-record.md)。

Runtime 命令使用 InputCommand、InputReceipt、CommandError 等 MASS 类型，不接受 agentd 方法名或 JSON-RPC 包装。输入与取消错误只公开稳定分类、脱敏说明、MASS 操作关联和接受证据；原生 SDK 错误留在适配内部。取消接纳后丢失结果统一为 cancel_unknown，接纳前离线为 offline。

前台终止不撤销同一工作的原生事实归属。工具重复启动通知只更新原生启动元信息且命令输入一致时，不重复发布调用；已终止工作的迟到事实不能重新取得审批。适配器用有界、计入上下文预算的调用指纹保留去重依据，不将原生工作注册表放入通用层。

## Codex Remote

Remote 通过固定官方客户端连接已有 ws/wss 服务。握手版本必须为 0.160.1；版本声明只用于协议准入，不证明服务、账号或数据身份。新建、输入、审批、取消、关闭、归一化、持久事实和证据验证与本地共用同一处理栈。cwd 必须为远端绝对路径，不验证 MASS 本机存在性。

配置仅允许 url、可选 env:NAME 格式 authRef 和与本地相同的三个关闭/容量参数。携带 token 仅允许 wss 或回环 ws；URL 不得包含 userinfo、query 或 fragment。秘密仅在激活时解析，不写入状态、错误、历史、证据或恢复快照。无引用值明确失败，不用宿主其他凭据补齐。

Remote 不创建、重启或发送信号给服务进程。停止排空已接纳事实后结束己方连接和本地任务，status.stopped 仅证明这部分资源结束。没有原生释放证据的 Attachment 仍保留未知绑定；断线、关闭连接、取消 ACK 或实例停止均不能补造释放或工作终止事实。其他客户端可继续使用服务。

首版不支持任何 Remote 恢复，包括同进程重新连接和冷恢复。capabilities.resume=false，显式恢复返回 unsupported_capability，不启动连接、不修历史尾部、不发送 thread/resume 或替代 thread/start。稳定实例绑定和真实 sessionId 仍持久保存，离线历史可查询。相同 URL/ID 不是归属证明。本地绑定 v2 及非用户配置来源限制不变。

真实验收使用测试自有的无认证回环 app-server，验证文本、工具、取消、归档、断线、独立客户端存活；不代表真实 Bearer 认证服务或 WSS 部署已经验收。
