# runc - 3W2H 深度调研

> 调研日期: 2026-04-01
> 最新稳定版: v1.4.1 (2026-03-13 发布)
> 最新 RC: v1.5.0-rc.1 (2026-03-13 发布)
> GitHub: https://github.com/opencontainers/runc
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

runc 是 OCI Runtime Specification 的**参考实现**，一个用 Go 编写的 CLI 工具，负责在 Linux 上根据 OCI 规范创建和运行容器。

### 核心定位

```
runc 在容器技术栈中的位置:

  ┌──────────────────────────────────────┐
  │  kubectl / docker CLI / podman CLI    │  用户接口层
  ├──────────────────────────────────────┤
  │  Kubernetes / Docker Engine / Podman  │  编排/引擎层
  ├──────────────────────────────────────┤
  │  containerd / CRI-O                   │  容器管理层
  ├──────────────────────────────────────┤
  │  ★ runc ★                            │  容器运行时层 ← 你在这里
  ├──────────────────────────────────────┤
  │  Linux kernel (namespaces, cgroups)   │  内核层
  └──────────────────────────────────────┘
```

runc 是一个**底层工具**，不面向终端用户设计。它被 containerd、CRI-O 等上层运行时调用。

### 核心能力

| 能力 | 说明 |
|------|------|
| **容器生命周期管理** | create, start, run, kill, delete |
| **Linux Namespaces** | pid, network, mount, user, uts, ipc, cgroup, time |
| **Cgroups** | v1 和 v2，CPU/内存/IO/PID 资源限制 |
| **Seccomp** | 系统调用过滤（通过 libseccomp） |
| **Rootless 容器** | 无 root 权限运行容器 |
| **Checkpoint/Restore** | 通过 CRIU 支持容器迁移（可选） |
| **Intel RDT** | 缓存和内存带宽资源控制 |
| **CPU Affinity** | 进程 CPU 绑定 |

### CLI 命令

```bash
runc create <container-id>    # 创建容器（不启动进程）
runc start <container-id>     # 启动容器进程
runc run <container-id>       # create + start 一步到位
runc state <container-id>     # 查询容器状态
runc list                     # 列出所有容器
runc kill <container-id> SIG  # 发送信号
runc delete <container-id>    # 删除容器
runc exec <container-id> CMD  # 在容器中执行额外进程
runc spec                     # 生成默认 config.json 模板
runc features                 # 列出运行时支持的特性
```

---

## Why - 为什么

### 历史背景

runc 源自 Docker 的内部容器运行时 `libcontainer`：

```
时间线:
  2013    Docker 诞生，使用 LXC 作为运行时
  2014    Docker 用 libcontainer 替换 LXC
  2015.06 Docker 将 libcontainer 捐赠给 OCI，成为 runc
  2015.06 OCI (Open Container Initiative) 成立
  2017.07 runc v1.0.0 发布（与 runtime-spec v1.0.0 同期）
  2024~   runc 进入快速迭代期（v1.2 → v1.3 → v1.4 → v1.5-rc）
```

### 要解决的问题

1. **容器运行时标准化**: 提供规范的参考实现，确保规范可落地
2. **去 Docker 耦合**: 将容器运行能力从 Docker 中解耦，任何编排系统都可使用
3. **安全边界**: 在操作系统层面提供容器隔离的最小实现

### 核心价值

1. **参考实现**: 规范的"活文档"——runc 的行为就是规范的正确理解
2. **生产验证**: 全球绝大多数容器实际上通过 runc 运行
3. **安全优先**: 经过 Cure53 第三方安全审计，持续修复 CVE
4. **最小化**: 只做容器运行时该做的事，不包含镜像管理、网络等

---

## Who - 谁在做

### 维护者

| 姓名 | 所属组织 | GitHub | 角色 |
|------|----------|--------|------|
| Aleksa Sarai | SUSE | @cyphar | 核心维护者，主要发版人 |
| Akihiro Suda | NTT | @AkihiroSuda | 核心维护者 |
| Kir Kolyshkin | - | @kolyshkin | 核心维护者，发版人 |
| Mrunal Patel | Red Hat | @mrunalp | 维护者 |
| Sebastiaan van Stijn | - | @thaJeztah | 维护者 |
| Li Fu Bang | - | @lifubang | 维护者 |
| Rodrigo Campos | - | @rata | 维护者 |

