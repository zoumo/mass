---
type: Contract
title: HTTP 承载
description: Service 接口的 HTTP 映射职责与尚未定义的传输合同。
status: draft
---

# HTTP 承载

## 职责

HTTP 将请求交给 [RuntimeService](../modules/service/runtime-service.tsp) 和 [SessionService](../modules/service/session-service.tsp)。参数、目标、成功结果与 OperationError 的业务语义和进程内调用一致；HTTP 不遍历原生 Agent 聚合目录，不隐式启动 Runtime 或恢复 Session。

资源和事件结构采用当前 TypeSpec。Runtime 为全局命名资源；Session 按 namespace/name 或 sessionId 寻址，通过 spec.runtimeRef 名称归属 Runtime，不比较 Runtime uid。资源状态使用 RuntimeState 和 SessionState。

## 操作与结果

Runtime 的创建、查询、列表、全量配置更新、启动、停止和删除分别映射 RuntimeService 对应操作。Session 的创建、查询、列表、恢复、归档、输入、交互回答、取消、历史查询与订阅分别映射 SessionService 对应操作。当前不提供 JSON Patch、Session 更新/删除或会话配置选项设置。

业务成功结果直接承载对应资源、回执或分页结构。OperationError 使用 code、reason、message；outcome_unknown 表示副作用结果无法确认，不能映射为“确定未执行”或自动重试。

Session 名称、Runtime 名称及 sessionId 均不直接用作用户可控文件路径。cwd 按 Agent 所在运行环境解释，远端路径不能被本机目录存在性检查拒绝。

## 历史与实时观察

历史查询使用共享 [HistoryQuery/HistoryPage](../shared/history-models.tsp)：types 精确 OR 过滤、after/next 为不透明 HistoryCursor，返回 events 与 syncedEventId。事件 UUID 不用于排序或分页。

实时观察映射 [EventSubscription](../modules/service/observation.tsp)，只接收订阅后的事件，不自动补历史。订阅关闭或传输断开只结束观察，不取消工作、归档会话或停止 Runtime。过载异常必须明确表达，不能把不完整交付伪装成正常结束。当前没有跨连接续传或待决交互补查合同。

## 待定义的传输合同

- URL、HTTP 方法、请求/响应 envelope，以及是否采用 JSON-RPC 或 SSE。
- 各业务操作、错误及订阅终止的准确传输映射。
- 认证、授权、公开配置与动态值编码。
- 订阅建立的生效时点、连接取消与服务器关闭行为。
- 存活、就绪及多 Runtime 健康聚合的接口与判定规则。

在上述事项定稿前，本页不提供可直接实现的完整 HTTP 协议，也不声明任意语言实现已符合该协议。
