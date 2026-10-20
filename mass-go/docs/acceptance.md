# 六单 synthetic 验收矩阵

本结果仅对应本次 Go 重建及固定 SDK
`github.com/zoumo/agent-sdk-go v0.0.0-20261018163950-6dbf3e9c8264`。
所有 native trace/scenario 都是 **synthetic**；使用真实 SDK 与 SDK 自带严格
mock CLI 子进程。fake 不执行工具、不访问模型或账户，不证明智能或真实 CLI 能力。

## 仓库入口

```sh
make prototype-go
```

这条命令实际构建两个 SDK mock、massd 和 massctl，然后运行
`TestEntrypointSuite`。不是调用 SDK probe 来代替最终产品入口。
测试通过 CLI 发出请求，经 HTTP v1 REST 调用 Service；实时观察来自
CLI → SSE → Service 独立订阅。测试包含原 ID 恢复后的独立新输入。

| 场景 | Codex | Claude | 直接证据/含义 |
|---|---|---|---|
| 无输入创建 native ID，无隐藏 prompt | PASS synthetic | PASS synthetic | `Test*BasicRecovery`；Claude init 仅证明身份，初始 work state 可为 unknown |
| 单独文本输入和接受回执 | PASS synthetic | PASS synthetic | CLI `session.input`；回执不等于工作完成 |
| 原 handle 自动同意权限 | PASS synthetic | PASS synthetic | strict scenario 等待正确请求答复；显式 daemon flag；不伪造 nativeAccepted |
| 用户问答，显式读取 form 后回答 | PASS synthetic | PASS synthetic | `TestCodexQuestion` / `TestClaudeQuestion`；与权限分开执行和报告 |
| native 问答先于 turn ACK | PASS synthetic | 不适用 | `TestCodexQuestionBeforeACK`；有界暂存直至可证明原 work ID |
| SSE ready 后实时观察、chunk | PASS synthetic | PASS synthetic | `Test*BasicRecovery`；ready 前已注册，chunk 不入历史 |
| 每个已发布完整事实等于历史读取 | PASS synthetic | PASS synthetic | SSE UUID/time/payload 与分页后的持久查询逐条比较 |
| 文件全量历史、过滤和分页 | PASS synthetic | PASS synthetic | limit=2 翻全历史，精确 type 过滤；停后只读打开 Store 比较完整结果 |
| 关闭观察不取消工作 | PASS synthetic | PASS synthetic | `Test*Cancellation`；断订阅后查询仍 running，再显式 cancel |
| 取消确认与后续终止分离 | PASS synthetic | PASS synthetic | strict cancel fixture；确认 idle 后才 archive/stop |
| archive/stop、原 ID/UID/history 恢复 | PASS synthetic | PASS synthetic | `Test*BasicRecovery`；更新期望 scenario → stop/start → resume → 新输入 |
| 同连接再次输入 | 有界，原 work ID 校验 | 明确拒绝 | Claude 每次运行仅一个输入；测试验证拒绝后 archive/原 ID resume，未绕过限制 |
| Session ID/名称寻址、列表及 Runtime 管理 | PASS synthetic | PASS synthetic | product suite；资源查询只经 Service；Runtime update 不热更新 |
| 同 daemon 双 Agent 及跨 Runtime 目录分页 | PASS synthetic | PASS synthetic | `TestDualAgentDirectory`；两个 native peer 同时存在，next/过滤绑定与独立路径读回 |
| 参数错误、未知操作、游标及空 types | PASS | PASS | product suite；稳定公开错误，非法输入不静默省略或自动重试 |
| 缺 native identity 的失败创建与清理 | 未单独在最终 CLI 注入 | PASS synthetic | `TestFailedCreateCleanup`；无持久 Session，stop/delete 结算，同名重建可用；Codex 故障见前票 suite |
| 慢 SSE 写入导致订阅过载 | PASS synthetic | 共用传输合同 | `TestFailureSuite/TestSSEOverflowIsExplicit`；真实 Service/SDK；140 完整事实保留，明确 error，无正常 end |
| 异常断线不能冒充正常 EOF | PASS 协议测试 | 共用客户端 | `pkg/client` framing suite；仅 end 正常；异常保持原错误，Close 释放阻塞 Next |
| 正常/失败测试资源清理读回 | PASS | PASS | daemon exit、端口无法连接、目录删除读回；TestMain 核对己方 SDK/application 进程 |