### 主要使用者

| 项目 | 关系 |
|------|------|
| **containerd** | 默认 OCI 运行时调用 runc |
| **Docker Engine** | 通过 containerd 间接使用 runc |
| **CRI-O** | Kubernetes 容器运行时，默认调用 runc |
| **Podman** | 默认使用 crun，也支持 runc |
| **Kubernetes** | 通过 containerd/CRI-O 间接使用 |

### 竞争/替代实现

| 项目 | 语言 | 特点 |
|------|------|------|
| **crun** | C | 更轻量、启动更快，Red Hat 维护 |
| **youki** | Rust | 内存安全，社区驱动 |
| **gVisor (runsc)** | Go | 用户态内核，更强沙箱隔离 |
| **Kata Containers** | Go | VM 级别隔离 |

---

## How - 怎么做

### 1. 代码架构

```
runc/
├── cmd/runc/                  # CLI 入口
│   ├── main.go                # 主入口，注册所有子命令
│   ├── create.go              # runc create
│   ├── start.go               # runc start
│   ├── run.go                 # runc run (create + start)
│   ├── exec.go                # runc exec
│   ├── kill.go                # runc kill
│   ├── delete.go              # runc delete
│   ├── state.go               # runc state
│   ├── list.go                # runc list
│   ├── spec.go                # runc spec (生成 config.json 模板)
│   ├── features.go            # runc features
│   ├── checkpoint.go          # runc checkpoint (CRIU)
│   └── restore.go             # runc restore (CRIU)
│
├── libcontainer/              # 核心库（原 Docker libcontainer）
│   ├── factory_linux.go       # 容器工厂 — 创建/加载容器
│   ├── container_linux.go     # 容器对象 — 生命周期管理
│   ├── process_linux.go       # 进程管理 — 启动/exec
│   ├── init_linux.go          # 容器 init 进程 — 在新 ns 中执行
│   ├── rootfs_linux.go        # rootfs 准备 — pivot_root/挂载
│   ├── standard_init_linux.go # 标准容器 init 流程
│   ├── setns_init_linux.go    # exec 进入容器的 init 流程
│   │
│   ├── nsenter/               # C 代码 — namespace 进入（Go runtime 前执行）
│   │   ├── nsenter.go         # cgo import
│   │   ├── nsexec.c           # 核心：clone/fork + setns
│   │   └── clonehelper.c      # clone3() 封装
│   │
│   ├── cgroups/               # cgroup 管理
│   │   ├── manager/           # cgroup manager 接口
│   │   ├── fs/                # 直接文件系统操作 (cgroup v1)
│   │   ├── fs2/               # cgroup v2 统一层级
│   │   └── systemd/           # systemd cgroup driver
│   │
│   ├── configs/               # 容器配置结构体（Go 类型）
│   ├── seccomp/               # seccomp-bpf 过滤器生成
│   ├── apparmor/              # AppArmor profile 加载
│   ├── dmz/                   # runc 自身二进制保护
│   ├── keys/                  # 内核 keyring 管理
│   ├── userns/                # user namespace 工具
│   └── utils/                 # 通用工具函数
│
├── script/                    # 构建和 CI 脚本
├── tests/                     # 集成测试（bats 框架）
└── docs/                      # 文档
```

### 2. 容器创建的完整执行流程

`runc create` 是最关键的操作，涉及多个进程和内核交互：

