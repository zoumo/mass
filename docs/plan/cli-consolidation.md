# CLI 重构计划：agentd + agentdctl 双二进制

## 背景

当前项目有 5 个独立二进制：

| 二进制 | 职责 |
|---|---|
| `agentd` | 启动 daemon，监听 ARI JSON-RPC socket |
| `agentdctl` | CLI 客户端，连接 daemon 执行操作 |
| `agent-shim` | 进程 shim，运行 ACP adapter |
| `agent-shim-cli` | shim 的调试 CLI |
| `workspace-mcp-server` | MCP server，暴露 workspace 工具给 agent |

目标：整合为两个二进制——`agentd`（系统进程，containerd 风格）和 `agentdctl`（用户操作工具，ctr 风格）。

---

## 目标命令结构

### agentd — 系统进程入口

只负责启动和运行各类后台进程，不做资源操作：

```
agentd
  server [--root /var/run/agentd]   # 启动 daemon（原 agentd 直接运行）
  shim                               # 运行 shim 进程（原 agent-shim，由 ProcessManager fork 调用）
    --bundle <path>
    --socket <path>
  workspace-mcp                      # 运行 workspace MCP server（原 workspace-mcp-server）
```

### agentdctl — 资源操作工具（ctr 风格，resource-first）

```
agentdctl
  agent
    list          [-w <workspace>]
    get           <name> -w <workspace>
    create        -w <workspace> --runtime <runtime> --name <name>
    delete        <name> -w <workspace>
    prompt        <name> -w <workspace> --text "..." [--wait]
    cancel-prompt <name> -w <workspace>
    stop          <name> -w <workspace>
    restart       <name> -w <workspace>

  runtime
    list
    get    <name>
    apply  -f <file>
    delete <name>

  workspace
    list
    get    <name>
    create
      local  <name> --path <path>
      git    <name> --url <url> [--ref <ref>] [--depth <n>]
      empty  <name>
      -f     <file>              # 完整 spec，支持 hooks 等高级配置
    delete <name>
    send   <name> --from <agent> --to <agent> --text "..."

  shim   <subcommand>   # shim 调试 CLI（原 agent-shim-cli）
```

workspace 使用 `-w` / `--workspace` flag。

### 命令示例

```bash
# 启动 daemon
agentd server --root /var/run/agentd

# runtime 管理
agentdctl runtime list
agentdctl runtime apply -f codex.yaml
agentdctl runtime get codex
agentdctl runtime delete codex

# agent 管理
agentdctl agent list -w oar-project
agentdctl agent create -w oar-project --runtime codex --name reviewer
agentdctl agent prompt reviewer -w oar-project --text "review this PR" --wait
agentdctl agent cancel-prompt reviewer -w oar-project
agentdctl agent stop reviewer -w oar-project
agentdctl agent restart reviewer -w oar-project
agentdctl agent delete reviewer -w oar-project

# workspace 管理
agentdctl workspace list
agentdctl workspace create local oar-project --path /home/jim/code/oar
agentdctl workspace create git oar-project --url https://github.com/org/repo --ref main
agentdctl workspace create empty oar-project
agentdctl workspace create -f workspace.yaml      # 需要 hooks 或其他高级配置时用
agentdctl workspace send oar-project --from codex --to claude-code --text "..."

# socket 默认值
--socket /var/run/agentd/agentd.sock
```

---

## 代码组织

### 目标目录结构

```
cmd/
  agentd/
    main.go                           # 仅调用 subcommands.NewRootCommand().Execute()
    subcommands/
      root.go                         # 注册 server / shim / workspace-mcp
      server/
        command.go                    # "agentd server" — 启动 daemon
      shim/
        command.go                    # "agentd shim" — 启动 shim 进程
      workspacemcp/
        command.go                    # "agentd workspace-mcp" — 启动 MCP server

  agentdctl/
    main.go                           # 仅调用 subcommands.NewRootCommand().Execute()
    subcommands/
      root.go                         # 注册所有 resource 子命令，绑定全局 --socket flag
      agent/
        command.go                    # "agentdctl agent" — subcommand group
        list.go
        get.go
        create.go
        delete.go
        prompt.go                     # "agentdctl agent prompt"
        cancel_prompt.go              # "agentdctl agent cancel-prompt"
        stop.go
        restart.go
      runtime/
        command.go                    # "agentdctl runtime" — subcommand group
        list.go
        get.go
        apply.go
        delete.go
      workspace/
        command.go                    # "agentdctl workspace" — subcommand group
        list.go
        get.go
        create/
          command.go                  # "agentdctl workspace create" — subcommand group
          local.go                    # "agentdctl workspace create local"
          git.go                      # "agentdctl workspace create git"
          empty.go                    # "agentdctl workspace create empty"
          file.go                     # "agentdctl workspace create -f"
        delete.go
        send.go
      shim/
        command.go                    # "agentdctl shim" — shim 调试（原 agent-shim-cli）

# 原有目录废弃：
# cmd/agentdctl/            → 重建，改为 ctr 风格 resource-first 结构
# cmd/agent-shim/           → 删除，逻辑移入 cmd/agentd/subcommands/shim/
# cmd/agent-shim-cli/       → 删除，逻辑移入 cmd/agentdctl/subcommands/shim/
# cmd/workspace-mcp-server/ → 删除，逻辑移入 cmd/agentd/subcommands/workspacemcp/
```