最终完整离线组另含 Ticket01 SDK probe、Store、Service/Runtime 纵向及故障验收。
前票 probe 的通过只证明其独立协议场景；上表明确使用最终产品入口证据。

## 能力和未验证范围

| 项目 | 结果和边界 |
|---|---|
| 文本输入/content | 已实现 text-only；Claude 恰好一个文本块。完整文本/思考多块、工具输入/结果动态值、空/null/缺失由前票纵向测试覆盖 |
| 不支持的完整内容 | 明确失败；图像、音频、文件、任意原生扩展与 subagent 归属未实现，不能把文本化替代说成支持 |
| 交互 | permission/form 原 handle；没有待决交互补查、自动到期通知或跨连接交互续传；表单仅按原生结构显式回答，不宣称通用 JSON Schema 校验 |
| 事件处理器/API | 不提供 harness 全部业务 handler 注册/enable/disable API，也无 Session 配置选项 API；原型只有显式权限策略 |
| Store | 完整文件快照已实现并离线验证；SQLite/索引重建未实现；私有恢复协议是 Go 实验选择 |
| 故障结算 | 前票 suite 保留未知责任、原 UUID、部分写入、有界过载和原工作隔离；本进程显式结算有效，不等于 crash-recoverable outbox |
| 崩溃/掉电/跨进程未知责任 | 未验证且跨进程未知操作自动结算未实现；已持久 unknown/未确认运行不静默当作可恢复 |
| 真实 CLI、原生持久恢复、模型、账户、工具执行 | 未验证；fake 不证明 Claude 真实无输入 init、真实 resume ID 消费或模型行为 |
| Linux、Rust | 本次未验证；本次是 macOS 离线 Go 实验，不声明生产发布 |
| HTTP v1 | 已按定稿合同迁移 REST/JSON/SSE、实例 Bearer 认证、TLS 入口和 HTTP 接纳探针；本页六票记录为先前合成验收，新增协议验收见 [HTTP v1 验收](http-v1-acceptance.md) |

没有把失败或未验证写为通过。开发期全量检查曾暴露一个 Codex 双 SDK 队列时序问题：
请求可能先于工作确认到达；当前由原 Session 的有界工作队列处理并增加早到问答场景。
同时修正故障测试对“running 必先于 request”的假设；独立 SDK 队列没有该顺序保证。
最终结论只以冻结源码后的成功检查为依据。

## 六单提交与证据

| 票 | 提交 | 主要内容 | 本地证据 |
|---|---|---|---|
| 01 | `9a9916e14` | 固定 SDK 的双 fake 协议探针 | `examples/sdkprobe/evidence`；scratch evidence/01 |
| 02 | `9643179d4d039f943dd0e479e1baaba85ac7d618` | Codex Service 与完整文件 Store | scratch evidence/02 |
| 03 | `afac6237f7984ac4c42b5629b68835c6dcb2cb66` | Claude、显式权限及独立问答 | scratch evidence/03 |
| 04 | `7afb7307141711f0a102b809d2755ada9aeb060f` | 资源配置和原 ID 恢复 | scratch evidence/04 |
| 05 | `2c7e70c7af3948b980bc400446ec4e4c6505e399` | 故障/隔离/停止结算 | scratch evidence/05 |
| 06 | `git log -1 --format=%H --grep='feat(go): expose experimental CLI and HTTP SSE acceptance'` | 产品入口及最终验收 | scratch evidence/06 |

scratch 统一位于仓库 `.scratch/mass-go-from-zero/`。每票 issue 记录命令、边界与证据；
`execution.md` 保存六票完成状态和本票最终 SHA（提交生成后读回）。本票证据包含
fmt/vet/build/full offline/race/`make prototype-go` 输出、源码前后摘要、结构/harness
检查与清理读回。源码摘要覆盖 Go 源码、fixture、go.mod/go.sum 及根 Makefile；
验证期间这些输入必须不变。证据是本机实验结果，不是跨平台或真实模型认证。

六票均串行直接提交 v2，不建分支/worktree、不 push。harness 对 `0cfe97729`
无差异；固定 Ticket01 probe 对 `9a9916e14` 无差异。生产代码只在 `pkg/`
（`cmd` 除外），没有旧 managed/catalog、PublicEvent/EventProjector 或 seq 历史兼容路径。