```
用户调用: runc create mycontainer

 runc 父进程 (宿主机 namespace)
  │
  │ 1. 解析 config.json → libcontainer.Config
  │ 2. 校验配置合法性
  │ 3. 创建容器状态目录 (/run/runc/mycontainer/)
  │ 4. 创建 socketpair 用于父子通信
  │
  │ 5. clone() / fork() ← nsenter C 代码在 Go runtime 之前执行
  │    │
  │    ▼
  │  runc init [stage 1] (C 代码, nsexec.c)
  │  │
  │  │  a. 通过 socketpair 从父进程获取 namespace 配置
  │  │  b. 调用 clone3() / unshare() 创建新的 namespaces
  │  │  c. 如果配置了 user namespace: 设置 uid/gid 映射
  │  │  d. 如果配置了 cgroup namespace: 设置 cgroup
  │  │
  │  │  ▼ 进入新 namespace 后
  │  │
  │  runc init [stage 2] (Go 代码, standard_init_linux.go)
  │  │
  │  │  a. 设置 session keyring
  │  │  b. 通过 socketpair 获取完整配置
  │  │  c. 准备 rootfs:
  │  │     - 挂载 config.json 中声明的 mounts
  │  │     - 使用 libpathrs 安全解析路径（防止 symlink 攻击）
  │  │     - pivot_root() 切换根文件系统
  │  │     - 挂载 /proc, /dev, /sys
  │  │     - 创建 /dev/null, /dev/zero, /dev/random 等设备
  │  │     - 应用 maskedPaths（bind mount /dev/null）
  │  │     - 应用 readonlyPaths（remount readonly）
  │  │  d. 设置 hostname, domainname
  │  │  e. 配置 apparmor profile
  │  │  f. 设置 sysctl 参数
  │  │  g. 配置 seccomp (如果是 seccomp notify 模式)
  │  │  h. 设置 rlimits
  │  │  i. 删除多余的 capabilities
  │  │  j. 设置 no_new_privileges
  │  │  k. 设置 selinux label
  │  │  l. 加载 seccomp 过滤器
  │  │
  │  │  m. 通过 socketpair 通知父进程: "我已就绪"
  │  │  n. 阻塞等待 "exec fifo" ← 等待 runc start
  │  │
  │  ▼ (进程阻塞在 exec fifo)
  │
  │ 6. 父进程收到就绪信号
  │ 7. 执行 createRuntime hooks（在宿主机 namespace）
  │ 8. 执行 createContainer hooks（在容器 namespace）
  │ 9. 将容器状态设为 "created"
  │ 10. 父进程退出
  │
  │
  ▼ 后续: runc start mycontainer
  │
  │ 1. 打开 exec fifo → 唤醒阻塞的 runc init
  │ 2. runc init 执行 startContainer hooks
  │ 3. runc init 调用 exec() 替换为用户指定的程序
  │    → 容器进入 "running" 状态
```

### 3. nsenter — 为什么需要 C 代码

runc 在一个 Go 项目中包含了关键的 C 代码（`libcontainer/nsenter/nsexec.c`），这是由 Go 运行时的限制决定的：

**问题**：
- Go 运行时在 `main()` 之前就会启动多个 goroutine（GC、信号处理等）
- 这些 goroutine 会创建多个 OS 线程
- Linux 的 `clone()` / `unshare()` 只影响调用线程
- 如果在 Go runtime 启动后再创建 namespace，其他线程不会进入新的 namespace
- 特别是 PID namespace 的 `CLONE_NEWPID` 要求在 fork 时设置

**解决方案**：
```c
// nsexec.c — 在 Go runtime 之前执行
// 通过 cgo 的 constructor 属性
__attribute__((constructor)) static void nsexec(void) {
    // 1. 检查是否是 runc init 进程
    // 2. 从 socketpair 读取 namespace 配置
    // 3. 执行 clone3() 创建新 namespaces
    // 4. 设置 uid/gid 映射
    // 5. 完成后交给 Go 运行时
}
```

`__attribute__((constructor))` 确保这段 C 代码在 `main()` 和 Go runtime 初始化之前执行。

### 4. runc init 的双阶段设计

```
runc create
  │
  │ fork()
  │
  ├─→ Stage 1 (C, nsexec.c)
  │     - clone3() 创建 namespaces
  │     - 这是在 Go runtime 之前的纯 C 执行
  │     - 完成 namespace 创建后返回到 Go
  │
  └─→ Stage 2 (Go, standard_init_linux.go)
        - 在新 namespace 中执行
        - Go runtime 已启动
        - 执行 rootfs 准备、挂载、安全策略等
        - 最终阻塞等待 start 信号
```

