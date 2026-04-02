# OCI Runtime Specification - 3W2H 深度调研

> 调研日期: 2026-04-01
> 规范版本: v1.3.0 (2025-11-04 发布)
> GitHub: https://github.com/opencontainers/runtime-spec
> 许可证: Apache License 2.0

---

## 目录

- [What - 是什么](#what---是什么)
- [Why - 为什么](#why---为什么)
- [Who - 谁在做](#who---谁在做)
- [How - 怎么做](#how---怎么做)
- [How Well - 做得怎么样](#how-well---做得怎么样)
- [参考链接](#参考链接)

---

## What - 是什么

### 一句话定义

OCI Runtime Specification 是一个开放标准，定义了容器的**配置格式**、**执行环境**和**生命周期管理**，确保任何符合规范的运行时都能以一致的方式创建和运行容器。

### 核心组成

规范由以下几个关键文档构成：

```
runtime-spec/
├── spec.md              # 规范入口，定义支持的平台和总览
├── bundle.md            # 文件系统 Bundle 格式
├── runtime.md           # 运行时行为和容器生命周期
├── config.md            # 容器配置（config.json）通用部分
├── config-linux.md      # Linux 特定配置（namespaces, cgroups, seccomp 等）
├── config-windows.md    # Windows 特定配置
├── config-freebsd.md    # FreeBSD 特定配置（v1.3.0 新增）
├── config-solaris.md    # Solaris 特定配置
├── config-vm.md         # 虚拟机特定配置
├── config-zos.md        # z/OS 特定配置
├── runtime-linux.md     # Linux 特定运行时行为
├── features.md          # 运行时特性声明结构
└── glossary.md          # 术语表
```

### 三大支柱

| 支柱 | 描述 |
|------|------|
| **Filesystem Bundle** | 容器的文件系统包，包含 `config.json` + rootfs 目录 |
| **Configuration (config.json)** | JSON 格式的容器配置，包括进程、挂载、hooks、平台特定设置 |
| **Runtime & Lifecycle** | 定义容器的 `creating → created → running → stopped` 生命周期 |

### 容器生命周期

```
                create
                  │
                  ▼
    ┌─────────────────────────┐
    │       creating          │
    └───────────┬─────────────┘
                │  prestart hooks
                │  createRuntime hooks
                │  createContainer hooks
                ▼
    ┌─────────────────────────┐
    │        created          │ ← container 已建但进程未启动
    └───────────┬─────────────┘
                │  start
                │  startContainer hooks
                ▼
    ┌─────────────────────────┐
    │        running          │ ← 用户进程运行中
    └───────────┬─────────────┘
                │  进程退出 / kill
                │  poststop hooks
                ▼
    ┌─────────────────────────┐
    │        stopped          │ ← delete 清理资源
    └─────────────────────────┘
```

### 支持平台

v1.3.0 支持六个平台：**Linux**、**Windows**、**FreeBSD**（新增）、**Solaris**、**VM**（虚拟机）、**z/OS**。

### config.json 核心字段

- **process**: 启动命令、参数、环境变量、工作目录、capabilities、rlimits
- **root**: 容器 rootfs 路径，是否只读
- **mounts**: 挂载点列表
- **hooks**: 生命周期钩子（prestart、poststart、poststop 等）
- **linux**: Linux 特定配置 — namespaces、cgroups、seccomp、apparmor、selinux
- **annotations**: 任意键值对元数据

---

## Why - 为什么

### 要解决的问题

在 OCI 出现之前，容器运行时没有统一标准：

```
Docker 时代的问题:

  Docker ──── 自有格式 ──── Docker Runtime
  rkt    ──── 自有格式 ──── rkt Runtime
  LXC    ──── 自有格式 ──── LXC Runtime

  → 每家格式不兼容，用户被锁定在特定工具链上
```

### 解决方案

```
OCI 时代:

  任何构建工具 ─┐                  ┌─ runc
  Buildah      ─┤                  ├─ crun
  Docker       ─┼── config.json ──┼─ youki
  Podman       ─┤   (统一规范)     ├─ gVisor
  其他工具     ─┘                  └─ kata-containers
```

### 核心价值

1. **可移植性**: 同一个 bundle 可以在任何兼容运行时上执行
2. **去供应商锁定**: 用户不被绑定到 Docker 或任何特定实现
3. **可组合性**: 不同层级的工具可以自由组合（构建器 + 运行时 + 编排器）
4. **安全可审计**: 开放的标准意味着安全属性可以被独立验证
5. **创新空间**: 新运行时只需符合规范即可融入生态（如 Rust 实现的 youki、C 实现的 crun）

---

## Who - 谁在做

### 维护者

| 姓名 | 所属组织 | GitHub |
|------|----------|--------|
| Michael Crosby | Docker | @crosbymichael |
| Mrunal Patel | Red Hat | @mrunalp |
| Aleksa Sarai | SUSE | @cyphar |
| Akihiro Suda | NTT | @AkihiroSuda |
| Kir Kolyshkin | - | @kolyshkin |
| Giuseppe Scrivano | Red Hat | @giuseppe |
| Sebastiaan van Stijn | - | @thaJeztah |
| Tianon Gravi | - | @tianon |
| Toru Komatsu | - | @utam0k |

### 治理结构

- **OCI (Open Container Initiative)**: Linux 基金会下属项目，2015 年由 Docker 和 CoreOS 发起
- **TOB (Technical Oversight Board)**: 技术决策最高机构
- 所有规范版本发布需要社区投票通过

### 规范实现者（运行时）

| 类型 | 项目 | 语言 | 说明 |
|------|------|------|------|
| 容器运行时 | **runc** | Go | OCI 参考实现，Docker/containerd 默认 |
| 容器运行时 | **crun** | C | Red Hat 维护，Podman 可选 |
| 容器运行时 | **youki** | Rust | 社区驱动 |
| 容器运行时 | **systemd-nspawn** | C | systemd v242+ 支持 `--oci-bundle` |
| 沙箱运行时 | **gVisor (runsc)** | Go | Google 用户态内核 |
| VM 运行时 | **Kata Containers** | Go | 基于虚拟机的隔离 |
| 机密计算 | **inclavare-containers** | Go | 阿里巴巴 SGX enclave 运行时 |

---

## How - 怎么做

### 规范的技术架构总览

```
┌──────────────────────────────────────────────────────────┐
│                    OCI Runtime Spec                       │
├──────────────┬──────────────────┬─────────────────────────┤
│   Bundle     │   Configuration  │   Runtime & Lifecycle   │
│  (bundle.md) │   (config.md)    │   (runtime.md)          │
│              │                  │                         │
│  rootfs/     │  config.json:    │  Operations:            │
│  config.json │  - process       │  - create               │
│              │  - root          │  - start                │
│              │  - mounts        │  - state                │
│              │  - hooks         │  - kill                 │
│              │  - annotations   │  - delete               │
│              │                  │                         │
│              │  Platform:       │  Lifecycle States:      │
│              │  - linux (ns,    │  creating → created     │
│              │    cgroups,      │  → running → stopped    │
│              │    seccomp)      │                         │
│              │  - windows       │  Hooks:                 │
│              │  - freebsd       │  - prestart             │
│              │  - vm            │  - createRuntime        │
│              │  - zos           │  - createContainer      │
│              │                  │  - startContainer       │
│              │                  │  - poststart            │
│              │                  │  - poststop             │
└──────────────┴──────────────────┴─────────────────────────┘
```

### 1. Filesystem Bundle — 容器的打包格式

Bundle 是容器在本地文件系统上的表示格式，包含运行容器所需的全部信息：

```
my-container-bundle/        # Bundle 根目录
├── config.json             # REQUIRED: 容器配置
└── rootfs/                 # root.path 引用的目录
    ├── bin/
    ├── etc/
    ├── lib/
    ├── usr/
    └── ...                 # 完整的容器根文件系统
```

关键约束：
- `config.json` **必须**位于 bundle 根目录且命名为 `config.json`
- rootfs 目录由 `config.json` 中的 `root.path` 字段引用
- Bundle 打成 tar 包时，这些文件必须在 tar 的根层级，不能嵌套在子目录中
- Bundle 的设计只关心本地文件系统表示，不涉及网络传输

### 2. 容器状态模型

运行时必须能返回一个标准化的容器状态 JSON：

```json
{
    "ociVersion": "1.3.0",
    "id": "oci-container1",
    "status": "running",
    "pid": 4422,
    "bundle": "/containers/redis",
    "annotations": {
        "myKey": "myValue"
    }
}
```

| 字段 | 类型 | 要求 | 说明 |
|------|------|------|------|
| `ociVersion` | string | REQUIRED | 兼容的 OCI 规范版本 |
| `id` | string | REQUIRED | 容器唯一 ID（同一主机唯一，跨主机不要求） |
| `status` | string | REQUIRED | `creating` / `created` / `running` / `stopped` |
| `pid` | int | 条件 REQUIRED | Linux 上 `created`/`running` 时必须提供 |
| `bundle` | string | REQUIRED | bundle 目录的绝对路径 |
| `annotations` | map | OPTIONAL | 关联的键值对元数据 |

### 3. 生命周期的 13 步详解

规范定义了精确的 13 步生命周期，每一步的行为都有 MUST/SHOULD 级别的约束：

```
步骤  触发者        操作                             失败处理
────  ──────        ────                             ────────
 1    调用者        create 命令 + bundle 路径 + ID
 2    运行时        按 config.json 创建运行环境        MUST 报错
                    （但 MUST NOT 启动用户进程）
 3    运行时        执行 prestart hooks               失败 → 停止 → 跳到 12
 4    运行时        执行 createRuntime hooks           失败 → 停止 → 跳到 12
 5    运行时        执行 createContainer hooks         失败 → 停止 → 跳到 12
                                                     ────── created 状态 ──────
 6    调用者        start 命令
 7    运行时        执行 startContainer hooks          失败 → 停止 → 跳到 12
 8    运行时        启动用户指定程序 (process)
 9    运行时        执行 poststart hooks               失败 → 停止 → 跳到 12
                                                     ────── running 状态 ──────
10    容器进程      进程退出 (正常/异常/kill)
                                                     ────── stopped 状态 ──────
11    调用者        delete 命令
12    运行时        销毁容器，撤销 step 2 的操作
13    运行时        执行 poststop hooks                失败仅 log warning，不影响
```

**关键设计决策**：
- **create/start 分离**：允许上层系统在容器环境就绪后、进程启动前进行额外配置（如网络设置）
- **Hooks 失败行为不同**：prestart/createRuntime/createContainer/startContainer/poststart 失败会中止容器；poststop 失败仅产生 warning
- **config.json 快照**：step 2 之后对 config.json 的修改 MUST NOT 影响已创建的容器
- **错误处理原则**：产生错误时，环境状态必须回到操作之前的状态

### 4. 五个标准操作

规范定义了运行时必须支持的 5 个操作（注意：这是语义层面的，不规定 CLI 接口）：

#### Query State: `state <container-id>`
- 返回上述容器状态 JSON
- 容器不存在时 MUST 报错

#### Create: `create <container-id> <path-to-bundle>`
- ID 在运行时范围内必须唯一
- 必须按 config.json 创建环境，但 `process.args` MUST NOT 在此时执行
- 其余 process 属性 MAY 在此时应用
- config.json 可以在此步被运行时校验

#### Start: `start <container-id>`
- 容器必须处于 `created` 状态
- 执行 config.json 中 `process` 指定的用户程序
- 如果 `process` 未设置则 MUST 报错

#### Kill: `kill <container-id> <signal>`
- 容器必须处于 `created` 或 `running` 状态
- 向容器进程发送指定信号

#### Delete: `delete <container-id>`
- 容器必须处于 `stopped` 状态
- 删除 create 阶段创建的资源
- 不属于本容器创建的资源 MUST NOT 被删除
- 删除后 ID 可以被新容器复用

### 5. config.json 完整结构深度解析

#### 通用配置（所有平台）

```json
{
  "ociVersion": "1.3.0",

  "root": {
    "path": "rootfs",
    "readonly": true
  },

  "process": {
    "terminal": true,
    "user": { "uid": 0, "gid": 0, "additionalGids": [5, 6] },
    "args": ["/bin/sh"],
    "env": ["PATH=/usr/bin:/bin", "TERM=xterm"],
    "cwd": "/",
    "capabilities": {
      "bounding": ["CAP_AUDIT_WRITE", "CAP_KILL"],
      "effective": ["CAP_AUDIT_WRITE", "CAP_KILL"],
      "inheritable": ["CAP_AUDIT_WRITE"],
      "permitted": ["CAP_AUDIT_WRITE", "CAP_KILL"],
      "ambient": ["CAP_AUDIT_WRITE"]
    },
    "rlimits": [
      { "type": "RLIMIT_NOFILE", "hard": 1024, "soft": 1024 }
    ],
    "noNewPrivileges": true,
    "apparmorProfile": "my-profile",
    "selinuxLabel": "system_u:system_r:svirt_lxc_net_t:s0:c124,c675"
  },

  "hostname": "my-container",
  "domainname": "example.com",

  "mounts": [
    {
      "destination": "/proc",
      "type": "proc",
      "source": "proc"
    },
    {
      "destination": "/dev",
      "type": "tmpfs",
      "source": "tmpfs",
      "options": ["nosuid", "strictatime", "mode=755", "size=65536k"]
    },
    {
      "destination": "/data",
      "source": "/volumes/data",
      "options": ["bind", "rw"]
    }
  ],

  "hooks": { ... },
  "annotations": { "com.example.app": "redis" },
  "linux": { ... }
}
```

#### process 字段详解

| 子字段 | 说明 |
|--------|------|
| `terminal` | 是否分配 pseudo-terminal（交互式容器需要） |
| `user` | uid/gid/additionalGids，Linux 还支持 umask |
| `args` | 启动命令 + 参数（第一个元素为可执行文件路径） |
| `env` | 环境变量数组，格式 `KEY=VALUE` |
| `cwd` | 容器内工作目录（必须是绝对路径） |
| `capabilities` | Linux capabilities 的 5 个集合（bounding/effective/inheritable/permitted/ambient） |
| `rlimits` | 进程资源限制（文件描述符数、进程数等） |
| `noNewPrivileges` | 设为 true 阻止子进程获得比父进程更多的权限 |
| `oomScoreAdj` | OOM killer 评分调整 (-1000 ~ 1000) |

#### mounts 字段详解

挂载规范支持多种挂载类型和丰富的选项：

| 属性 | 说明 |
|------|------|
| `destination` | 容器内挂载点（绝对路径，v1.2.0+ 也支持相对路径） |
| `source` | 宿主机源路径或设备名 |
| `type` | 挂载类型：`bind`, `proc`, `tmpfs`, `sysfs`, `devpts`, `cgroup` 等 |
| `options` | 挂载选项数组 |
| `uidMappings`/`gidMappings` | v1.2.0+ 支持 idmap mount（用户命名空间映射挂载） |

v1.2.0 新增的 **idmap mount** 允许将挂载的文件所有权映射到不同的 uid/gid 空间，对 rootless 容器至关重要。

### 6. Hooks 系统详解

Hooks 允许在容器生命周期的不同阶段注入自定义操作：

```
┌────────────────────────────────────────────────────────────────────┐
│                        Hook 执行上下文                              │
├─────────────────────┬────────────────┬─────────────────────────────┤
│ Hook                │ 执行命名空间    │ 典型用途                     │
├─────────────────────┼────────────────┼─────────────────────────────┤
│ prestart (已废弃)   │ 运行时命名空间  │ 向后兼容                     │
│ createRuntime       │ 运行时命名空间  │ 网络配置（CNI）              │
│ createContainer     │ 容器命名空间    │ 容器内部初始化               │
│ startContainer      │ 容器命名空间    │ 进程启动前的最后准备         │
│ poststart           │ 运行时命名空间  │ 通知、监控注册               │
│ poststop            │ 运行时命名空间  │ 网络清理、日志归档           │
└─────────────────────┴────────────────┴─────────────────────────────┘
```

Hook 配置示例：

```json
{
  "hooks": {
    "createRuntime": [{
      "path": "/usr/bin/cni-setup",
      "args": ["cni-setup", "--container-id", "abc123"],
      "env": ["PATH=/usr/bin"],
      "timeout": 10
    }],
    "poststop": [{
      "path": "/usr/bin/cleanup-network",
      "args": ["cleanup-network", "abc123"]
    }]
  }
}
```

每个 hook 通过 stdin 接收容器的 state JSON，使得 hook 程序能获知容器 ID、PID、bundle 路径等信息。

**Hook 的分层设计意义**：
- `createRuntime` 在运行时命名空间执行，可以看到宿主机网络，适合 CNI 网络插件
- `createContainer` 在容器命名空间执行，可以修改容器内部状态
- 这种分层让 hook 既能操作宿主机资源，也能操作容器内部

### 7. Linux 特定配置深度解析

#### Namespaces — 隔离的基石

```json
{
  "linux": {
    "namespaces": [
      { "type": "pid" },
      { "type": "network", "path": "/var/run/netns/mynet" },
      { "type": "mount" },
      { "type": "ipc" },
      { "type": "uts" },
      { "type": "user" },
      { "type": "cgroup" },
      { "type": "time" }
    ]
  }
}
```

| Namespace | 隔离的资源 | 内核标志 | 使用场景 |
|-----------|-----------|----------|----------|
| `pid` | 进程 ID 空间 | CLONE_NEWPID | 容器内 PID 从 1 开始 |
| `network` | 网络协议栈 | CLONE_NEWNET | 独立网卡、IP、路由表、iptables |
| `mount` | 文件系统挂载点 | CLONE_NEWNS | 独立的挂载视图 |
| `ipc` | System V IPC、POSIX 消息队列 | CLONE_NEWIPC | 隔离共享内存和信号量 |
| `uts` | 主机名和域名 | CLONE_NEWUTS | 独立的 hostname |
| `user` | 用户/组 ID 映射 | CLONE_NEWUSER | rootless 容器的基础 |
| `cgroup` | cgroup 根目录视图 | CLONE_NEWCGROUP | 隐藏宿主机 cgroup 层级 |
| `time` | 系统时钟偏移 | CLONE_NEWTIME | 容器可以有不同的时间 |

**关键细节**：
- `path` 字段可选：如果设置了，表示加入一个已存在的 namespace（而非创建新的）
- 这使得多个容器可以共享同一个 network namespace，实现 Pod 模型（Kubernetes 的基础）

#### Cgroups — 资源控制

```json
{
  "linux": {
    "resources": {
      "memory": {
        "limit": 536870912,
        "reservation": 268435456,
        "swap": 536870912,
        "disableOOMKiller": false
      },
      "cpu": {
        "shares": 1024,
        "quota": 100000,
        "period": 100000,
        "cpus": "0-3",
        "mems": "0"
      },
      "pids": {
        "limit": 1024
      },
      "blockIO": {
        "weight": 500,
        "throttleReadBpsDevice": [
          { "major": 8, "minor": 0, "rate": 104857600 }
        ]
      },
      "hugepageLimits": [
        { "pageSize": "2MB", "limit": 209715200 }
      ],
      "network": {
        "classID": 1048577,
        "priorities": [
          { "name": "eth0", "priority": 500 }
        ]
      }
    }
  }
}
```

| 资源类别 | 关键字段 | cgroup v2 对应 | 说明 |
|----------|----------|---------------|------|
| **memory** | limit, reservation, swap | memory.max, memory.low, memory.swap.max | 内存硬限制、软限制、交换空间 |
| **cpu** | shares, quota, period, cpus | cpu.weight, cpu.max, cpuset.cpus | CPU 权重、配额、绑核 |
| **pids** | limit | pids.max | 进程数上限 |
| **blockIO** | weight, throttle* | io.weight, io.max | 块设备 IO 权重和限速 |
| **hugepages** | pageSize, limit | hugetlb.*.max | 大页内存限制 |
| **network** | classID, priorities | — (v1 only) | 网络流量分类 |

#### Seccomp — 系统调用过滤

```json
{
  "linux": {
    "seccomp": {
      "defaultAction": "SCMP_ACT_ERRNO",
      "architectures": ["SCMP_ARCH_X86_64", "SCMP_ARCH_X86"],
      "syscalls": [
        {
          "names": ["read", "write", "exit", "rt_sigreturn"],
          "action": "SCMP_ACT_ALLOW"
        },
        {
          "names": ["clone"],
          "action": "SCMP_ACT_ALLOW",
          "args": [
            { "index": 0, "value": 2080505856, "op": "SCMP_CMP_MASKED_EQ" }
          ]
        }
      ]
    }
  }
}
```

支持的动作：

| Action | 行为 |
|--------|------|
| `SCMP_ACT_ALLOW` | 允许系统调用 |
| `SCMP_ACT_ERRNO` | 拒绝并返回 errno |
| `SCMP_ACT_KILL` | 终止线程 |
| `SCMP_ACT_KILL_PROCESS` | 终止进程 |
| `SCMP_ACT_TRAP` | 发送 SIGSYS 信号 |
| `SCMP_ACT_TRACE` | 通知 ptrace tracer |
| `SCMP_ACT_LOG` | 记录日志但允许 |
| `SCMP_ACT_NOTIFY` | 发送到 seccomp 用户空间 agent（高级用法） |

seccomp 还支持按参数值过滤（`args` 字段），可以精确控制特定系统调用的特定参数组合。

#### 安全路径控制

```json
{
  "linux": {
    "maskedPaths": [
      "/proc/acpi",
      "/proc/kcore",
      "/proc/keys",
      "/proc/latency_stats",
      "/proc/timer_list",
      "/proc/timer_stats",
      "/proc/sched_debug",
      "/sys/firmware"
    ],
    "readonlyPaths": [
      "/proc/asound",
      "/proc/bus",
      "/proc/fs",
      "/proc/irq",
      "/proc/sys",
      "/proc/sysrq-trigger"
    ]
  }
}
```

- **maskedPaths**: 用 bind mount `/dev/null` 或空 tmpfs 隐藏这些路径，防止容器读取敏感内核信息
- **readonlyPaths**: 以只读方式暴露，防止容器修改宿主机内核参数

#### Intel RDT — 硬件级资源隔离（v1.3.0 增强）

```json
{
  "linux": {
    "intelRdt": {
      "closID": "container1",
      "l3CacheSchema": "L3:0=fff;1=fff",
      "memBwSchema": "MB:0=100;1=100",
      "schemata": "L3:0=fff;1=fff\\nMB:0=100;1=100",
      "enableMonitoring": true
    }
  }
}
```

利用 Intel Resource Director Technology 控制 CPU 缓存分配和内存带宽，实现硬件级别的性能隔离。

#### 网络设备（v1.3.0 新增）

```json
{
  "linux": {
    "netDevices": {
      "eth0": {
        "name": "ceth0"
      }
    }
  }
}
```

允许将宿主机网络设备直接移入容器的网络命名空间。

#### 内存策略（v1.3.0 新增）

```json
{
  "linux": {
    "memoryPolicy": {
      "mode": "bind",
      "nodes": [0, 1]
    }
  }
}
```

在 NUMA 架构上控制容器进程的内存分配策略。

### 8. Features 结构（v1.1.0+）— 运行时能力发现

Features 结构让上层工具能发现运行时支持哪些能力，而不需要试错：

```json
{
  "ociVersionMin": "1.0.0",
  "ociVersionMax": "1.3.0",
  "hooks": ["prestart", "createRuntime", "createContainer",
            "startContainer", "poststart", "poststop"],
  "mountOptions": ["bind", "rbind", "ro", "rw", "nosuid", "nodev", "noexec",
                   "relatime", "idmap", "ridmap"],
  "linux": {
    "namespaces": ["pid", "network", "mount", "ipc", "uts", "user", "cgroup", "time"],
    "capabilities": ["CAP_NET_ADMIN", "CAP_SYS_ADMIN", ...],
    "cgroup": {
      "v1": true,
      "v2": true,
      "systemd": true,
      "systemdUser": true,
      "rdma": true
    },
    "seccomp": {
      "enabled": true,
      "actions": ["SCMP_ACT_ALLOW", "SCMP_ACT_ERRNO", "SCMP_ACT_KILL", ...],
      "operators": ["SCMP_CMP_EQ", "SCMP_CMP_NE", ...],
      "archs": ["SCMP_ARCH_X86_64", "SCMP_ARCH_AARCH64", ...]
    },
    "apparmor": { "enabled": true },
    "selinux": { "enabled": true },
    "intelRdt": {
      "enabled": true,
      "schemata": true,
      "enableMonitoring": true
    }
  },
  "annotations": {
    "org.opencontainers.runc.checkpoint.enabled": ""
  },
  "potentiallyUnsafeConfigAnnotations": [
    "org.opencontainers.runc.*"
  ]
}
```

**设计意义**：
- containerd/CRI-O 可以查询运行时能力，做出智能调度决策
- 避免"试了才知道不支持"的糟糕体验
- 支持运行时实现差异的优雅处理

### 9. 多平台支持的设计模式

规范通过**通用 + 平台特定**的分层设计支持多平台：

```
config.md (通用)
  ├── process, root, mounts, hooks, annotations  ← 所有平台共享
  │
  ├── config-linux.md      ← namespaces + cgroups + seccomp + apparmor
  ├── config-windows.md    ← Windows containers (HCS)
  ├── config-freebsd.md    ← FreeBSD jails (v1.3.0 新增)
  ├── config-solaris.md    ← Solaris zones
  ├── config-vm.md         ← 虚拟机 (hypervisor + kernel + image)
  └── config-zos.md        ← IBM z/OS
```

每个平台扩展使用 config.json 中对应的顶级字段（`linux`、`windows`、`freebsd` 等），互不干扰。

### 10. 错误处理与一致性保证

规范对错误处理有严格要求：

- **原子性**: 产生错误时，环境状态 MUST 回到操作之前的状态（即操作像从未发生一样）
- **Warning 语义**: 某些场景（如 poststop hooks 失败）只产生 warning，不影响后续流程
- **未定义行为**: 运行时可以定义额外的状态值，但不能与规范定义的状态语义冲突

这种设计确保了上层编排系统能对容器生命周期做出可靠的判断。

---

## How Well - 做得怎么样

### 版本历史

| 版本 | 发布日期 | 关键变化 |
|------|----------|----------|
| v1.0.0 | 2017-07-19 | 首个正式版本 |
| v1.1.0 | 2022-11-28 | 新增 features 结构、createRuntime/createContainer hooks |
| v1.2.0 | 2024-02-13 | idmap mount、相对路径 mount、annotation 支持 |
| v1.2.1 | 2025-02-27 | CPU affinity、cpus/mems 格式描述 |
| v1.3.0 | 2025-11-04 | **FreeBSD 平台支持**、Intel RDT 增强、VM hwConfig、网络设备、内存策略 |

### 成熟度评估

| 维度 | 评价 |
|------|------|
| 规范稳定性 | 极高 — 从 v1.0.0 到 v1.3.0 保持向后兼容 |
| 实现覆盖度 | 极高 — runc、crun、youki、gVisor、Kata 等多种实现 |
| 行业采纳度 | 事实标准 — Docker、Kubernetes、Podman 全部基于此规范 |
| 平台覆盖度 | 广泛 — Linux/Windows/FreeBSD/Solaris/VM/z/OS |
| 社区活跃度 | 活跃 — 持续有新特性和修复 |

### 生态系统位置

```
┌─────────────────────────────────────────────────┐
│              Kubernetes / Docker Compose          │
├─────────────────────────────────────────────────┤
│           containerd / CRI-O / Docker            │
├─────────────────────────────────────────────────┤
│    ┌─────────────────────────────────────────┐   │
│    │         OCI Runtime Spec                │   │ ← 你在这里
│    │    (config.json + lifecycle API)         │   │
│    └─────────────────────────────────────────┘   │
├─────────────────────────────────────────────────┤
│     runc / crun / youki / gVisor / Kata          │
├─────────────────────────────────────────────────┤
│           Linux kernel (ns, cgroups, ...)         │
└─────────────────────────────────────────────────┘
```

---

## 参考链接

- [GitHub 仓库](https://github.com/opencontainers/runtime-spec)
- [OCI 官网](https://opencontainers.org)
- [规范正文](https://github.com/opencontainers/runtime-spec/blob/main/spec.md)
- [实现列表](https://github.com/opencontainers/runtime-spec/blob/main/implementations.md)
- [OCI Charter](https://github.com/opencontainers/tob/blob/master/CHARTER.md)
