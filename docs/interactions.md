# 当前交互合同

当前 MASS 在适配内部自动批准一次性的受支持命令执行请求，不创建长期授权或修改全局配置。工具参数已完整不代表执行已经开始。

不支持的问题、文件修改审批和动态工具请求明确拒绝。公共接口不提供结构化交互回答、后台生命周期或配置修改能力。普通助手问句仍是文本，不自动变为交互请求；未知状态和目录保持未知，已知的空配置目录明确返回空数组。

审批仅作用于原运行资源、Attachment 和目标工作。取消或归档使尚未交付上游的旧决策失效，已解决请求不能被迟到策略再次批准。已经交付上游的许可不能追溯撤回。

取消确认、工作终止与后台清理是不同事实。未知结果保持准入限制；不能因控制请求结束就虚构空闲或成功。具体原生映射见[Codex 审批与取消](design/native-runtime-connectors.md#approval-and-cancellation)，当前外部响应见[取消协议](http-api.md#native-cancellation-ticket-04)，处理器使用同一[绑定命令合同](handlers.md)。

[Runtime 提案](design/runtime-core-architecture.md)中的未来交互能力仍需单独定稿，本文不因此宣告支持。既有原生实现说明保留在[接入记录](develop/native-runtime-record.md)。