这种双阶段设计是 runc 架构中最精妙的部分——它解决了 Go 语言与 Linux namespace 语义不兼容的核心矛盾。

### 5. rootfs 准备 — pivot_root 深度解析

容器的文件系统隔离通过 `pivot_root` 系统调用实现（而非简单的 `chroot`）：

```
挂载过程:

1. 在 mount namespace 中，rootfs 目录被 bind mount 到自身
2. 按 config.json 顺序挂载所有 mounts:
   - /proc (type: proc)
   - /dev  (type: tmpfs，然后创建设备节点)
   - /dev/pts (type: devpts)
   - /dev/shm (type: tmpfs)
   - /dev/mqueue (type: mqueue)
   - /sys (type: sysfs 或 bind)
   - 用户自定义挂载
3. pivot_root(new_root, put_old):
   - 将容器 rootfs 设为新的 /
   - 原来的 / 挂在 put_old 上
4. umount2(put_old, MNT_DETACH):
   - 卸载旧的根文件系统
   - 容器进程无法再访问宿主机文件系统
5. 应用 maskedPaths 和 readonlyPaths

为什么 pivot_root 比 chroot 安全:
  - chroot 只改变路径解析的起点，进程仍在原来的 mount namespace
  - pivot_root 实际替换了进程的根挂载点
  - 配合 mount namespace，旧的根文件系统被完全卸载
  - 即使容器进程获得 CAP_SYS_CHROOT，也无法逃逸
```

#### libpathrs — 路径安全保护

传统的文件路径操作（`open()`, `mkdir()`, `stat()`）存在 TOCTOU (Time-of-check to time-of-use) 竞争：

```
攻击场景 (无 libpathrs):
  1. runc 检查 /container/rootfs/etc/resolv.conf 是否安全
  2. 攻击者将 /container/rootfs/etc 替换为指向 /host/etc 的 symlink
  3. runc 写入 /container/rootfs/etc/resolv.conf → 实际写入了 /host/etc/resolv.conf
  → 容器逃逸！

libpathrs 的解决:
  - 使用 openat2() + RESOLVE_NO_SYMLINKS / RESOLVE_BENEATH
  - 所有路径操作都在 rootfs fd 之下进行
  - 内核级别保证路径不会逃出 rootfs 边界
```

v1.5.0-rc.1 开始 libpathrs 默认启用，runc 1.6 计划将其设为必须依赖。

### 6. Cgroup 管理 — 三种驱动

runc 支持三种 cgroup 管理方式：

```
┌─────────────────────────────────────────────────────────────┐
│                    Cgroup 管理驱动                           │
├────────────────┬────────────────────┬───────────────────────┤
│ 直接 fs 操作    │ systemd cgroup      │ systemd user session  │
│ (cgroup v1)    │ driver              │ driver                │
├────────────────┼────────────────────┼───────────────────────┤
│ 直接读写       │ 通过 systemd DBus   │ 用于 rootless 容器    │
│ /sys/fs/cgroup │ API 管理 cgroup     │ 使用用户级 systemd    │
│                │                    │                       │
│ 最低开销       │ 与 systemd 状态     │ 不需要 root 权限      │
│ 最大灵活性     │ 保持一致            │                       │
│                │ Kubernetes 推荐     │                       │
└────────────────┴────────────────────┴───────────────────────┘
```

#### cgroup v1 vs v2

```
cgroup v1 (已废弃，v1.4.0+):
  /sys/fs/cgroup/
  ├── cpu/              ← 每个资源一个独立的层级
  │   └── docker/
  │       └── <id>/
  │           ├── cpu.shares
  │           └── cpu.cfs_quota_us
  ├── memory/
  │   └── docker/
  │       └── <id>/
  │           ├── memory.limit_in_bytes
  │           └── memory.usage_in_bytes
  ├── pids/
  └── ...

cgroup v2 (推荐):
  /sys/fs/cgroup/
  └── system.slice/
      └── docker-<id>.scope/  ← 统一层级，所有资源在同一目录
          ├── cpu.max
          ├── cpu.weight
          ├── memory.max
          ├── memory.current
          ├── pids.max
          └── io.max
```

