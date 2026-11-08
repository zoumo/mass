---
type: Contract
title: TypeSpec 合同入口
description: Service、Runtime 与 Store 的接口版本与统一校验工具。
status: stable
interface_version: v0.2.20
sources:
- id: direction
  resource: 项目负责人在 2026-10-19 关于应用服务与运行时拆分的讨论
  title: 两个模块的职责与依赖
---

# TypeSpec 合同入口

当前接口版本记录在本页元数据。取得相应 harness 写入授权后修改当前合同，成批定稿时更新版本并在 [log.md](../log.md) 记录差异；历史由 Git 保存，不复制版本目录。

当前修订在 RuntimeSpec 顶层增加 Session 默认值 `features` 与 `mcpServers`，不放入类型专属 `config`。Runtime 启动和 Driver 构造不消费默认值；Service 在 createSession、resumeSession 与 loadSession 时读取当前 Runtime spec，features 按字段、MCP server 按名称与 Session 显式值合并后交给 Driver。同名 feature 覆盖，同名 MCP server 整体替换并保持原位置，新名称追加；Session 空 MCP 列表不清空 Runtime 默认项。合并结果为 nil 时保留 provider ambient，非 nil（含空列表）是完整有效列表，adapter 必须排除其他 ambient server；远端 URL 与秘密引用受公共安全约束。Runtime 更新对下一次 Session 打开操作生效，已打开 Session 不热更新；Session spec 只保存显式值。loadSession 接收合并后的完整 DriverSessionSpec。接口修订为 v0.2.20。

此前 v0.2.19 修订删除 Session `archived` 与 archive 操作，统一使用 close/offline：close 从任意状态接纳；需要调用 SDK 时，SDK Session close 成功是唯一 native 成功条件，随后仍须结算 MASS 内部责任才能进入 offline。失败或结算未知保持 closing 且可显式重试；idle Session 默认 10m 后走同一 close 路径，`0` 禁用且不停止 Runtime。create/resume 按需启动 Runtime，prompt/answer/cancel 不隐式恢复；服务重启不启动历史运行，旧在线状态校正为 unknown，旧 archived 记录迁移为 offline，查询与写入不再接受旧状态。冷进程没有当前运行映射的 unknown Runtime 只由显式 start/resume 接管，不证明旧 provider 工作或后端资源已经结束。

此前 v0.2.17 修订增加可扩展的 Session features，首个开关 agentQuestions 默认开启，Driver 以 effectiveFeatures 返回实际生效值，不支持时降级为 false。更早修订已明确 EventStore 自动增量同步与失败续接、Service 独占当前运行映射和完整停止操作，Registry 只负责配置校验与 Driver 构造。停止只暴露 stopRuntime，不固定内部多步协调 Interface。设计变化历史见[更新记录](../log.md)，实现差距与检查证据见[实现差距记录](../implementation-gaps.md)。

