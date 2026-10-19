# Ticket 01 验收记录

2026-10-20，v2 分支，基线 b263dfc1e。测试后没有改动探针源码或场景；`source-sha256.txt` 标识本次输入。环境见 `environment.txt`（Go 1.26.9，macOS arm64）。所有命令均在 mass-go 执行。

| 命令 / 范围 | 结果 | 证据 |
| --- | --- | --- |
| `GOWORK=off GOPROXY=off go test -count=1 -v ./examples/sdkprobe` | 通过，13 个子场景 | `test.txt` |
| `GOWORK=off GOPROXY=off go test -race -count=1 -v ./examples/sdkprobe` | 通过，无 race 报告，13 个子场景 | `test-race.txt` |
| `GOWORK=off GOPROXY=off go vet ./examples/sdkprobe` | 通过，exit 0，无诊断 | `vet.txt`（空输出） |
| `git diff --exit-code 0cfe97729 -- harness`（仓库根） | 通过，exit 0，无差异 | 提交前再核对 |

基本协议、权限、完整消息/工具事实、正常及失败清理均通过。Codex 用户问答与 Claude AskUserQuestion 的独立 synthetic 输入更新均通过。Claude 缺少无输入 init 时 ID 不可用是预期反例（100ms 有界观察），不是成功创建；随后独立 hello 路径通过。两个最终测试日志末尾均确认无本次 peer 子进程及临时二进制目录残留。测试过程中的每个临时工作目录也删除并 stat 检查。

审查旧草稿时先发现编译错误、非 NDJSON 的直接输入，以及 Codex thread/start synthetic response 缺少 SDK 必填字段；均已修正并在当前固定 SDK 原校验下重新通过。最终没有遗留失败项。

未验证：真实 Agent/账户/模型、真实 CLI 的无输入身份与问答语义、Linux、产品架构/API/持久化、仓库其他模块及后续工单。这些不属于本单；本记录不宣称真实 Agent 验收通过。未调用真实工具，也没有真实命令执行结果。