runc v1.4.0 正式废弃 cgroup v1，未来版本将移除支持。

### 7. Seccomp 实现细节

runc 使用 libseccomp 库生成 BPF (Berkeley Packet Filter) 程序来过滤系统调用：

```
config.json seccomp 配置
        │
        ▼
libcontainer/seccomp/
        │
        │  1. 解析 seccomp 配置
        │  2. 设置默认 action
        │  3. 为每个 syscall 规则调用 libseccomp API
        │  4. 处理参数条件过滤
        │
        ▼
libseccomp 生成 BPF 字节码
        │
        ▼
seccomp(SECCOMP_SET_MODE_FILTER, ...)
        │
        ▼
Linux 内核在每次系统调用时执行 BPF 过滤
```

**Seccomp Notify (高级模式)**：
- 将 seccomp 事件转发到用户空间 agent
- agent 可以检查参数、修改行为、模拟系统调用
- 用于容器内 `mount()` 等需要精细控制的场景
- runc 支持通过 socket 将 notify fd 传递给外部 agent

### 8. Rootless 容器 — 无 root 运行

Rootless 模式是 runc 的重要创新，允许非特权用户运行容器：

```
Rootless 容器工作原理:

  宿主机                                容器内
  ├── UID 1000 (alice)                  ├── UID 0 (root)
  │                                     │
  │   user namespace 映射:              │
  │   宿主 UID 1000 → 容器 UID 0       │
  │   宿主 GID 1000 → 容器 GID 0       │
  │                                     │
  │   /etc/subuid: alice:100000:65536   │   UID 1-65535 映射到
  │   /etc/subgid: alice:100000:65536   │   宿主 UID 100000-165535
```

**Rootless 的限制**：
- 需要内核支持 unprivileged user namespaces (`CONFIG_USER_NS=y`)
- 网络功能受限（没有 CAP_NET_ADMIN，不能创建 veth pair）
- 不能绑定 <1024 的端口
- cgroup 操作可能受限（需要 cgroup v2 + systemd user session）

**使用方式**：
```bash
runc spec --rootless                          # 生成 rootless config.json
runc --root /tmp/runc run mycontainerid       # 状态存储在用户可写目录
```

### 9. Checkpoint/Restore — 容器迁移

通过集成 CRIU (Checkpoint/Restore In Userspace) 实现容器状态快照和恢复：

```
Checkpoint:
  runc checkpoint --image-path /tmp/checkpoint mycontainer
  │
  ├── 冻结容器进程 (SIGSTOP)
  ├── CRIU dump 进程状态到 image 文件:
  │   ├── 内存页面
  │   ├── 打开的文件描述符
  │   ├── 网络连接状态
  │   ├── 进程树
  │   └── 信号处理器
  └── 容器停止

Restore:
  runc restore --image-path /tmp/checkpoint mycontainer
  │
  ├── 从 image 文件恢复进程状态
  ├── 重建 namespaces 和 cgroups
  ├── 恢复内存页面
  ├── 重建文件描述符
  └── 进程继续执行 (SIGCONT)
```

典型用途：容器实时迁移、快速启动（从快照恢复比冷启动快）。

### 10. runc exec — 进入运行中的容器

`runc exec` 在已运行的容器中启动额外进程，使用 `setns()` 进入已存在的 namespaces：