### main.go 示例（极简）

```go
// cmd/agentd/main.go
package main

import (
    "os"
    "github.com/zoumo/open-agent-runtime/cmd/agentd/subcommands"
)

func main() {
    if err := subcommands.NewRootCommand().Execute(); err != nil {
        os.Exit(1)
    }
}
```

### agentd root.go 示例

```go
// cmd/agentd/subcommands/root.go
package subcommands

import (
    "github.com/spf13/cobra"
    "github.com/zoumo/open-agent-runtime/cmd/agentd/subcommands/server"
    "github.com/zoumo/open-agent-runtime/cmd/agentd/subcommands/shim"
    "github.com/zoumo/open-agent-runtime/cmd/agentd/subcommands/workspacemcp"
)

func NewRootCommand() *cobra.Command {
    root := &cobra.Command{
        Use:   "agentd",
        Short: "Open Agent Runtime daemon",
    }
    root.AddCommand(
        server.NewCommand(),
        shim.NewCommand(),
        workspacemcp.NewCommand(),
    )
    return root
}
```

### agentdctl root.go 示例

```go
// cmd/agentdctl/subcommands/root.go
package subcommands

import (
    "github.com/spf13/cobra"
    "github.com/zoumo/open-agent-runtime/cmd/agentdctl/subcommands/agent"
    "github.com/zoumo/open-agent-runtime/cmd/agentdctl/subcommands/runtime"
    "github.com/zoumo/open-agent-runtime/cmd/agentdctl/subcommands/shim"
    "github.com/zoumo/open-agent-runtime/cmd/agentdctl/subcommands/workspace"
)

func NewRootCommand() *cobra.Command {
    root := &cobra.Command{
        Use:   "agentdctl",
        Short: "CLI client for Open Agent Runtime",
    }
    root.PersistentFlags().String("socket", "/var/run/agentd/agentd.sock", "agentd ARI socket path")
    root.AddCommand(
        agent.NewCommand(),
        runtime.NewCommand(),
        workspace.NewCommand(),
        shim.NewCommand(),
    )
    return root
}
```

---

## agentd server：去除 config.yaml，改为 --root

```bash
agentd server [--root /var/run/agentd]
```

**`--root`（默认 `/var/run/agentd`）**

root 目录下自动派生所有路径：

```
/var/run/agentd/
  agentd.sock     # ARI Unix socket
  workspaces/     # workspace 根目录
  bundles/        # agent bundle 根目录
  agentd.db       # SQLite 元数据库（同时存储 runtime 实体）
```

> runtime 不再是文件，直接持久化在 `agentd.db` 中，通过 `agentdctl runtime apply/get/delete` 管理。

> 如果以非 root 用户运行，`/var/run/agentd/` 可能不存在或无写权限。启动时若目录创建失败，报明确错误并提示用 `--root ~/.agentd`。

### server Options 结构

```go
// cmd/agentd/subcommands/server/command.go
type Options struct {
    Root string  // --root，默认 /var/run/agentd
}

func (o *Options) socketPath() string    { return filepath.Join(o.Root, "agentd.sock") }
func (o *Options) workspaceRoot() string { return filepath.Join(o.Root, "workspaces") }
func (o *Options) bundleRoot() string    { return filepath.Join(o.Root, "bundles") }
func (o *Options) metaDBPath() string    { return filepath.Join(o.Root, "agentd.db") }
```

---

## agentd shim：原 agent-shim

`agent-shim` 的职责不变，只是入口变为 `agentd shim`。

`agentd` 内部（ProcessManager）fork 子进程时，改为 fork 自身并传入 `shim` 子命令：

```go
// pkg/agentd/process.go
// 旧：exec.Command(shimBinary, "--bundle", bundlePath, "--socket", socketPath)
// 新：exec.Command(os.Executable(), "shim", "--bundle", bundlePath, "--socket", socketPath)
```

