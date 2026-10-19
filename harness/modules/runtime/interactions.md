---
type: Contract
title: Session 交互事件
description: 权限、结构化信息与外部流程的请求、回答和失效语义。
status: stable
sources:
- id: permission
  resource: https://agentclientprotocol.com/protocol/v1/tool-calls
  title: ACP 权限选项与 selected/cancelled 回答
- id: elicitation
  resource: https://agentclientprotocol.com/protocol/v1/elicitation
  title: ACP form/url elicitation 与响应
---

# Session 交互事件

准确声明见 [interactions.tsp](interactions.tsp)。这是 MASS 对 ACP 交互语义的适配，不要求公共调用者实现 ACP JSON-RPC，也不把 ACP 请求 ID 当作跨运行有效的交互凭据。

## 请求与结束

adapter 在收到原生交互时创建 InteractionRef，将它绑定到原运行、原生请求及相关工作，通过 session.interaction_requested 交付。事件信封已有完整 Session 身份；payload 携带 interaction、request、blocking，不再复制 sessionId。InteractionRef 在全部未结算请求中不冲突，不因恢复重用旧引用指向新请求。

request 有三种形态：

| kind | 内容 | 回答示例 |
|---|---|---|
| permission | toolCallId、可选 title/content 与原生 options；每项含 optionId、name、kind | {"outcome":"selected","optionId":"allow-once"} 或 {"outcome":"cancelled"} |
| form | message 与开放的 requestedSchema | {"action":"accept","content":{"strategy":"balanced"}}、{"action":"decline"} 或 {"action":"cancel"} |
| url | message 与 url，表示外部流程 | {"action":"accept"}、{"action":"decline"} 或 {"action":"cancel"} |

权限 kind 沿用 ACP 的 allow_once、allow_always、reject_once、reject_always 展示提示；真正选择按原请求的 optionId 验证，不能凭 kind 推断授权或自行生成“永远允许”选项。拒绝选择原生 reject 选项，取消表示未选择，不能混同。

InteractionAnswer 继续保持动态值，以上为对应 kind 的标准约定；adapter 验证请求类型、答案结构与选项范围。form 的 schema 由具体交互校验，不宣称实现了任意 JSON Schema。真实 ACP adapter 还须满足 ACP 自身的 schema 和能力协商要求。

URL 事件只提供外部流程入口，不自动打开地址。accept 只表示同意进入外部流程，不证明授权完成；后续必须由原生端的完成事实结算，凭据不通过事件或回答回传。仅支持 permission 的 Driver 不得宣称支持 form/url。

adapter 在回答已被原生端接受、请求被取消/失效或外部流程明确完成后发出 session.interaction_closed，携带 interaction、outcome 和可选 reason。answered 结束普通 permission/form 请求；URL accept 之后仍等待原生完成，最终使用 completed。decline/cancel 也需要原生结算证据；不能因为 UI 点击或请求发出便提前关闭。没有结算证据时保留未确定责任，不重新发送或绑定新交互。

## 回答与竞态

调用者通过 SessionService.answerInteraction(session, interaction, answer) 回答；Driver 校验原 Session、原运行与交互仍可回答，然后返回接受证据。事件先于调用返回也是合法时序，调用者不能依赖先 receipt 后 closed 的顺序。

并发回答在单个请求内串行接纳，至多一个回答进入原生提交；重复、过期和已结束请求明确拒绝，未知结果不能自动重发。关闭/恢复/停止不能让旧回答转发到新请求；原请求的迟到关闭只结算旧请求，不改变新运行状态。

取消工作时，ACP adapter 负责按 ACP 协议向待决权限请求提交 cancelled；公共取消接受并不意味着这些请求已完成结算。交互通知只反映原生事实，不会因某个观察者结束订阅而取消。

## 状态与交付

blocking=true 的请求阻塞当前工作，Service 将可用运行投影为 requires_action。可以同时存在多个待决交互，Service 只维护交互标识、原运行关联及必要控制信息，不缓存消息 delta。一个交互关闭不说明其他交互关闭，也不证明工作已重新开始或结束；需依据后续工作事实进入 running/idle。

两种交互事件均为实时控制通知，当前不写入 HistoryStore，也不携带答案内容或秘密。subscribeEvents 不补历史，晚订阅可能错过既有请求；当前未提供待决交互列表查询，不能宣称支持断线后自动恢复交互界面。未来若需要，可单独增加只读当前交互查询，不从历史重放权限动作。