```
runc exec -t mycontainer /bin/sh
  │
  ├── 1. 查找容器的 init 进程 PID
  ├── 2. 读取 /proc/<pid>/ns/ 下的 namespace 文件
  │      ├── /proc/<pid>/ns/mnt
  │      ├── /proc/<pid>/ns/pid
  │      ├── /proc/<pid>/ns/net
  │      ├── /proc/<pid>/ns/ipc
  │      ├── /proc/<pid>/ns/uts
  │      └── /proc/<pid>/ns/user
  ├── 3. nsenter C 代码: setns() 进入每个 namespace
  ├── 4. Go 代码 (setns_init_linux.go):
  │      ├── 设置 capabilities
  │      ├── 设置 apparmor/selinux
  │      ├── 加载 seccomp 过滤器
  │      └── 设置 rlimits
  ├── 5. v1.5.0+: 请求 systemd 将 exec 进程移入容器 cgroup
  └── 6. exec() 替换为用户指定的命令
```

### 11. /proc/self/exe 保护 — 防止运行时自身被篡改

runc v1.2.0 引入了 `/proc/self/exe` sealing 机制：

```
攻击场景 (CVE-2019-5736):
  1. 容器内恶意进程覆写 /proc/self/exe
  2. 由于 /proc/self/exe 指向宿主机上的 runc 二进制
  3. 攻击者可以覆写宿主机上的 runc
  4. 下次任何人运行 runc → 执行攻击者的代码
  → 容器逃逸 + 宿主机被控制

runc 的防护:
  v1.2.0+: runc 通过 memfd_create() 创建内存文件
           将自身二进制复制到内存文件中
           然后 exec 内存文件中的副本
           → 容器进程看到的 /proc/self/exe 指向 memfd
           → 即使覆写也只影响内存副本，不影响宿主机磁盘上的 runc
```

### 12. containerd 如何调用 runc

理解 runc 在实际生产环境中的使用方式：

```
kubectl run nginx --image=nginx
  │
  ▼
kubelet (CRI 接口)
  │
  ▼
containerd (CRI 实现)
  │
  ├── 1. 从 registry 拉取 OCI Image
  ├── 2. 解压 layers → rootfs (使用 snapshotter)
  ├── 3. 从 image config 生成 config.json (OCI Bundle)
  ├── 4. 创建 cgroup
  │
  ├── 5. 调用: runc create --bundle /var/lib/containerd/.../bundle <container-id>
  │      → runc 创建 namespaces, 准备 rootfs, 配置安全策略
  │      → 返回 "created" 状态
  │
  ├── 6. 配置网络 (CNI)
  │      → 在 network namespace 中创建 veth pair, 分配 IP
  │
  ├── 7. 调用: runc start <container-id>
  │      → 容器用户进程开始执行
  │
  ├── 8. 监控容器进程 (通过 containerd-shim)
  │      → shim 进程持有容器的 stdin/stdout
  │      → 即使 containerd 重启，容器也不受影响
  │
  └── 9. 容器退出后: runc delete <container-id>
         → 清理 namespaces 和状态文件
```

**containerd-shim 的作用**：
- 作为 runc 和 containerd 之间的中间层
- 允许 containerd 重启而不影响运行中的容器
- 持有容器的 stdio 和 exit status
- 每个容器一个 shim 进程

### 13. Build Tags 与编译选项

| Build Tag | 功能 | 默认启用 | 依赖 | 说明 |
|-----------|------|----------|------|------|
| `seccomp` | syscall 过滤 | 是 | libseccomp v2.6.0+ | 生产环境强烈推荐 |
| `libpathrs` | 路径安全 | 是 (v1.5.0+) | libpathrs v0.2.4+ | 防止 symlink 攻击 |
| `runc_nocriu` | 禁用 checkpoint/restore | 否 | criu | 不需要迁移功能时可禁用 |

已废弃的 Build Tags：
- `runc_nodmz` — v1.2.1 起 runc dmz 二进制已移除
- `nokmem` — v1.0.0-rc94 起内核内存设置被忽略
- `apparmor` — v1.0.0-rc93 起 apparmor 总是启用
- `selinux` — v1.0.0-rc93 起 selinux 总是启用

### 14. 平台与架构支持

