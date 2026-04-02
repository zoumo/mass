# OCI Image Specification - 3W2H 深度调研

> 调研日期: 2026-04-01
> 规范版本: v1.1.1 (2025-03-03 发布)
> GitHub: https://github.com/opencontainers/image-spec
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

OCI Image Specification 定义了容器镜像的**标准格式**，包括镜像清单（manifest）、文件系统层（layers）、镜像配置和镜像索引，使得容器镜像能在不同工具和注册中心之间互操作。

### 核心组成

```
image-spec/
├── spec.md              # 规范入口和总览
├── manifest.md          # 镜像清单 (Image Manifest)
├── image-index.md       # 镜像索引 (Image Index) — 多平台支持
├── layer.md             # 文件系统层 (Filesystem Layers)
├── config.md            # 镜像配置 (Image Configuration)
├── descriptor.md        # 内容描述符 (Content Descriptors)
├── image-layout.md      # 镜像布局 (Image Layout)
├── media-types.md       # 媒体类型定义
├── annotations.md       # 注解规范
├── conversion.md        # 镜像到运行时 Bundle 的转换
├── considerations.md    # 扩展性和规范化考量
└── artifacts-guidance.md # 非镜像内容的打包指南
```

### 关键概念

| 概念 | 说明 |
|------|------|
| **Manifest** | 描述镜像的组成——配置 + 层列表，每项通过 content-addressable digest 引用 |
| **Image Index** | 高层清单，指向多个 manifest（通常是不同平台/架构的变体） |
| **Layer** | 文件系统变更集（changeset），tar 格式，可选 gzip/zstd 压缩 |
| **Configuration** | 镜像运行时配置——CMD、ENTRYPOINT、ENV、EXPOSE、层顺序等 |
| **Descriptor** | 内容引用——mediaType + digest + size，用于寻址任何 blob |
| **Image Layout** | 本地文件系统上的镜像表示格式 |

### 镜像结构示意

```
Image Index (可选，多平台)
  │
  ├── Manifest (linux/amd64)
  │     ├── Config (JSON blob)
  │     │     ├── 创建时间、作者
  │     │     ├── 架构、OS
  │     │     ├── 层 diff_ids（未压缩 sha256）
  │     │     └── 运行时配置（cmd, env, volumes, ports...）
  │     │
  │     └── Layers[]
  │           ├── Layer 0 (base layer, tar+gzip)
  │           ├── Layer 1 (changeset, tar+gzip)
  │           └── Layer N (changeset, tar+gzip)
  │
  └── Manifest (linux/arm64)
        ├── Config
        └── Layers[]
```

### Media Types

```
application/vnd.oci.image.index.v1+json        # Image Index
application/vnd.oci.image.manifest.v1+json      # Image Manifest
application/vnd.oci.image.config.v1+json        # Image Config
application/vnd.oci.image.layer.v1.tar          # Layer (uncompressed)
application/vnd.oci.image.layer.v1.tar+gzip     # Layer (gzip)
application/vnd.oci.image.layer.v1.tar+zstd     # Layer (zstd)
application/vnd.oci.empty.v1+json               # Empty descriptor
```

---

## Why - 为什么

### 要解决的问题

Docker 镜像格式最初是 Docker 专有的：

```
问题:
  Docker Image Format v1/v2 ── Docker 专有
  ACI (App Container Image)  ── CoreOS/rkt 专有

  → 镜像格式不统一
  → 注册中心 API 不统一
  → 工具链被供应商锁定
```

### 解决方案

```
OCI Image Spec:
  ┌──────────────────────────────────────────────────────┐
  │  统一的镜像格式 + 统一的 Content Descriptor 模型      │
  │                                                      │
  │  任何工具构建 ──┐              ┌── Docker Hub         │
  │  Docker build  ─┤              ├── GitHub GHCR        │
  │  Buildah       ─┼── OCI Image ─┼── Harbor             │
  │  ko             ─┤              ├── ACR/ECR/GCR        │
  │  Buildpacks    ─┘              └── 任何 OCI Registry  │
  └──────────────────────────────────────────────────────┘
```

