---
type: Module Design
title: 异步加载原生历史
description: 导入已知原生会话，沿既有校验、提交和分发链异步回放。
status: draft
sources:
  - id: sdk-evidence
    resource: ../../../mass-go/docs/native-history-sdk.md
    title: 原生 SDK 加载接口与离线验证边界
  - id: direction
    resource: 项目负责人在 2026-10-21 授权 SDK 异步历史加载及相关 harness 更新
    title: load 异步接纳，不等待全部历史处理完成
---

# 恢复原生会话并异步回放历史

loadSession 给定 Runtime、绝对 cwd、已知原生 sessionId 和新的 namespace/name，
恢复同一个原生会话，异步回放其全部保留主历史；本地提交和分发结算后，沿恢复的同一
会话继续输入，无需再次 resume。普通 resume 只恢复执行连接，不做这次历史导入。
load 不新建替代会话，不发送隐藏 prompt，不重跑历史工具。

原生加载句柄须具有异步历史接收、结束原因、取消以及恢复后继续输入的能力。恢复、
回放和正常运行复用原生 SDK 的既有 resume 和消息接口。SDK 接口及实现证据只在
sources 的 sdk-evidence 中记录；不为通用合同规定具体语言的并发容器。

## 接纳、身份与恢复

首版只接受新的名称、全局 sessionId 及空公共历史；不合并、覆盖或自动续传。不同
Runtime 指向同一数据源也不得重复导入同一 ID。名称、ID 与原子预留沿用
[Session](session.md)。Runtime 须已启动并支持同一会话恢复与完整保留历史回放；
不隐式启动已停止实例。

Service 在调用 Driver 前固定原 Driver、Runtime 配置、loadId、名称和预期原生 ID，
建立有界接收绑定，确保唯一 Driver 流的接收循环已运行。Driver 校验原生身份、cwd、
账号及数据来源，并使用普通原生 resume 恢复该 ID，然后才交付历史。不存在、不支持、
身份不符或恢复失败明确失败，不以新建或更换原生 ID 替代。原生只报告请求 ID 而未
报告实际观察 ID 时，必须保留证据区别，不能把请求值伪造为原生观察。

接纳后持久保存 Session 与 historyLoad 状态，返回 SessionLoadAccepted，不等全部
回放、历史追加或 publish 完成。提前到达的事实按原 load 有界暂存，身份落盘成功后
才进入提交。落盘失败取消本次加载并释放己方恢复资源，未知时保留责任。接纳不证明
原生存在或可输入；nativeVerified、nativeResumed 和最终结果各自需要证据。

## 单一事件管线

先建立接收再生产。adapter 沿正常内容转换处理原生完整消息、思考及工具事实，保留
raw、原生 ID、parent、时间及分页证据，不另建 MASS 历史格式，不将历史快照伪装为
原生 live 通知。历史统一事件携带 replay.loadId 及可证明的记录/parent 身份。
历史来源不授予当前执行或旧交互控制能力；当前控制权仅来自成功恢复的原运行绑定。

历史内容和 session.history_replay 标记共用唯一 RuntimeDriver.events()：adapter
转换 → Service 校验原 load 绑定及顺序 → EventStore 提交持久事实 → 原 Session
Broadcast.publish。Service 在公共读取循环外按 Session 串行结算。不另建历史总线、
Driver 公共流、持久化 Handler 或绕过 retired/work 校验的注入入口；过滤须明确识别
合法 load 来源，未知旧运行仍不能改绑到当前运行。

完整性只指声明范围内全部保留主历史。压缩摘要保留，原生已删除的正文、旧分支、
不支持的独立子代理文件或未保存 token 不得伪造。工具调用与结果保持原生关联，缺失
结果不补成 completed；输出保留缺失、空值及截断区别。损坏、缺 parent、未知不可转换
内容或分页未穷尽明确失败，保留部分事实，不静默跳过后称完整。

历史 idle/running、配置、权限及旧交互不作为当前通知发布，不投影当前工作或待审批，
不恢复旧 AnswerHandle。首版历史只投递五种既有持久内容类型及回放控制类型，其他
记录作原生校验/范围证据。恢复期间的真实当前通知仍属于原恢复绑定，Service 在放开
业务输入前按来源及顺序结算；不能用历史状态填补当前观察。Handler 不重跑历史动作。

## 接纳、原生结束与可继续输入

Driver 在来源校验及同一原生会话恢复成功后发送 started，之后依次交付历史，最后
发送唯一原生读取终止标记 native_completed、failed 或 canceled。started 确认
nativeVerified/nativeResumed；早期失败允许直接 failed。终止标记排在全部已接纳
历史之后，Driver 共享流 EOF 不替代它。标记不是持久对话内容或当前 Agent 状态。

native_completed 证明上游回放穷尽，并携带同一恢复会话的身份/能力与可证明的当前
状态；成功时保留该恢复连接，不释放它。Service 仍须确认所有前序持久追加及 publish
调用已结算，恢复绑定未失败，再保存 completed/deliveryCompleted、应用版本及能力，
放开该 Session 的正常输入。无需再次 resume，后续输入和 live 事件继续同一运行。