---

## agentd workspace-mcp：原 workspace-mcp-server

环境变量接口不变，入口变为子命令：

```bash
# 旧
workspace-mcp-server   # via env: OAR_AGENTD_SOCKET, OAR_WORKSPACE_NAME, OAR_AGENT_NAME

# 新
agentd workspace-mcp   # 同样 via env
```

---

## 实施阶段

### Phase 1：代码骨架重组

1. 创建 `cmd/agentd/subcommands/` 目录结构（server / shim / workspacemcp）
2. `cmd/agentd/main.go` 改为极简入口
3. 将现有 `cmd/agentd/main.go` 核心逻辑移入 `cmd/agentd/subcommands/server/command.go`
4. 将 `cmd/agent-shim/main.go` 移入 `cmd/agentd/subcommands/shim/command.go`
5. 将 `cmd/workspace-mcp-server/main.go` 移入 `cmd/agentd/subcommands/workspacemcp/command.go`
6. 重建 `cmd/agentdctl/`：创建 resource-first 结构（agent / runtime / workspace / shim）
7. 将原有 `cmd/agentdctl/` 各命令逻辑迁移到新的 resource 子命令中
8. 将 `cmd/agent-shim-cli/` 逻辑移入 `cmd/agentdctl/subcommands/shim/`
9. 更新 `Makefile`：只构建 `agentd` 和 `agentdctl`

**验收：** `agentd server`、`agentd shim`、`agentd workspace-mcp`、`agentdctl agent list` 均可运行，功能与原来一致。

### Phase 2：server 配置重构 + runtime 实体化

1. `cmd/agentd/subcommands/server/command.go` 新增 `--root` flag，从 root 派生所有路径，移除 `--runtime-classes` flag
2. 在 `pkg/meta/` 新增 `runtime.go`：`Runtime` 数据模型 + Store CRUD（参考 agent.go / workspace.go 规范）
3. daemon 启动时从 DB 加载已有 runtime，初始化 `RuntimeClassRegistry`
4. 新增 ARI 方法：`runtime/set`、`runtime/list`、`runtime/get`、`runtime/delete`
5. 实现 `agentdctl runtime apply/get/list/delete` 子命令
7. 更新 `bin/e2e/` 下的脚本，去掉 config.yaml 依赖
8. `ProcessManager` 改为 fork `agentd shim`，移除 `OAR_SHIM_BINARY` 依赖

**验收：**
- `agentd server --root /tmp/test-agentd` 可无 config.yaml 启动
- `agentdctl runtime apply -f codex.yaml` 注册后可用于 `agent create`
- `agentdctl runtime list` 列出已注册 runtime

### Phase 3：CLI 对齐

1. 所有 agent 操作改为 ctr 风格：`agentdctl agent <verb> <name> -w <workspace>`
2. `--socket` 默认值改为 `/var/run/agentd/agentd.sock`
3. 更新 `docs/manual/room-validation-runbook.md`
4. 更新 `bin/e2e/setup-room.sh`、`teardown-room.sh`

**验收：** runbook 中所有命令可正常执行。

### Phase 4：清理

1. 删除 `cmd/agent-shim/`、`cmd/agent-shim-cli/`、`cmd/workspace-mcp-server/`
2. 更新 `Makefile`，产物只有 `bin/agentd` 和 `bin/agentdctl`
3. 更新 `README`、所有文档中对旧二进制的引用

---

## Makefile 变更

```makefile
# 旧
BINARIES := agentd agentdctl agent-shim agent-shim-cli workspace-mcp-server

# 新
BINARIES := agentd agentdctl
```

---

## 风险点详解

### 风险 1：shim 自 fork 路径（`os.Executable()`）

**问题根源**

`ProcessManager` 调用 `os.Executable()` 获取自身路径，然后：

```go
exec.Command(self, "shim", "--bundle", bundlePath, "--socket", socketPath)
```

`os.Executable()` 在不同场景下行为不一致：

| 场景 | `os.Executable()` 返回 | 问题 |
|---|---|---|
| `./bin/agentd server` | `/Users/jim/code/.../bin/agentd` | 正常 |
| PATH 中的 `agentd server` | `/usr/local/bin/agentd`（symlink） | macOS 不自动解析 symlink，exec 仍然有效，但路径可能超出预期 |
| `go run ./cmd/agentd` | `/tmp/go-buildXXXXX/exe/agentd`（临时文件） | 临时文件在 fork 后可能已被 go 工具链删除，shim 无法启动 |
| 二进制被热替换后 | 仍指向旧路径（已被替换） | exec 调用新文件，版本不同 |