### 核心价值

1. **镜像互操作**: 一次构建，推送到任何兼容注册中心，在任何运行时执行
2. **Content Addressable**: 所有内容通过 digest（sha256）寻址，确保完整性
3. **多平台支持**: Image Index 天然支持多架构镜像（amd64、arm64、s390x 等）
4. **层复用**: 相同层可以在不同镜像之间共享，节省存储和传输
5. **可扩展性**: Artifacts Guidance 允许用 OCI 格式打包非容器内容（Helm charts、WASM 模块、SBOM 等）

### 与 Runtime Spec 的关系

```
OCI Image Spec                      OCI Runtime Spec
┌──────────────┐     unpack/convert  ┌──────────────┐
│  OCI Image   │ ──────────────────→ │  OCI Bundle  │
│  (registry)  │                     │  (config.json│
│              │    conversion.md    │   + rootfs)  │
└──────────────┘    定义转换规则      └──────────────┘
                                           │
                                           ▼
                                     runc create/start
```

---

## Who - 谁在做

### 维护者

| 姓名 | 所属组织 | GitHub |
|------|----------|--------|
| Brandon Mitchell | - | @sudo-bmitch |
| Jon Johnson | Chainguard | @jonjohnsonjr |
| Sajay Antony | Microsoft | @sajayantony |
| Stephen Day | - | @stevvooe |
| Tianon Gravi | - | @tianon |
| Aleksa Sarai | SUSE | @cyphar |

### 关键使用者

| 项目/产品 | 使用方式 |
|-----------|----------|
| **Docker** | 构建和推送 OCI 镜像 |
| **containerd** | 拉取和管理 OCI 镜像 |
| **Podman/Buildah** | Red Hat 的 OCI 镜像构建和运行工具链 |
| **Harbor** | VMware 的企业级 OCI Registry |
| **GitHub Container Registry** | 存储和分发 OCI 镜像 |
| **Helm 3.8+** | 使用 OCI 格式存储 Helm charts |
| **ORAS** | 用 OCI 格式推送/拉取任意 artifacts |
| **Sigstore/cosign** | OCI 镜像签名和验证 |

---

## How - 怎么做

### 1. 镜像构建到运行的完整流程

```
1. 构建阶段
   Dockerfile / Buildah ──→ 生成 OCI Image (manifest + config + layers)

2. 分发阶段
   docker push / oras push ──→ OCI Registry (Distribution Spec API)

3. 拉取阶段
   containerd / CRI-O ──→ 通过 manifest 获取 config + layers

4. 解压阶段
   layers (tar+gzip) ──→ 按顺序解压为 rootfs
   config ──→ 转换为 runtime-spec 的 config.json

5. 运行阶段
   rootfs + config.json = OCI Bundle ──→ runc create/start
```

### 2. Merkle DAG — 镜像的数据结构

OCI 镜像在逻辑上是一个 **Merkle 有向无环图 (DAG)**，所有节点通过 Content Descriptor 互相引用：

```
Image Index (可选)
  │  digest: sha256:aaa...
  │
  ├──→ Manifest (linux/amd64)
  │      digest: sha256:bbb...
  │      │
  │      ├──→ Config blob
  │      │      digest: sha256:ccc...
  │      │      (包含 rootfs.diff_ids 引用层的未压缩哈希)
  │      │
  │      ├──→ Layer 0 blob (base)
  │      │      digest: sha256:ddd...
  │      │
  │      ├──→ Layer 1 blob
  │      │      digest: sha256:eee...
  │      │
  │      └──→ Layer 2 blob
  │             digest: sha256:fff...
  │
  └──→ Manifest (linux/arm64)
         digest: sha256:ggg...
         └──→ ...
```

**Merkle DAG 的安全意义**：
- 修改任何一层的内容会改变其 digest
- 改变层的 digest 会改变 manifest 的内容（因为 manifest 引用了 digest）
- 改变 manifest 的内容会改变 manifest 自身的 digest
- 因此从顶层 digest 可以验证整个镜像的完整性（类似 Git 的 commit hash）

