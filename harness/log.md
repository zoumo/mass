# 设计知识包更新记录

## 2026-10-20

- **定稿 HTTP v1**：按负责人“先起草、由另一 agent 审阅，无问题后写入 harness”的条件授权，经独立 agent 审阅、修订与复核通过，确定资源式 REST JSON API 与可选 SSE，不采用公共 JSON-RPC。明确 Runtime/Session 路由、两种会话寻址、输入/回答/取消回执、历史分页、错误映射、SSE ready/终止边界、实例级 Bearer 认证与健康探针。不透明 Session ID/交互引用的路径片段使用规范 base64url，JSON 中保留原值；错误 code 保持开放。接口知识包修订为 v0.2.0，Service/Runtime/Store 签名未改。幂等键、Operation 查询、细粒度授权、待决交互补查、webhook 与连续续传不在本版；定稿和 agent 文档审阅不代表实现或真实 Agent 验收完成。

- **更新确认状态**：按负责人要求，将已确认的整体架构、模块设计、事件与共享结构、工作原则、词汇及知识管理/合同入口从 draft 更新为 stable。HTTP 与持久化承载保留 draft，页面内的待决事项继续保持未确认；状态不代表实现或运行验收完成。

- **明确设计文档命名**：整体逻辑架构统一为 architecture.md，Service、Store、Runtime 的主设计统一为模块目录下的 design.md；补充整体架构的范围说明，同步索引与仓库引用。TypeSpec 工具使用入口保留 README.md。

- **建立 Harness**：知识目录迁为 harness，作为项目演进与发展的唯一真相源；开发原则归入 principles，增加 agent 工作入口及事先授权的写入规则。同步仓库入口、语言资料与校验工具路径，知识结构遵循 OKF v0.2。

- **整理当前设计**：文档独立描述 Service → Store / Runtime 的职责与依赖；接口和结构以当前 TypeSpec 为准，具体实现与真实 Agent 验收另行验证。
- **名称归属**：Session.spec.runtimeRef 持久保存 Runtime 名称，不保存或比较归属 Runtime 的 uid；停止协调按名称固定原 Driver。原运行、原工作和迟到操作继续按各自关联结算。
- **资源与配置**：Runtime、Session 使用 metadata/spec/status；状态采用 RuntimeState、SessionState，Reason 为开放字符串。Runtime 配置更新为全量替换，不热切换当前 Driver，不提供 JSON Patch。
- **存储合同**：MetaStore 提供资源 CRUD、generation 与 resourceVersion；HistoryStore 直接追加与查询统一事件。文件保存完整数据，SQLite 为可重建索引，准确文件提交协议仍待定义。
- **运行时合同**：RuntimeRegistry 校验配置、构造并登记未启动 Driver、按名称查找和移除；RuntimeDriver 执行实际启停与原生会话操作。每个 Driver 提供一条由 Service 独占消费的多会话事件流。
- **统一事件**：公共信封包含完整 Session 名称与身份，adapter 在打开/恢复时绑定名称并补齐事件；每种 type 固定 payload。eventId 为一次生成的 UUID，不表示顺序或原生事件去重。
- **内容与增量**：message、thinking 共用消息模型、条目身份及内容块规则。chunk 只追加，MASS 不跨事件累积 delta，也不依靠拼接生成完整历史。内容采用 ACP 的五种 ContentBlock。
- **观察与交互**：资源分页、历史查询和实时订阅已有声明；Service 与 Store 共用历史查询/分页模型。交互请求和关闭使用独立事件，配置选项采用 ACP 风格的选择、分组和布尔结构。
- **事件处理器**：进程内按 Session 构造并串行处理，支持登记、关闭、停用与显式启用；业务消费与核心持久提交分离，不提供可靠业务重放。
- **开发规范**：语言无关编码原则与测试验收要求独立维护在 principles/；远端认证、秘密、原生身份与资源所有权要求随 Runtime 维护。
- **删除过时内容**：删除旧文档来源清单、被替代的设计记录及 HTTP/存储格式摘录，移除对前一版文档目录的引用。HTTP 与持久化绑定仅保留当前要求和真实待决项。
