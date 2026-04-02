# OAR Workspace Spec

## 架构参照

OCI Image Spec 定义了如何打包、分发和解压容器镜像。
镜像是一组文件系统层的叠加；containerd 将它们解压为容器的 rootfs。

OAR Workspace Spec 服务于类似但更窄的目的：**如何准备 Agent 所需的工作环境**。
我们不需要分发（没有 registry，没有 push/pull）。
我们需要描述代码从哪里来、需要执行哪些准备步骤。

### 从 OCI Image Spec 借鉴了什么

| OCI 概念 | OAR 对应 | 理由 |
|----------|---------|------|
| Image → rootfs 流水线 | Workspace Spec → workdir 流水线 | 都在回答"如何准备文件系统" |
| Image manifest（描述层） | Workspace Spec（描述 source + hooks） | 都是对准备步骤的声明式描述 |
| containerd 管理镜像 | agentd 管理 workspace | 高层守护进程拥有准备生命周期 |

### 不需要 OCI Image Spec 的什么

| OCI 概念 | 为什么不需要 |
|----------|------------|
| Layer tarball、diff | Agent 使用普通目录工作，不需要分层文件系统 |
| Content-addressable store | 当前阶段没有去重需求 |
| Distribution Spec / Registry | Workspace 从 source（git、本地路径）构建，不从 registry 拉取 |
| Snapshotter（overlayfs、btrfs） | 没有联合文件系统。Agent 直接写入 workspace |

### 容器类比

```
容器世界：
  1. 拉取 image manifest         → 描述镜像由哪些 layer 组成
  2. 下载 layers（tarball）       → 实际的文件系统内容
  3. 通过 snapshotter 解压       → 叠加所有 layer，得到最终 rootfs
  4. 挂载为容器 root             → 容器看到统一的文件系统

Agent 世界：
  1. 读取 workspace spec         → 描述代码在哪、需要执行什么 hook
  2. 准备 source                 → git clone 或创建空目录
  3. 执行 setup hooks            → npm install、go mod download、启动服务
  4. 设为 agent cwd              → agent 在这个目录下工作
```

流水线在结构上是同构的；只是机制不同。
我们用 git 和 shell 命令代替了 tarball 和 snapshot。

## 规范定义

### 顶层结构

```json
{
  "oarVersion": "0.1.0",
  "metadata": { },
  "source": { },
  "hooks": { }
}
```

### `source`

**类型**: object（必填）

代码从哪里来。支持两种 source 类型。

#### Git Source