### 3. Content Descriptor — 万物皆引用

Descriptor 是 image-spec 最核心的抽象——所有组件之间的引用都通过它表达：

```json
{
  "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
  "digest": "sha256:6dbdec7...",
  "size": 3109422,
  "urls": ["https://mirror.example.com/layers/6dbdec7..."],
  "annotations": {
    "org.opencontainers.image.title": "layer.tar.gz"
  },
  "data": "base64-encoded-content-for-small-blobs",
  "artifactType": "application/vnd.example.sbom.v1"
}
```

| 字段 | 要求 | 说明 |
|------|------|------|
| `mediaType` | REQUIRED | 内容的 MIME 类型，遵循 RFC 6838 |
| `digest` | REQUIRED | 内容的加密哈希，格式 `algorithm:encoded` |
| `size` | REQUIRED | 原始内容字节数（防止 zip bomb 攻击） |
| `urls` | OPTIONAL | 可选的下载 URL 列表（用于 non-distributable layers） |
| `annotations` | OPTIONAL | 键值对元数据 |
| `data` | OPTIONAL | Base64 编码的内嵌内容（小 blob 可避免额外 roundtrip） |
| `artifactType` | OPTIONAL | 当描述 artifact 时标识其类型 |

#### Digest 算法

```
digest = algorithm ":" encoded

已注册的算法:
  sha256   ── REQUIRED，所有实现必须支持
  sha512   ── OPTIONAL，某些 CPU 上比 sha256 更快
  blake3   ── OPTIONAL，高性能并行哈希

示例:
  sha256:6c3c624b58dbbcd3c0dd82b4c53f04194d1247c6eebdaab7c610cf7d66709b3b
  sha512:401b09eab3c013d4ca54922bb802bec8fd5318192b0a75f201d8b372742...
  blake3:6c3c624b58dbbcd3c0dd82b4c53f04194d1247c6eebdaab7c610cf7d66709b3b
```

#### Digest 验证流程

```
C = 从 registry 或网络下载的内容字节
D = Descriptor 中声明的 digest

验证步骤:
  1. 检查 len(C) == Descriptor.size  ← 先验证大小，减少哈希碰撞空间
  2. 计算 H = sha256(C)
  3. 比较 "sha256:" + hex(H) == D    ← 匹配则内容可信
```

### 4. Image Manifest 详解