**`go run` 场景的实际影响**

开发时 `go run ./cmd/agentd server` 然后触发 agent create，ProcessManager 会尝试 exec 一个已经被 go 工具链删除的临时二进制。表现为 agent 立即进入 `error` 状态，日志显示 `exec: no such file or directory`。

**缓解方案**

1. **首选**：`agentd server` 启动时将 `os.Executable()` 解析为绝对路径存入 `Options`，并打印 `shim binary: /absolute/path`，方便排查。
2. **`go run` 检测**：启动时检测路径是否含 `/tmp/go-build`，若是则打 warn 并要求用 `make build` 先构建二进制再运行。

---

### 风险 2：socket 路径长度溢出

**OS 限制**

| 平台 | 限制 |
|---|---|
| macOS | 104 字节（`UNIX_PATH_MAX`） |
| Linux | 108 字节（`sockaddr_un.sun_path`） |

当前 `pkg/spec/state.go` 硬编码了 104（取下界）。

**代码改动：区分平台**

```go
// pkg/spec/maxsockpath_linux.go
package spec
const maxUnixSocketPath = 108

// pkg/spec/maxsockpath_darwin.go
package spec
const maxUnixSocketPath = 104
```

**默认路径 `/var/run/agentd` 的 budget**

```
/var/run/agentd/bundles/<ws>/<name>/agent-shim.sock
  固定：/var/run/agentd/bundles/ = 24 字节
  后缀：/agent-shim.sock         = 16 字节
  ──────────────────────────────
  ws + / + name 可用：
    macOS：104 - 24 - 16 = 64 字节
    Linux：108 - 24 - 16 = 68 字节
```

64 字节对于较长名称仍然紧张（中文名每字 3 字节）。

**缓解方案**

1. **区分平台限制**（必做）：用 build tag 文件替换硬编码 104。
2. **哈希 bundle ID**（建议做）：改为固定长度 hash 前缀：
   ```
   /var/run/agentd/bundles/<sha1(workspace+":"+name)[:12]>/agent-shim.sock
   # → 52 bytes，永远不超限
   ```
   目录里放 `meta.json` 记录原始 workspace/name，recovery 扫描时还原身份。
3. **`create agent` 时前置校验**（必做）：写入 DB 之前计算路径并校验，返回用户友好错误。

---

### 风险 3：`agentdctl runtime apply` 数据模型

**数据模型（`pkg/meta/models.go`）**

runtime 只有 spec，无验证状态。`StartupTimeout` 控制 agent 启动时等待 shim 就绪的超时时间，不同 runtime 冷启动时间差异较大（如 bunx 首次运行需要下载依赖），通过此字段按需调整。

```go
// RuntimeSpec describes the desired configuration of a runtime.
type RuntimeSpec struct {
    // Command is the executable to run (required).
    Command string `json:"command"`

    // Args are the command-line arguments passed to Command.
    Args []string `json:"args,omitempty"`

    // Env holds extra environment variables to set in the shim process.
    // Env > EnvFile > agentd inherited env（同名变量按此优先级覆盖）。
    Env []EnvVar `json:"env,omitempty"`

    // EnvFile is a path to a local file containing additional environment
    // variables (key=value format, one per line). Loaded at shim startup;
    // only the path is stored in the DB, not the file contents.
    // Priority: lower than Env, higher than agentd inherited env.
    EnvFile string `json:"envFile,omitempty"`

    // StartupTimeoutSeconds is the max seconds to wait for the shim to become ready.
    // Defaults to 60 if unset.
    StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`
}

// EnvVar represents a single environment variable.
type EnvVar struct {
    Name  string `json:"name"`
    Value string `json:"value"`
}

// Runtime represents a runtime record in pkg/meta.
type Runtime struct {
    Metadata ObjectMeta  `json:"metadata"`
    Spec     RuntimeSpec `json:"spec"`
}
```

**Store 方法（`pkg/meta/runtime.go`）**

```go
func (s *Store) SetRuntime(ctx context.Context, rt *Runtime) error
func (s *Store) GetRuntime(ctx context.Context, name string) (*Runtime, error)
func (s *Store) ListRuntimes(ctx context.Context) ([]*Runtime, error)
func (s *Store) DeleteRuntime(ctx context.Context, name string) error
```

**`agentdctl runtime apply` 命令接口**

```bash
agentdctl runtime apply -f codex.yaml
agentdctl runtime apply -f - <<EOF
name: codex
command: bunx
args:
  - "@zed-industries/codex-acp"
envFile: /etc/agentd/codex.env
startupTimeoutSeconds: 120
EOF
```