```json
{
  "source": {
    "type": "git",
    "url": "https://github.com/user/project.git",
    "ref": "feature/auth-refactor",
    "depth": 1
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | `"git"` |
| `url` | string | 是 | Git 仓库 URL |
| `ref` | string | 否 | 分支、tag 或 commit SHA。默认：默认分支 |
| `depth` | int | 否 | 浅克隆深度。0 或省略 = 完整克隆 |

agentd 将仓库克隆到其 workspace 存储的托管目录下。

#### EmptyDir Source

```json
{
  "source": {
    "type": "emptyDir"
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | `"emptyDir"` |

agentd 在 workspace 存储下创建一个空的托管目录。
适用于不需要已有代码的场景（如从零创建项目、纯对话型 agent）。

**是否支持空的 git 目录？** 考虑过在 emptyDir 中 `git init`，但决定不做：
- emptyDir 的语义是"空目录"，加 git init 就变成了 "gitEmptyDir"，职责不清
- 如果 agent 需要 git，它可以自己 `git init`
- 或者使用 git source + setup hook 来初始化特定的 git 状态
- 保持 source 类型的语义纯粹

#### Local Source

```json
{
  "source": {
    "type": "local",
    "path": "/home/user/project"
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | `"local"` |
| `path` | string | 是 | 宿主机上已存在的目录绝对路径 |

agentd 直接使用指定的宿主机目录作为 workspace，不做克隆或创建。
适用于 agent 在本地已有的代码仓库上工作的场景。

**注意**：与 git / emptyDir 不同，local workspace 的目录不由 agentd 托管。
agentd 不会在 cleanup 时删除该目录，只会执行 teardown hooks。

#### 未来 Source 类型（预留）

**git-worktree**（暂不定义）：

```
设计思考：
  agentd 可以参考 Go modules 的 VCS 管理模式 —
  go 命令自己管理 module cache ($GOPATH/pkg/mod/)，
  从中提取需要的版本。

  类比到 agentd：
  1. agentd 维护一个 git bare repo 缓存（类似 Go 的 module cache）
  2. 当需要创建 workspace 时，从 bare repo 创建 git worktree
  3. 多个 session/room 可以从同一个 repo 创建不同的 worktree
  4. 每个 worktree 是独立的工作目录，互不影响

  优势：
  - 共享 git 对象存储，节省磁盘空间和网络带宽
  - 创建 worktree 比 clone 快得多
  - 每个 agent 在独立 worktree 中工作，天然隔离
  - Room 内多个 agent 可以工作在不同 worktree 上

  这与 Claude Code 本身的 worktree 机制类似，
  但由 agentd 统一管理（而非 agent 自己创建）。
```

### `hooks`

**类型**: object（可选）

Workspace 生命周期钩子。分为 `setup` 和 `teardown` 两个阶段。

**设计说明**：OCI Runtime Spec 的 hooks 用于容器生命周期的基础设施准备
（网络、设备）。我们考虑过将 hooks 放在 Runtime Spec 中，但找不到 Agent 的
真实使用场景 — Agent 不需要基础设施准备。然而 workspace 有真实的生命周期需求：
安装依赖、构建项目、启动数据库、清理服务。所以 hooks 放在这里，这里才有真实的工作。

```json
{
  "hooks": {
    "setup": [
      {
        "command": "npm",
        "args": ["install"],
        "description": "安装 Node.js 依赖"
      },
      {
        "command": "npm",
        "args": ["run", "build"],
        "description": "构建项目"
      }
    ],
    "teardown": [
      {
        "command": "docker",
        "args": ["compose", "down"],
        "description": "停止数据库容器"
      }
    ]
  }
}
```

#### Hook 阶段

| 阶段 | 触发时机 | 使用场景 |
|------|---------|---------|
| `setup` | source 准备就绪后（克隆或创建完成） | `npm install`、`go mod download`、`docker compose up -d`、数据库迁移 |
| `teardown` | workspace 销毁之前 | `docker compose down`、临时文件清理、关闭后台服务 |

**命名说明**：早期设计使用 `postPrepare` / `postCleanup`，改为 `setup` / `teardown` 因为：
- 语义更直接 — "准备环境"和"拆除环境"，不需要理解"post"的隐含含义
- 和常见的 test framework 概念一致（JUnit、xUnit 的 setup/teardown）
- 与 OCI hooks 的 `prestart` / `poststop` 风格不同，但更符合 workspace 的语义

#### Hook 条目

每个 hook 条目：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `command` | string | 是 | 可执行文件 |
| `args` | []string | 否 | 命令行参数 |
| `description` | string | 否 | 人类可读的描述，用于日志输出 |

**`command` + `args` 的设计对齐**：与 Runtime Spec 中 `acpAgent.process.command` / `acpAgent.process.args`
以及 agentd runtimeClass 配置中的 `command` / `args` 保持一致。
不使用单一的 shell 字符串（如 `"npm install"`），因为：
- 避免 shell 注入风险
- 与 OCI hooks、K8s container command 的设计一致
- 可以精确控制参数传递（含空格、特殊字符的参数）

Hooks 按数组顺序依次执行。任何一个 hook 失败，整个 workspace 准备都失败。
所有 hook 命令的工作目录都是 workspace 目录。

**未来 hook 阶段**（预留，暂不定义）：

- `preSetup` — source 准备之前（磁盘空间检查、预飞验证）
- `postTeardown` — workspace 销毁之后（通知、清理外部资源）

## 完整示例

### Go 项目

```json
{
  "oarVersion": "0.1.0",
  "metadata": {
    "name": "backend-service",
    "annotations": {
      "org.openagents.workspace.language": "go"
    }
  },
  "source": {
    "type": "git",
    "url": "https://github.com/org/backend.git",
    "ref": "main"
  },
  "hooks": {
    "setup": [
      {
        "command": "go",
        "args": ["mod", "download"],
        "description": "下载 Go 模块"
      }
    ]
  }
}
```

### 带数据库的 Node.js 项目

```json
{
  "oarVersion": "0.1.0",
  "metadata": {
    "name": "web-app",
    "annotations": {
      "org.openagents.workspace.language": "typescript"
    }
  },
  "source": {
    "type": "git",
    "url": "https://github.com/org/web-app.git",
    "ref": "feature/auth"
  },
  "hooks": {
    "setup": [
      {
        "command": "npm",
        "args": ["install"],
        "description": "安装依赖"
      },
      {
        "command": "docker",
        "args": ["compose", "up", "-d", "postgres", "redis"],
        "description": "启动数据库服务"
      },
      {
        "command": "npm",
        "args": ["run", "db:migrate"],
        "description": "执行数据库迁移"
      }
    ],
    "teardown": [
      {
        "command": "docker",
        "args": ["compose", "down"],
        "description": "停止数据库服务"
      }
    ]
  }
}
```

### 空目录（从零开始的项目）

```json
{
  "oarVersion": "0.1.0",
  "metadata": {
    "name": "new-project"
  },
  "source": {
    "type": "emptyDir"
  }
}
```

### Git 项目（无 Hooks）

```json
{
  "oarVersion": "0.1.0",
  "metadata": {
    "name": "simple-project"
  },
  "source": {
    "type": "git",
    "url": "https://github.com/org/simple.git"
  }
}
```

## Workspace 生命周期

由 agentd 的 Workspace Manager 管理（见 [agentd.md](../agentd/agentd.md)）：

```
准备（Prepare）:
  1. 校验 spec
  2. 解析 source → workspace 目录
     - git: 克隆到托管存储
     - emptyDir: 在托管存储下创建空目录
  3. 执行 setup hooks（按顺序，在 workspace 目录下）
  4. 返回 workspace 路径

清理（Cleanup）:
  1. 执行 teardown hooks（按顺序，在 workspace 目录下）
  2. 删除托管的 workspace 目录
```

## Workspace 存储

agentd 在可配置的根目录下管理 workspace 目录：

```
/var/lib/agentd/workspaces/
├── ws-abc123/          ← 项目 A 的 git clone
├── ws-def456/          ← 项目 B 的 git clone
├── ws-ghi789/          ← 空目录（emptyDir）
└── ...
```