Manifest 是镜像的"组装说明书"，描述一个特定平台镜像的完整组成：

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.example+type",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:b5b2b2c507a0944348e0303114d8d93aaaa081732b86451d9bce1f432a537bc7",
    "size": 7023
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:9834876dcfb05cb167a5c24953eba58c4ac89b1adf57f28f2f9d09af107ee8f0",
      "size": 32654
    },
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:3c3a4604a545cdc127456d94e421cd355bca5b528f4a9c1905b15da2eb4a4c6b",
      "size": 16724
    },
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:ec4b8955958665577945c89419d1af06b5f7636b4ac3da7f12184802ad867736",
      "size": 73109
    }
  ],
  "subject": {
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "digest": "sha256:5b0bcabd1ed22e9fb1310cf6c2dec7cdef19f0ad69efa1f392e94a4333501270",
    "size": 7682
  },
  "annotations": {
    "com.example.key1": "value1"
  }
}
```

| 字段 | 说明 |
|------|------|
| `schemaVersion` | 固定为 2（向后兼容 Docker v2） |
| `config` | 指向镜像配置 JSON 的 descriptor |
| `layers` | 层数组，index 0 为 base layer，按叠加顺序排列 |
| `subject` | v1.1.0+ 新增，指向另一个 manifest，用于 referrers API（签名、SBOM 关联） |
| `artifactType` | v1.1.0+ 新增，标识 artifact 类型 |
| `annotations` | 键值对元数据 |

**layers 的排列约束**：
- index 0 MUST 是 base layer
- 后续层按叠加顺序（stack order）排列
- 最终文件系统 = 从空目录开始依次 apply 每一层的结果

### 5. Image Configuration 完整结构

Config 是镜像的"运行说明"，也是计算 ImageID 的基础：

```json
{
    "created": "2015-10-31T22:22:56.015925234Z",
    "author": "Alyssa P. Hacker <alyspdev@example.com>",
    "architecture": "amd64",
    "os": "linux",
    "os.version": "10.0.14393.1066",
    "os.features": ["win32k"],
    "variant": "v8",

    "config": {
        "User": "alice",
        "ExposedPorts": { "8080/tcp": {} },
        "Env": [
            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "FOO=oci_is_a",
            "BAR=well_written_spec"
        ],
        "Entrypoint": ["/bin/my-app-binary"],
        "Cmd": ["--foreground", "--config", "/etc/my-app.d/default.cfg"],
        "Volumes": { "/var/job-result-data": {}, "/var/log/my-app-logs": {} },
        "WorkingDir": "/home/alice",
        "Labels": {
            "com.example.project.git.url": "https://example.com/project.git",
            "com.example.project.git.commit": "45a939b2999782a3..."
        },
        "StopSignal": "SIGTERM"
    },

    "rootfs": {
        "type": "layers",
        "diff_ids": [
            "sha256:c6f988f4874bb0add23a778f753c65efe992244e148a1d2ec2a8b664fb66bbd1",
            "sha256:5f70bf18a086007016e948b04aed3b82103a36bea41755b6cddfaf10ace3c6ef"
        ]
    },

    "history": [
        {
            "created": "2015-10-31T22:22:54.690851953Z",
            "created_by": "/bin/sh -c #(nop) ADD file:a3bc1e842b69636f... in /"
        },
        {
            "created": "2015-10-31T22:22:55.613815829Z",
            "created_by": "/bin/sh -c #(nop) CMD [\"sh\"]",
            "empty_layer": true
        },
        {
            "created": "2015-10-31T22:22:56.329850019Z",
            "created_by": "/bin/sh -c apk add curl"
        }
    ]
}
```

#### 关键字段解析

| 字段 | 说明 |
|------|------|
| `architecture` | CPU 架构（遵循 Go 的 GOARCH：amd64、arm64、s390x 等） |
| `os` | 操作系统（遵循 Go 的 GOOS：linux、windows、freebsd 等） |
| `config.Entrypoint` | 容器启动命令（不可被 CMD 覆盖，除非显式指定） |
| `config.Cmd` | 默认参数（传给 Entrypoint，或作为独立命令） |
| `config.Env` | 环境变量，格式 `KEY=VALUE` |
| `config.User` | 运行用户（支持 `user`、`uid`、`user:group`、`uid:gid` 格式） |
| `config.ExposedPorts` | 声明容器暴露的端口（仅文档性质，不实际打开） |
| `config.Volumes` | 数据卷挂载点声明 |
| `rootfs.diff_ids` | 每层**未压缩** tar 的 sha256（注意：与 manifest 中的 layer digest 不同！） |
| `history` | 构建历史记录，包含 `empty_layer` 标记（如 ENV 指令不产生层） |

#### ImageID 计算

```
ImageID = sha256(config JSON bytes)

由于 config 的 rootfs.diff_ids 引用了每层的内容哈希
→ ImageID 实际上是整个镜像内容的唯一标识
→ 这就是为什么说 OCI 镜像是 "content-addressable" 的
```

### 6. Layer 文件系统变更集 — 深度解析

#### Layer 的本质

每一层是一个 tar 归档，包含相对于上一层的文件系统变更集 (changeset)：

```
Layer 0 (base):           Layer 1 (changeset):        最终文件系统:
./                        ./etc/my-app.d/             ./
./etc/                    ./etc/my-app.d/default.cfg  ./etc/
./etc/my-app-config       ./bin/my-app-tools          ./etc/my-app.d/
./bin/                    ./etc/.wh.my-app-config     ./etc/my-app.d/default.cfg
./bin/my-app-binary                                   ./bin/
./bin/my-app-tools                                    ./bin/my-app-binary
                                                      ./bin/my-app-tools (已更新)