| 架构 | 支持级别 | 说明 |
|------|----------|------|
| amd64 | 完全支持 | 主要开发和测试平台 |
| arm64 | 完全支持 | ARM 服务器和嵌入式 |
| armhf (ARMv7) | 完全支持 | v1.4.1 起明确为 ARMv7 |
| ppc64le | 完全支持 | IBM Power |
| s390x | 完全支持 | IBM Z |
| riscv64 | 完全支持 | RISC-V |
| i386 | 完全支持 | 32 位 x86 |
| loong64 | 初步支持 | v1.4.1 新增，LoongArch |

---

## How Well - 做得怎么样

### 版本历史（近期）

| 版本 | 发布日期 | 关键变化 |
|------|----------|----------|
| v1.0.0 | 2021-06-22 | 首个 1.0 稳定版 |
| v1.1.0 | 2022-01-14 | cgroup v2 全面支持 |
| v1.2.0 | 2024-09-13 | /proc/self/exe sealing、idmap mount |
| v1.3.0 | 2025-05-13 | runtime-spec v1.3 支持、Intel RDT 增强 |
| v1.3.5 | 2026-03-17 | 挂载标志修复、回归修复 |
| v1.4.0 | 2025-11-27 | cgroup v1 废弃、runtime-spec v1.3 合规、CVE 修复 |
| v1.4.1 | 2026-03-13 | loong64 支持、fd 泄漏修复、回归修复 |
| v1.5.0-rc.1 | 2026-03-13 | libpathrs 默认启用、大量废弃 API 清理、user.* sysctl |

### 发布策略

```
发版节奏:
  - 每 ~6 个月一个 minor 版本
  - 前一个 minor 版本降级为 security-only
  - 前前一个 minor 版本变为 unmaintained

  当前支持状态 (2026-04):
    v1.5.x  ── RC 阶段（预计 2026-04 月底 GA）
    v1.4.x  ── 全功能支持
    v1.3.x  ── security + significant bugfix only
    v1.2.x  ── 仅高危 CVE，2026-04 月底 EOL
```

### 安全

- **第三方审计**: Cure53 完成安全审计（报告公开）
- **CII Best Practices**: 已获得 Core Infrastructure Initiative 最佳实践徽章
- **CVE 响应**: 历史上修复过多个容器逃逸漏洞（如 CVE-2019-5736、CVE-2024-21626、CVE-2025-52881）
- **签名发布**: 所有 release 都经过 GPG 签名

### 成熟度评估

| 维度 | 评价 |
|------|------|
| 代码成熟度 | 极高 — 源自 Docker 2014 年的 libcontainer，10+ 年生产验证 |
| 安全性 | 极高 — 第三方审计、持续 CVE 修复、libpathrs 加固 |
| 性能 | 良好 — 作为 Go 实现，比 crun (C) 稍慢但足够 |
| 兼容性 | 极高 — OCI runtime-spec 参考实现 |
| 社区活跃度 | 极高 — 持续快速迭代，多家大厂核心参与 |
| 行业采纳度 | 统治性 — 全球大部分容器通过 runc 运行 |

### 与其他运行时对比

| 特性 | runc | crun | youki |
|------|------|------|-------|
| 语言 | Go | C | Rust |
| 启动速度 | 基准 | 更快 (~2x) | 接近 runc |
| 内存占用 | 基准 | 更小 | 接近 runc |
| 成熟度 | 最高 | 高 | 中 |
| 默认使用 | Docker/containerd/CRI-O | Podman (可选) | 实验性 |
| 安全审计 | 是 | — | — |
| rootless | 支持 | 支持 | 支持 |

---

## 参考链接

- [GitHub 仓库](https://github.com/opencontainers/runc)
- [安全审计报告](https://github.com/opencontainers/runc/blob/master/docs/Security-Audit.pdf)
- [Go Reference](https://pkg.go.dev/github.com/opencontainers/runc)
- [spec-conformance 文档](https://github.com/opencontainers/runc/blob/main/docs/spec-conformance.md)
- [OCI Runtime Spec](https://github.com/opencontainers/runtime-spec)
- [libpathrs](https://github.com/cyphar/libpathrs)
- [RELEASES.md](https://github.com/opencontainers/runc/blob/main/RELEASES.md)