completed 需要 nativeResumed、nativeCompleted、deliveryCompleted 均为 true。
cleanupCompleted 表示失败/取消时己方资源与待办已清理，成功 load 保留可用连接，
不以 cleanupCompleted=true 作为成功条件。publish 成功只证明 Handler.handle 返回，
不证明外部或普通异步动作完成；syncedEventId 也不证明原生穷尽或分发完成。

加载期间 Session 为 opening，并拒绝 prompt、resume 和普通冲突操作；close 仍从任意
状态接纳，建立关闭栅栏并接管已接纳 load 的交付与清理。查询、历史读取、订阅和取消
加载可用。本地完成后根据真实当前证据更新状态，不能据旧历史宣称 idle；缺少当前状态
时保持 unknown，但业务准入可使用已验证的当前恢复绑定和无己方未结算工作的证据，不能
仅凭状态字符串推断。后续普通 sendPrompt 仍由 Driver 核验连接与原操作证据，断线或
不确定工作不授权继续输入。

close 只有在已证明无需 SDK 目标时，才按 [Session close](design.md#session-close)
的 MASS 清理例外终结该 load 的本地责任：当前状态为 `unknown` 且没有句柄，或原操作
明确证明没有产生原生资源。保留已提交部分与真实错误，不标记 completed、不重放历史
或开放输入；仍有待结算追加、发布或取消责任时保持 `closing`，再次 close 继续这条
清理而不要求 SDK 句柄。仅有句柄暂时缺失不构成清理依据；仍在执行的 open、resume 或
load 保持 `closing` 并接管原结果，结果未确认时不得进入 `offline`。

## 失败、取消与所有权

任何原生恢复、历史读取、转换、校验、追加或 required publish 失败都使 load 失败，
不能开放输入。Service 保留最初错误，向原 Driver/sessionId/loadId 请求取消加载，
持续消费共享流并结算已接纳事实、原 UUID 追加与己方恢复资源。失败事实不被后序
发布越过。资源或存储未结算时保留绑定与责任，phase=failed 不等于释放完成。

取消加载须解除慢消费及分页等待，并释放本次恢复的会话连接/子进程；不取消其他
客户端的工作或关闭健康共享 Driver。Driver 的取消方法只有确认己方原生资源释放才
成功；结果未知报错并保留责任。Service 取消入口仍可异步接纳，最终查询 canceled 和
cleanupCompleted。即使原生回放已结束，只要本地完成尚未结算，取消仍须释放本次
保留的恢复连接；已存在 native_completed 不再生成第二个读取终止标记。

恢复请求已派发但响应未知时，不能假装从未恢复或干净释放，不能重试 Load/Resume
或开放输入。仅当具体 Driver 证明原所有权已结算才解除占用；需要关闭原共享连接才
能确认时，保留 unknown 清理责任，不为单 Session 自动停止其他健康会话。清理成功
不把原失败改成 completed。失败/取消保留部分历史及唯一预留，首版不支持部分导入
自动重试或公开清除；普通 resume 不能用来掩盖不完整导入。

HTTP 返回或客户端断开只结束请求等待。后台 load 的上下文持续至本地结果结算；
成功后转为普通 Session 运行生命周期，结束回放上下文不关闭恢复连接。成功后的 close
按 closeSession 释放运行；已完成 load 的旧取消请求明确拒绝，不误取消后来输入。

Runtime stop 封闭新 load，覆盖尚在验证/落盘的预留和接收绑定；取消在途加载，结算
已接纳回放及清理；对已成功 load 的会话沿普通 cancel/closeSession 排空运行。确认
会话及原操作排空后才调用 Driver.stop，并结算停止期间的最终事件。持续消费原流并保留 Broadcast/健康 Handler
与订阅，未知时保留栅栏。应用 shutdown 同样如实结算，见
[Service 停止协调](design.md#停止-runtime-的协调职责)。跨重启的未结算 load 不自动从头回放或恢复输入。

## 观察与验收

Session.get 的 historyLoad 区分接纳、来源验证、原生恢复、原生穷尽、本地完成、失败
和清理。SSE 只观察订阅之后的同一 Broadcast；晚订阅者查询已提交历史，不承诺响应
与订阅就绪无缝衔接，不提供第二条历史补发总线。

验收覆盖同一 ID 恢复后再回放、全部保留主链多页/首条/顺序/工具关系、损坏与身份错误、
两会话隔离、有界慢消费、分页/启动取消、晚期读取错误、本地追加/required 发布失败、
原生结束领先本地完成、清理未知、重复/冲突、重启未结算、旧状态和交互不复活；还须
验证完成前输入拒绝、完成后无需 resume 即继续同一原生会话、回放上下文结束不关闭
成功会话，以及普通 resume 不自动回放。合成合同、原生协议探测与真实模型历史验收
分开报告；SDK 或 TypeSpec 通过不证明 MASS 整链已接通。
