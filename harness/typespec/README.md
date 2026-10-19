---
type: Contract
title: TypeSpec 合同入口
description: Service、Runtime 与 Store 的接口版本与统一校验工具。
status: stable
interface_version: v0.1.0
sources:
- id: direction
  resource: 项目负责人在 2026-10-19 关于应用服务与运行时拆分的讨论
  title: 两个模块的职责与依赖
---

# TypeSpec 合同入口

当前接口版本记录在本页元数据。取得相应 harness 写入授权后修改当前合同，成批定稿时更新版本并在 [log.md](../log.md) 记录差异；历史由 Git 保存，不复制版本目录。

- [资源管理接口](../modules/service/main.tsp)：RuntimeService、SessionService；进程内 EventHandlerService 提供处理器登记、停用与重新启用。
- [运行时接口](../modules/runtime/main.tsp)：RuntimeRegistry、RuntimeDriver；Driver.events() 返回单消费者 EventStream<SessionEvent>，多个 Session 共用。
- [元数据存储接口](../modules/store/main.tsp)：MetaStore 按 Runtime 与 Session 绑定资源和查询类型；HistoryStore 提供批量追加、读取与同步进度。
- [状态值](../shared/states.tsp)：RuntimeState、SessionState，共享值不依赖 Service 资源 DTO。
- [交互事件](../modules/runtime/interactions.tsp)：permission/form/url 请求与关闭通知；[语义](../modules/runtime/interactions.md)。
- [配置选项](../modules/runtime/session-config.tsp)：select、分组选择与 boolean；[语义](../modules/runtime/session-config.md)。
- [共享值](../shared/types.tsp)：开放类型名、配置、能力、操作引用、交互回答与接受证据。
- [历史查询与分页](../shared/history-models.tsp)：Service 与 Store 共用 HistoryQuery、HistoryPage。
- [事件处理器](../modules/service/event-handlers.tsp)：进程内登记、按会话构造、交付、关闭与重新启用。
- [统一事件](../modules/runtime/session-events.tsp)：SessionEvent Base、事件类型与按 type 绑定的 payload 联合；顶层 _meta 为 Record<unknown>。
- [事件 JSON 预览](../modules/runtime/event-examples.json)：从 [TypeSpec 示例值](../modules/runtime/event-examples.tsp) 导出，覆盖 17 种事件及五种内容块。
- [内容块](../shared/content-blocks.tsp)：ACP 的五种 ContentBlock，无公共 Base；[字段说明](../shared/content-blocks.md)。
- [共享值类型](../shared/models.tsp)：输入、回执与 OperationError。
- [资源内部停止协调](../modules/service/coordination.tsp)：不进入运行时依赖。

运行时拥有统一事件声明，只导入运行时及共享值类型；Service 模块依赖运行时的会话选项合同和共享值类型。main.tsp 是统一阅读入口。unknown 的语义以字段声明为准；RuntimeConfig、InteractionAnswer、工具动态值与扩展 map 明确允许开放值，不是待决占位。

安装依赖和导出示例会写入 harness，执行前须有相应人类授权；check 与 format:check 用于只读校验。

```sh
rtk proxy npm ci --prefix harness/typespec --ignore-scripts --no-audit --no-fund
rtk proxy npm run check --prefix harness/typespec
rtk proxy npm run format:check --prefix harness/typespec
rtk proxy npm run examples --prefix harness/typespec
```

校验分别编译 Service、Runtime、Store 三个模块及资源内部协调，开启 noEmit 与 warningAsError；固定编译器和依赖锁，需要 Node.js 22 或更新版本。编译成功不代表并发、资源释放或实际 Agent 行为已经验证。

Store 的职责见 [Store](../modules/store/design.md)，已区分 MetaStore 与 HistoryStore；MetaStore CRUD、资源分页和实时订阅均已定义 TypeSpec；共享基础类型见 [types.tsp](../shared/types.tsp)，其开放值不是待决占位。Service 与 Store 共用 [历史模型](../shared/history-models.tsp)，保留类型过滤、历史游标和同步进度。SessionEvent 已定义统一信封和按 type 绑定的 payload，adapter 补齐名称后交付；内容块已定义，消息与思考按条目 ID 定位，不增加块坐标；交互与配置的适配细节见运行时事件文档。准确错误 code 及文件提交协议仍需细化。编译检查不表示已有存储实现。

事件预览通过标准 @example 编写，使用编译器校验并序列化。npm run check 同时检查示例是否覆盖所有事件类型、UUID 格式及生成 JSON 是否过期；它不验证所有跨字段业务条件或媒体正文的实际有效性。导出工具不增加额外依赖。