```

#### 变更类型

| 类型 | 表示方式 | 说明 |
|------|----------|------|
| **添加** | 直接包含新文件 | 文件/目录/symlink/设备等 |
| **修改** | 包含完整的新版本文件 | 覆盖下层的同路径文件 |
| **删除** | `.wh.<filename>` whiteout 文件 | 空文件，表示删除下层对应文件 |
| **目录替换** | `.wh..wh..opq` opaque whiteout | 删除下层目录的全部子内容 |

#### Whiteout 详解

```
场景: 删除 /etc/my-app-config
表示: /etc/.wh.my-app-config     ← 前缀 .wh. + 原文件名

场景: 替换整个 /bin/ 目录内容
表示: /bin/.wh..wh..opq          ← 隐藏下层 bin/ 的所有子文件
      /bin/new-binary             ← 新内容

注意:
  - Whiteout 只对下层/父层生效，不影响同层文件
  - .wh. 前缀是保留的，无法创建真正以 .wh. 开头的文件
  - 规范推荐使用 explicit whiteout，但必须同时接受 opaque whiteout
  - Whiteout 文件应该排在同级目录条目之前
```

#### 覆盖已存在文件的规则

- 如果条目和已存在路径**都是目录**：用条目的属性替换现有目录属性
- 其他所有情况：先删除（unlink）已存在路径，再根据条目重建

#### 层的三种标识符

```
DiffID  = sha256(未压缩的 tar 归档)
         → 标识单个层的内容
         → 存储在 config.rootfs.diff_ids 中

ChainID = 标识层叠加后的结果
         → ChainID(L₀) = DiffID(L₀)
         → ChainID(L₀|...|Lₙ) = sha256(ChainID(L₀|...|Lₙ₋₁) + " " + DiffID(Lₙ))
         → 用于安全地标识"应用了前 N 层后的文件系统状态"

Layer Digest = sha256(可能压缩的 blob)
         → 存储在 manifest.layers[].digest 中
         → 用于从 registry 下载时的内容校验
```

**为什么需要 ChainID？**

```
假设有层 A, B, C（从下到上）

如果只用 DiffID(C) 标识，存在安全风险:
  - 攻击者可以替换 A 和 B，只要保持 C 不变
  - DiffID(C) 仍然匹配，但 A|B|C 的整体内容已被篡改

ChainID(A|B|C) = sha256(sha256(DiffID(A) + " " + DiffID(B)) + " " + DiffID(C))
  - 改变任何层都会改变最终的 ChainID
  - 保证了层叠加顺序的完整性
```

#### 文件属性

层中的文件必须（如果支持）包含以下属性：

| 属性 | tar 字段 | 说明 |
|------|----------|------|
| 修改时间 | mtime | 文件最后修改时间 |
| 用户 ID | uid | 文件所有者（优先于 uname） |
| 组 ID | gid | 文件所属组（优先于 gname） |
| 权限模式 | mode | rwxrwxrwx + setuid/setgid/sticky |
| 扩展属性 | xattrs | 如 security.selinux 标签 |
| 符号链接 | linkname | 软链接目标 |
| 硬链接 | linkname (type=1) | tar type 为 '1' 的硬链接条目 |

Windows 平台还支持额外的 PAX 扩展属性（MSWINDOWS.fileattr、MSWINDOWS.rawsd 等）。

#### 压缩格式

| 格式 | Media Type | 说明 |
|------|-----------|------|
| 未压缩 tar | `...layer.v1.tar` | 基本格式 |
| gzip | `...layer.v1.tar+gzip` | 最常用，RFC 1952 |
| zstd | `...layer.v1.tar+zstd` | 更高性能，RFC 8478，SHOULD 支持 |

实践中 `tar+gzip` 是最广泛使用的格式。zstd 提供更好的压缩比和解压速度，正在逐步普及。

### 7. Image Index — 多平台支持

Image Index 是一个"胖清单"，指向多个平台特定的 manifest：

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:aaa...",
      "size": 7143,
      "platform": {
        "architecture": "amd64",
        "os": "linux"
      }
    },
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:bbb...",
      "size": 7682,
      "platform": {
        "architecture": "arm64",
        "os": "linux",
        "variant": "v8"
      }
    },
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:ccc...",
      "size": 7920,
      "platform": {
        "architecture": "amd64",
        "os": "windows",
        "os.version": "10.0.14393.1066"
      }
    }
  ]
}
```

