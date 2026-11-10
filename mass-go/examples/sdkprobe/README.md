# SDK fake 协议探针（Ticket 01）

在仓库根目录执行：

```sh
rtk proxy sh -c 'cd mass-go && GOWORK=off GOPROXY=off go test -count=1 -v ./examples/sdkprobe'
```

需要 Go 1.26.10、`go` 与 `ps` 在 PATH，且 mass-go 的依赖已缓存。缺缓存依赖时报告未执行；离线验收不自动下载或安装。探针校验 SDK 固定版本 `github.com/zoumo/agent-sdk-go@v0.0.0-20261109202203-1c8514ff8a18` 和工具固定版本 `github.com/zoumo/agent-sdk-testify@v0.1.1-0.20261109183517-b7c1fd72fc17`，并拒绝任一 replace。探针从消费模块构建 MASS 自有的 `internal/testpeer`，经临时 launcher 启动这些严格合成场景；不构建已从最新 SDK 删除的 `tools/agent-sdk-testify protocol mock`。场景先压缩为单行原生 JSON，保持 JSONL 协议帧。两个 provider 均使用 Open/Submit 和唯一 Receive 消费者；没有修改 SDK 或调用真实 Agent。

子进程 Env 仅含显式 `CODEX_MOCK_SCENARIO` / `CLAUDE_MOCK_SCENARIO`，工作目录为临时目录。不会启动真实 Agent、读取账户或调用模型。所有场景标注 `provenance: synthetic`；工具输出只是场景数据，工具命令不会执行。

## 场景与观察

| 测试 | 顺序与验证点 |
| --- | --- |
| `TestCodex/basic` | initialize → initialized → thread/start 原生 thread ID 与 sessionId（无 turn/input）→ 独立 hello turn → command/file 两个 approval 原 handle accept → 完整 tool/message → turn/completed → interrupt 往返屏障 → Close |
| `TestCodex/question` | 独立启动，收到 item/tool/requestUserInput；核对 request/thread/turn/item ID，通过原 handle 回答 format=json，不发送 approval decision；随后完成消息/工具与清理 |
| `TestClaude/basic` | control initialize → synthetic system/init 原生 ID（无 user 消息）→ 独立 hello → can_use_tool/Bash 原 PermissionRequest allow，保留原 input → 完整 tool_use/tool_result/text/result → interrupt 屏障 → Close |
| `TestClaude/question` | 独立启动，明确识别 AskUserQuestion，通过原 IncomingRequest 按完整问题文本回答并保留原 questions/options；没有把问答直接送入全同意策略 |
| `TestClaude/no-identity` | 脚本在 hello 前不发 init；Receive 等待 100ms 到期且 SessionID 未知，明确观察缺 ID。随后才发送非空 hello，再观察脚本 init 与正常工具/消息。需要创建时 ID 的调用方必须报错，不能用空 prompt 或自造 ID 补齐 |
| `TestPeerExit/{codex,claude}/{complete,mismatch,incomplete}` | 独立启动同一个 MASS scenario peer 二进制，按场景送完整 NDJSON 再 EOF：exit 0；错误请求与提前 EOF：exit 1 且有 stderr。每次 Run/Wait 后核对 ProcessState 已退出并回收 |
| `TestSDKInitializationFailure/{codex,claude}` | 将临时脚本首个预期请求改为不匹配，实际 SDK 初始化返回错误；用于验证失败资源清理 |

原 handle 的 ID 与原生关联字段均检查；strict peer 校验回答关联与完整 JSON，错误回答无法到达下一协议步骤。interrupt 只是已回答请求的往返屏障，不代表真正中断了模型。SDK Close 校验错误与 Done，Codex 还检查 stderr 字节数与 unmatched responses 为零。直接子进程测试验证精确退出码，避免把 SDK 的 Close 等价于 peer exit 0。

所有成功和失败断言都注册清理；每个工作目录删除后 stat 读回不存在，最后 ps 读回无本次唯一二进制路径的进程，再删除并 stat 读回整个临时树。SDK Close 负责关闭管道并等待子进程；探针没有另行读取已关闭的 FD。go build 有一分钟上限，各协议场景有五秒上限。

## 证据与限制

最终命令、环境、源码/场景 SHA256 与逐步骤输出在 [evidence](evidence/)；详见 [验收记录](evidence/README.md)。问答两侧均通过的是 SDK + authored synthetic 场景的协议往返，不代表真实模型能提问、理解答案或真实 CLI 的 AskUserQuestion schema 已验收。特别是 Claude 无输入 ID 成功仅因为场景主动发 init；缺 init 的反例也已验证。两个 Agent 的真实无输入身份能力均未验证。

本目录只包含可执行测试样例与可复用 JSON 场景。不涉及产品 Service/Store/Runtime 替换、持久化、HTTP/SSE 或后续工单。运行日志不证明上述产品能力。

## Evidence versions

The files already under `evidence/` bind their original SDK/source versions
(including the older 6dbf3e9 SDK). They are historical results, not a pass for the
current pin. Current adaptation runs are recorded separately in central
`.scratch/sdk-session-adaptation/validation/ticket-08.md`. Do not rewrite the old
logs or treat this SDK-only probe as product, official recording or live evidence.