- [资源管理接口](../modules/service/main.tsp)：RuntimeService、SessionService；历史存取与实时观察是分开的业务操作。
- [运行时接口](../modules/runtime/main.tsp)：RuntimeRegistry、RuntimeDriver；Driver.events() 返回单消费者 EventStream<SessionEvent>，多个 Session 共用。
- [元数据存储接口](../modules/metastore/main.tsp)：MetaStore 按 Runtime 与 Session 绑定资源和查询类型；MetaStoreConfig 选择唯一权威后端，默认 sqlite。
- [事件存储接口](../modules/eventstore/main.tsp)：EventStore 提供批量追加、读取与同步进度；文件权威，SQL 可重建。
- [状态值](../shared/states.tsp)：RuntimeState、SessionState，共享值不依赖 Service 资源 DTO。
- [交互事件](../modules/runtime/interactions.tsp)：permission/form/url 请求与关闭通知；[语义](../modules/runtime/interactions.md)。
- [配置选项](../modules/runtime/session-config.tsp)：select、分组选择与 boolean；[语义](../modules/runtime/session-config.md)。
- [Runtime Session 默认配置](../modules/runtime/session-defaults.md)：Runtime 顶层默认值与 Session 覆盖的字段级/按名称合并规则。
- [共享值](../shared/types.tsp)：开放类型名、配置、能力、操作引用、交互回答与接受证据。
- [历史查询与分页](../shared/history-models.tsp)：Service 与 Store 共用 HistoryQuery、HistoryPage。
- [Events](../modules/events/main.tsp)：单 Session EventBroadcast；publish、addHandler、subscribe 和 close，仅实时分发。
- [事件处理器](../modules/events/event-handlers.tsp)：只读 required 属性（逻辑读取操作）、handle(SessionEvent) 与 close，无 name/order 或运行上下文。
- [实时订阅](../modules/events/observation.tsp)：队列 Handler 封装的可取消异步流；逻辑接收/错误查询不是目标语言必须复制的方法。
- [Go 订阅映射](../bindings/go-events.md)：只读事件 channel、Err 和 Close，承载同一异步流合同。
- [统一事件](../modules/runtime/session-events.tsp)：SessionEvent Base、事件类型与按 type 绑定的 payload 联合；顶层 _meta 为 Record<unknown>。
- [事件 JSON 预览](../modules/runtime/event-examples.json)：从 [TypeSpec 示例值](../modules/runtime/event-examples.tsp) 导出，覆盖 18 种事件及五种内容块。
- [内容块](../shared/content-blocks.tsp)：ACP 的五种 ContentBlock，无公共 Base；[字段说明](../shared/content-blocks.md)。
- [共享值类型](../shared/models.tsp)：输入、回执与 OperationError。
- [Service 停止协调](../modules/service/design.md#停止-runtime-的协调职责)：行为保证属于 Service，内部协调结构不作为固定 TypeSpec Interface。

运行时拥有统一事件声明，只导入运行时及共享值类型；Service 模块依赖运行时的会话选项合同和共享值类型。main.tsp 是统一阅读入口。unknown 的语义以字段声明为准；RuntimeConfig、InteractionAnswer、工具动态值与扩展 map 明确允许开放值，不是待决占位。

安装依赖和导出示例会写入 harness，执行前须有相应人类授权；check 与 format:check 用于只读校验。

```sh
rtk proxy npm ci --prefix harness/typespec --ignore-scripts --no-audit --no-fund
rtk proxy npm run check --prefix harness/typespec
rtk proxy npm run format:check --prefix harness/typespec
rtk proxy npm run examples --prefix harness/typespec
```

校验分别编译 Service、Runtime、MetaStore、EventStore、Events 五个模块，开启 noEmit 与 warningAsError；固定编译器和依赖锁，需要 Node.js 22 或更新版本。编译成功不代表并发、资源释放或实际 Agent 行为已经验证。

存储职责分别见 [MetaStore](../modules/metastore/design.md) 与 [EventStore](../modules/eventstore/design.md)；MetaStore CRUD、资源分页和实时订阅均已定义 TypeSpec；共享基础类型见 [types.tsp](../shared/types.tsp)，其开放值不是待决占位。Service 与 Store 共用 [历史模型](../shared/history-models.tsp)，保留类型过滤、历史游标和同步进度。SessionEvent 已定义统一信封和按 type 绑定的 payload，adapter 补齐名称后交付；内容块已定义，消息与思考按条目 ID 定位，不增加块坐标；交互与配置的适配细节见运行时事件文档。HTTP 已定义已知错误 code 的状态码映射，code 仍开放；各业务拒绝场景的准确分类及文件提交协议仍需细化。编译检查不表示已有存储实现。

事件预览通过标准 @example 编写，使用编译器校验并序列化。npm run check 同时检查示例是否覆盖所有事件类型、UUID 格式及生成 JSON 是否过期；它不验证所有跨字段业务条件或媒体正文的实际有效性。导出工具不增加额外依赖。