**工作流程**：
1. 客户端拉取 image index
2. 根据自身平台 (os/architecture/variant) 匹配最合适的 manifest
3. 下载匹配的 manifest 及其引用的 layers

这就是为什么 `docker pull nginx` 在 amd64 和 arm64 机器上下载不同的二进制，但使用同一个镜像名。

### 8. 镜像到 Runtime Bundle 的转换（conversion.md）

OCI 定义了从 image 到 runtime bundle 的标准转换流程：

```
OCI Image                              OCI Runtime Bundle
┌──────────────┐                       ┌──────────────────┐
│  Config      │──── 转换规则 ────────→│  config.json      │
│  - Env       │     Env → process.env │  - process        │
│  - Cmd       │     Cmd → process.args│    .args          │
│  - Entrypoint│     EP → process.args │    .env           │
│  - User      │     User → process.  │    .user          │
│  - WorkingDir│     user.uid/gid     │    .cwd           │
│  - Volumes   │     WD → process.cwd │  - mounts         │
│  - ExposedPorts│                     │  - root           │
│  - Labels    │                       │  - annotations    │
│              │                       │                   │
│  Layers[]    │──── 解压叠加 ────────→│  rootfs/          │
│  - Layer 0   │     按顺序 apply      │  (完整文件系统)    │
│  - Layer 1   │     处理 whiteout     │                   │
│  - Layer N   │                       │                   │
└──────────────┘                       └──────────────────┘
```

**转换规则**：
- `Entrypoint` + `Cmd` → `process.args`（合并逻辑与 Docker 相同）
- `Env` → `process.env`
- `User` → 解析为 `process.user.uid` / `process.user.gid`
- `WorkingDir` → `process.cwd`
- `Volumes` → 可选的挂载配置
- `ExposedPorts` → 提示信息（不直接映射到 runtime 配置）
- `Labels` → `annotations`

### 9. Artifacts — OCI 作为通用包格式（v1.1.0+）

v1.1.0 的重大创新是将 OCI 从"容器镜像格式"扩展为"通用内容寻址包格式"：

```
OCI 格式可以打包:
├── 容器镜像 (传统用途)
├── Helm Charts (helm push --oci)
├── WASM 模块
├── SBOM (Software Bill of Materials)
├── 签名 (Sigstore/cosign)
├── 策略文件 (OPA/Gatekeeper)
├── ML 模型
└── 任何 blob 数据
```

#### Artifact 打包的三种模式

**模式 1: 纯元数据 artifact（无实际 blob）**

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.example+type",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2
  },
  "layers": [{
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2
  }],
  "annotations": { "com.example.data": "payload-in-annotation" }
}
```

**模式 2: 有内容但无配置的 artifact**

```json
{
  "artifactType": "application/vnd.example.sbom.v1",
  "config": { "mediaType": "application/vnd.oci.empty.v1+json", ... },
  "layers": [{
    "mediaType": "application/vnd.example.sbom.spdx+json",
    "digest": "sha256:e258d248...",
    "size": 1234
  }]
}
```

**模式 3: 有内容 + 有配置的完整 artifact**

```json
{
  "artifactType": "application/vnd.example+type",
  "config": {
    "mediaType": "application/vnd.example.config.v1+json",
    "digest": "sha256:5891b5b5...",
    "size": 123
  },
  "layers": [{
    "mediaType": "application/vnd.example.data.v1.tar+gzip",
    "digest": "sha256:e258d248...",
    "size": 1234
  }]
}
```

#### Empty Descriptor 约定

为了表示"空内容"，规范定义了标准的空描述符：

```json
{
  "mediaType": "application/vnd.oci.empty.v1+json",
  "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
  "size": 2,
  "data": "e30="
}
```

对应的 blob 内容就是 `{}`（2 字节），base64 编码为 `e30=`。

#### subject 与 Referrers API

v1.1.0 引入的 `subject` 字段建立了 artifact 与目标镜像的弱关联：

```
nginx:latest (镜像)
  │
  ├── referrer: cosign 签名 (subject → nginx manifest)
  ├── referrer: SBOM (subject → nginx manifest)
  └── referrer: vulnerability scan (subject → nginx manifest)
```

客户端可以通过 Distribution Spec 的 `referrers` API 查询某个镜像的所有关联 artifact。

### 10. Image Layout — 本地文件系统表示

OCI 定义了镜像在本地文件系统上的标准目录结构：

```
my-image/
├── oci-layout               # {"imageLayoutVersion": "1.0.0"}
├── index.json                # 入口 Image Index
└── blobs/
    └── sha256/
        ├── aaa...            # manifest blob
        ├── bbb...            # config blob
        ├── ccc...            # layer 0 blob
        └── ddd...            # layer 1 blob
```

- `oci-layout` 文件标识这是一个 OCI Image Layout
- `index.json` 是入口点（Image Index 或 Manifest）
- `blobs/` 目录按 digest 算法分子目录存储所有 blob
- 这个格式允许不依赖 registry 进行本地镜像操作（如 `skopeo copy` 到本地目录）

---

## How Well - 做得怎么样

### 版本历史

| 版本 | 发布日期 | 关键变化 |
|------|----------|----------|
| v1.0.0 | 2017-07-19 | 首个正式版本，与 runtime-spec v1.0.0 同期 |
| v1.0.1 | 2018-04-20 | 修复和澄清 |
| v1.0.2 | 2021-08-18 | 小修复 |
| v1.1.0-rc1~rc6 | 2023~2024 | Artifacts 支持演进 |
| v1.1.0 | 2024-02-15 | **Artifacts Guidance**、empty config、image index 增强 |
| v1.1.1 | 2025-03-03 | 修复和更新 |

### 成熟度评估

| 维度 | 评价 |
|------|------|
| 规范稳定性 | 极高 — v1.0 到 v1.1 保持完全向后兼容 |
| 行业采纳度 | 事实标准 — 所有主流容器工具和注册中心都支持 |
| 扩展能力 | 强 — Artifacts 机制让 OCI 成为通用包格式 |
| 社区活跃度 | 活跃 — 持续演进，多家大厂参与维护 |
| 工具生态 | 丰富 — Go types、JSON Schema、验证工具等 |

### 与 Docker Image Format 的对比

| 特性 | Docker Image v2 | OCI Image Spec |
|------|-----------------|----------------|
| 开放标准 | 否（Docker 控制） | 是（OCI 治理） |
| 多平台 | 支持（manifest list） | 支持（image index） |
| Artifacts | 不支持 | v1.1.0+ 支持 |
| 实际兼容性 | 与 OCI 高度兼容 | — |
| 注册中心 API | Docker Registry API | OCI Distribution Spec |

> 实际上 Docker Image Format v2 和 OCI Image Spec 非常接近，OCI 基本上是 Docker 格式的标准化演进。

---

## 参考链接

- [GitHub 仓库](https://github.com/opencontainers/image-spec)
- [规范正文](https://github.com/opencontainers/image-spec/blob/main/spec.md)
- [OCI Distribution Spec](https://github.com/opencontainers/distribution-spec)
- [ORAS (OCI Registry As Storage)](https://oras.land/)
- [Artifacts Guidance](https://github.com/opencontainers/image-spec/blob/main/artifacts-guidance.md)
- [OCI 官网](https://opencontainers.org)
