# containerd 3W2H Deep Research

## What - What is containerd?

containerd (pronounced "container-dee") is an industry-standard container runtime with an emphasis on simplicity, robustness, and portability. It is a **CNCF Graduated Project** (the highest maturity level), responsible for managing the complete container lifecycle on a host system: image transfer and storage, container execution and supervision, low-level storage and network attachments, and more.

### Position in the Container Stack

```
  Higher-level systems (Docker Engine, Kubernetes kubelet, nerdctl, BuildKit, k3s...)
                              |
                         containerd  (manages images, containers, tasks, snapshots)
                              |
                    OCI Runtime (runc, kata, gVisor...)
                              |
                    Linux / Windows Kernel
```

containerd is a **middle-layer daemon** designed to be embedded into larger systems, not used directly by end-users. Its CLI tool `ctr` is intentionally minimal and unstable -- it exists only for debugging. User-facing tools like Docker, nerdctl, crictl, or Kubernetes kubelet sit above it.

### Core Capabilities (Scope)

**In Scope:**

- Container execution (create, start, stop, pause, resume, exec, signal, delete)
- Copy-on-Write filesystems (overlay, btrfs snapshot drivers)
- Image distribution (push/pull OCI/Docker images from any OCI Distribution Spec-compliant registry)
- Container-level metrics (cgroup stats, OOM events)
- Namespaces (multi-tenant sharing of a single containerd instance)
- Checkpoint and restore (via CRIU, supporting container live migration)
- CRI plugin (built-in Kubernetes Container Runtime Interface implementation)
- Runtime Shim v2 API (pluggable low-level runtimes)

**Explicitly Out of Scope:** Networking (handled by CNI), Building (handled by BuildKit), Volume management, Log persistence.

### Current Release Status

| Release | Status | Notes |
|---------|--------|-------|
| 1.7 | LTS | Supported until September 2026 |
| 2.0 | EOL | Released 2024.11, EOL 2025.11 |
| 2.1/2.2 | Active | Actively maintained |
| 2.3 | Upcoming (LTS) | April 2026, first release under new cadence |

Starting with v2.3, containerd has moved to a **4-month release cadence** synchronized with the Kubernetes release schedule (April, August, December). One release per year is designated Long Term Stable (LTS) with at least 2 years of support.

---

## Why - Why was containerd created?

### Historical Context

containerd was born from **Docker's architectural decomposition**. In Docker's early monolithic architecture, `dockerd` handled everything -- image management, container execution, networking, storage, and the API. This created three core problems:

1. **Tight coupling** -- a bug in one component could affect the entire system
2. **Difficult to reuse** -- projects like Kubernetes that wanted container execution capabilities had to interface with the entire Docker stack
3. **Standardization pressure** -- the OCI (Open Container Initiative) was formed in 2015 to standardize container formats and runtimes, pushing for clean separation of concerns

Docker decomposed the engine into independent components: `runc` (donated to OCI in 2015) handles low-level container creation, and `containerd` (first released December 2015) manages the full container lifecycle. containerd was donated to CNCF in March 2017 and graduated in February 2019.

### Why is containerd important?

1. **The real runtime behind Docker** -- `docker run` actually calls containerd -> runc
2. **The default Kubernetes runtime** -- After K8s 1.24 removed dockershim, containerd became the dominant CRI runtime; all major cloud providers use it by default
3. **The standard integration point** -- Shim v2 API enables Kata Containers, Firecracker, gVisor and other diverse execution backends to integrate through a unified interface

### Comparison with Alternatives

| Dimension | containerd | CRI-O |
|-----------|-----------|-------|
| Scope | Docker + K8s + custom platforms | Kubernetes only |
| Production deployment | All three major cloud providers + Docker embedded | Primarily Red Hat OpenShift |
| Plugin ecosystem | Snapshot plugins, runtime shims, NRI, CDI | Leaner |
| Design philosophy | General-purpose container runtime platform | K8s-specific minimal runtime |

---

## Who - Who is behind containerd?

### Founders

- **Michael Crosby** (`@crosbymichael`) -- Foundational architect. Core Docker engineer whose "fingerprints are all over the container ecosystem from the early days of Docker to libcontainer into runc."
- **Stephen Day** (`@stevvooe`) -- Architect of the containerd 1.0 designs and author of the initial OCI distribution specification.

### Current Core Maintainers (Committers)

Akihiro Suda (NTT), Derek McGowan, Phil Estes, Mike Brown (IBM), Fu Wei, Maksym Pavlenko, Davanum Srinivas, Kazuyoshi Kato (Baseten), Samuel Karp, Kirtana Ashok, and others.

### Companies Involved

Docker/Moby (origin), **Amazon AWS** (multiple maintainers; uses in EKS/Fargate/Firecracker/Bottlerocket), **Google** (GKE/COS), **Microsoft** (AKS; contributed Windows hcsshim support), **IBM**, **Intel**, **Alibaba** (PouchContainer), **NTT**, **Mirantis**, **DaoCloud**, and more.

### Major Production Users

Docker Engine, Google GKE, Amazon EKS/Fargate, Azure AKS, Rancher k3s, VMware TKG, Cloud Foundry, Kata Containers, Firecracker, Talos Linux, BuildKit, OpenFaaS faasd, and many more.

---

## How - How does containerd work? (Detailed Technical Architecture)

### Overall Architecture

containerd adopts a **"thin daemon + smart client"** architecture. The daemon handles low-level container operations (storage, execution, supervision), while all high-level logic (interacting with registries, generating OCI specs, loading images from tar) is handled by the **client**.

```
+-------------------------------------------------------------------+
|                          Client Layer                              |
|   (Docker/Moby, nerdctl, ctr, kubelet via CRI)                   |
+-------------------------------------------------------------------+
                              |
                         gRPC / tTRPC API (Unix Socket)
                              |
+-------------------------------------------------------------------+
|                     containerd Daemon                              |
|  +-------------------------------------------------------------+  |
|  |  gRPC/tTRPC Services (grpc.v1 plugins)                      |  |
|  |  containers, content, diff, events, images, leases,         |  |
|  |  namespaces, snapshots, tasks, version, cri, transfer       |  |
|  +-------------------------------------------------------------+  |
|  +-------------------------------------------------------------+  |
|  |  Service Plugins (service.v1)                                |  |
|  |  containers-service, content-service, images-service,        |  |
|  |  snapshots-service, tasks-service, leases-service...         |  |
|  +-------------------------------------------------------------+  |
|  +-------------------------------------------------------------+  |
|  |  Core Subsystems                                             |  |
|  |  - Metadata Store (BoltDB)    - Content Store (CAS)          |  |
|  |  - Snapshot Drivers           - Diff Service                 |  |
|  |  - Image Service              - Events (Exchange pub/sub)    |  |
|  |  - GC Scheduler               - Lease Manager               |  |
|  |  - Transfer Service           - NRI / CDI                   |  |
|  +-------------------------------------------------------------+  |
|  +-------------------------------------------------------------+  |
|  |  Runtime v2 (io.containerd.runtime.v2)                      |  |
|  |  Launches shim processes per container/pod                  |  |
|  +-------------------------------------------------------------+  |
+-------------------------------------------------------------------+
         |                           |
    ttrpc/grpc                 fork/exec
         |                           |
  +------------------+      +--------------------+
  | Shim Process     |      | Runtime Engine     |
  | (containerd-shim-|----->| (runc, kata,       |
  |  runc-v2)        | exec | gVisor...)         |
  +------------------+      +--------------------+
                                     |
                              +------+------+
                              |  Container  |
                              +-------------+
```

### Plugin System

Almost **all functionality in containerd is delivered via plugins**, including internal implementations. This ensures decoupling and treats internal and external extensions equally.

**Plugin registration and initialization flow:**

1. Plugins register via Go `init()` functions using `plugin.Register()`
2. At startup, containerd loads all registered plugins, resolves the dependency graph (plugins declare `Requires` for other plugin types)
3. Initializes in topological order. Each plugin receives an `InitContext` with access to other plugins, configuration, root/state directories, event bus, and metadata database
4. Plugin configuration is in `config.toml` under `[plugins."<type>.<id>"]`

**Two-tier service architecture:** Each API area has two plugin layers:

- **Service Plugin** (`io.containerd.service.v1`): Implements business logic, wraps lower-level stores
- **gRPC Plugin** (`io.containerd.grpc.v1`): Thin gRPC adapter exposing the service plugin over the API

**Key plugin types:**

| Type String | Purpose |
|-------------|---------|
| `io.containerd.runtime.v2` | Runtime Shim v2 |
| `io.containerd.snapshotter.v1` | Snapshot drivers (overlayfs/btrfs/devmapper etc.) |
| `io.containerd.content.v1` | Content store (CAS) |
| `io.containerd.metadata.v1` | Metadata store (BoltDB) |
| `io.containerd.service.v1` | Internal service implementations |
| `io.containerd.grpc.v1` | gRPC API endpoints |
| `io.containerd.gc.v1` | Garbage collection scheduler |
| `io.containerd.transfer.v1` | Transfer service |
| `io.containerd.nri.v1` | Node Resource Interface |
| `io.containerd.sandbox.controller.v1` | Sandbox controller |
| `io.containerd.image-verifier.v1` | Image signature verification |
| `io.containerd.cri.v1` | Kubernetes CRI implementation |
| `io.containerd.event.v1` | Event handling |
| `io.containerd.lease.v1` | Lease management |
| `io.containerd.differ.v1` | Layer diff computation |
| `io.containerd.streaming.v1` | Stream manager |

**External plugin mechanisms:**

- **V2 Runtimes**: Binaries in PATH named `containerd-shim-<name>-<version>`
- **Proxy Plugins**: External gRPC services connected via local Unix sockets, supporting `snapshot`, `content`, and `diff` proxy types. Configured in `[proxy_plugins]` in config.toml.

### Content Store (Content-Addressable Storage)

All OCI image content is stored in a **content-addressable** manner:

- **Disk path**: `/var/lib/containerd/io.containerd.content.v1.content/blobs/sha256/`
- Each file is named by its SHA256 digest, storing indexes, manifests, config blobs, and layer tarballs
- **Write flow**: Two-phase -- write to a temporary ingest directory first, verify the hash, then move to the final location
- **GC reference labels**: Reference chains are established via `containerd.io/gc.ref.*` labels:
  - Index -> `gc.ref.content.m.<N>` -> Manifest
  - Manifest -> `gc.ref.content.config` -> Config, `gc.ref.content.l.<N>` -> Layers
  - Config -> `gc.ref.snapshot.<snapshotter>` -> Top snapshot
- **Cross-namespace sharing**: Content is deduplicated by hash and shared globally; image name metadata is isolated per namespace
- **Distribution source tracking**: Each blob carries `containerd.io/distribution.source.<registry>=<repo>` labels

### Snapshot System (Snapshotter)

The snapshot system replaces Docker's "Graph Driver" (overlay2, aufs, etc.) with a cleaner, pluggable interface.

**Core concepts:**

- **Committed**: Immutable snapshot, corresponds to image layers
- **Active**: Mutable snapshot, corresponds to a running container's writable layer

**Key operations:**

- `Prepare(key, parent)` -> Create an active snapshot, returns mount instructions
- `Commit(name, key)` -> Convert active snapshot to committed (immutable)
- `View(key, parent)` -> Create a read-only active snapshot
- `Mounts(key)` -> Get mount instructions
- `Remove(key)` -> Delete a snapshot

**Example with OverlayFS (3-layer image):**

```
/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/
  metadata.db          # BoltDB tracking snapshot relationships
  snapshots/
    1/fs/              # Base layer (committed)
    2/fs/              # Layer 2 (committed)
    3/fs/              # Layer 3 (committed)
    4/fs/              # Container writable layer (active)
    4/work/            # OverlayFS work directory
```

Container mount command:
```
overlay on /rootfs type overlay (
  lowerdir=snapshots/3/fs:snapshots/2/fs:snapshots/1/fs,
  upperdir=snapshots/4/fs,
  workdir=snapshots/4/work
)
```

**Built-in snapshot drivers:** overlayfs (default), native (full copy), btrfs, zfs, devmapper, blockfile, erofs

**External snapshot drivers:** fuse-overlayfs (rootless), nydus (on-demand loading), stargz (eStargz lazy-pull), overlaybd (block-device accelerated)

### Runtime Shim v2 - The Core Container Execution Mechanism

This is one of containerd's most elegant designs. containerd **does NOT directly create containers** -- it manages them through **Shim processes**.

**Two-component model:**

| Component | Responsibility |
|-----------|---------------|
| **Shim** (e.g., `containerd-shim-runc-v2`) | The process containerd actually invokes. Starts a tTRPC server, receives lifecycle commands, invokes the runtime engine, manages container I/O, acts as sub-reaper for orphaned child processes, publishes events back to containerd |
| **Runtime Engine** (e.g., `runc`) | The actual OCI runtime that creates Linux namespaces/cgroups and exec's the container process. Invoked by the shim, NOT by containerd directly |

**Shim binary naming convention:**
`io.containerd.runc.v2` -> `containerd-shim-runc-v2` (take last two segments, replace `.` with `-`, add prefix)

**Shim lifecycle detailed flow:**

1. **Start Shim**: containerd executes `containerd-shim-runc-v2 start --namespace <ns> --address <socket> --publish-binary <binary> --id <container-id>`. The shim starts a tTRPC server and prints the socket address to stdout.
2. **Create**: containerd sends `TaskService.Create` to the shim. The shim calls `runc create`, which sets up namespaces/cgroups but **does NOT start the user process yet**.
3. **Start**: `TaskService.Start` is sent. The shim calls `runc start`, triggering the container init process to execute the user-specified command.
4. **Wait**: Blocks on `TaskService.Wait` until the container exits.
5. **Kill**: Sends a signal (SIGTERM/SIGKILL) to the container process.
6. **Delete**: `TaskService.Delete` cleans up resources, then `TaskService.Shutdown` causes the shim to exit.
7. **Fallback Delete**: If the shim is unreachable (e.g., was SIGKILL'd), containerd directly runs `containerd-shim-runc-v2 delete` binary to clean up remaining resources.

**Shim multiplexing (1:N model):**
For Kubernetes Pods, all containers sharing the same `io.kubernetes.cri.sandbox-id` label are managed by **a single shim process**. The shim decides whether to start a new instance or reuse an existing one.

**Decoupling from containerd:** If containerd crashes, shim processes and containers **continue running**. After containerd restarts, it reconnects to existing shims.

**Required shim events:**

| Topic | When |
|-------|------|
| `TaskCreateEventTopic` | Task successfully created |
| `TaskStartEventTopic` | Task successfully started (MUST follow Create) |
| `TaskExitEventTopic` | Task exits (MUST follow Start) |
| `TaskDeleteEventTopic` | Task removed from shim |
| `TaskPausedEventTopic` | Task paused |
| `TaskResumedEventTopic` | Task resumed |
| `TaskOOMEventTopic` | Out-of-memory detected |
| `TaskExecAddedEventTopic` | Exec process added |
| `TaskExecStartedEventTopic` | Exec process started |

**tTRPC vs gRPC:** Shim communication prefers tTRPC, which uses the same protobuf service definitions as gRPC but removes the HTTP/2 stack to minimize memory overhead and binary size.

### Complete Container Lifecycle

**Step 1: Create Container (metadata only)**
```
Client -> containerd gRPC -> containers-service -> BoltDB
```
Stores container metadata (ID, image reference, runtime name, OCI spec, snapshotter name, snapshot key). Creates an **active snapshot** on top of the image's committed snapshots as the container's writable layer. **No process is started.**

**Step 2: Create Task (prepare runtime)**
```
Client -> containerd -> tasks-service -> runtime v2 -> fork/exec shim
-> shim starts tTRPC -> runc create
```
containerd prepares the OCI bundle (config.json + rootfs mount info), starts the shim, and the shim calls `runc create` to create the container without starting it. Returns the container PID.

**Step 3: Start Task**
```
Client -> containerd -> shim -> runc start -> user process begins execution
```

**Step 4: Wait / Monitor**
Client calls `Wait` which blocks until the container exits. The shim monitors the container process.

**Step 5: Kill Task (optional)**
Sends a signal to the container process via the shim.

**Step 6: Delete Task**
Cleans up runtime resources and shuts down the shim.

**Step 7: Delete Container**
Removes container metadata and the active snapshot. GC cleans up unreferenced content.

### Image Pull and Unpack Flow

**Pull flow:**

1. **Resolve** -- Resolve image reference to a descriptor via registry HTTP API
2. **Platform Select** -- If an index (multi-platform manifest list), select the matching manifest
3. **Fetch & Store** -- Independently fetch each component into Content Store
4. **Set GC Labels** -- Establish reference chain from index -> manifest -> config/layers
5. **Create Image Record** -- Map name to target descriptor in metadata store

**Unpack to snapshots (for each layer, from base to top):**

1. `Prepare()` an active snapshot (from parent or blank for base)
2. Diff Applier reads the layer blob and applies it to the active snapshot
3. `Commit()` to make it an immutable committed snapshot
4. Use this as the parent for the next layer

### Namespace Mechanism

containerd implements **multi-tenancy isolation** through namespaces:

- Every API call carries `containerd-namespace` in gRPC metadata
- Containers, images, tasks, snapshot metadata, leases are all **isolated per namespace**
- **Content blobs are shared across namespaces** (deduplicated by hash)
- Namespaces are an **administrative isolation, NOT a security boundary**

Well-known namespaces: `default` (ctr), `k8s.io` (CRI plugin), `moby` (Docker)

Namespaces can have labels to configure defaults:
```bash
ctr namespaces label k8s.io containerd.io/defaults/snapshotter=btrfs
ctr namespaces label k8s.io containerd.io/defaults/runtime=testRuntime
```

### Events System

containerd uses a **pub/sub event system** built around the `Exchange` (an in-memory event bus).

**Event flow:**

1. **Internal events**: Published by plugins/services using `Publisher.Publish()` with a topic string
2. **Shim events**: Shims publish events back to containerd using the `-publish-binary` mechanism
3. **Exchange**: The in-memory event bus fans out events to subscribers
4. **Subscribers**: Clients subscribe via gRPC Events service with optional filter expressions

**Event topics follow a hierarchical naming:**
- `/tasks/create`, `/tasks/start`, `/tasks/exit`, `/tasks/delete`, `/tasks/oom`
- `/tasks/exec-added`, `/tasks/exec-started`, `/tasks/paused`, `/tasks/resumed`
- `/images/create`, `/images/update`, `/images/delete`
- `/containers/create`, `/containers/update`, `/containers/delete`
- `/snapshots/prepare`, `/snapshots/commit`, `/snapshots/remove`

### Garbage Collection (GC)

Label-based garbage collection:

- Resource relationships are tracked through structural properties and `containerd.io/gc.ref.*` labels
- **Leases** protect in-flight resources from being GC'd. Clients must hold a lease while creating resources
- GC scheduling parameters:
  - `pause_threshold`: Max fraction of time the DB can be locked for GC (default 2%)
  - `mutation_threshold`: Number of DB mutations before triggering GC (default 100)
  - `deletion_threshold`: Number of deletes to immediately trigger GC (default 0)
  - `schedule_delay`: Delay between trigger and GC execution
  - `startup_delay`: Delay before first GC after daemon start (default 100ms)

### Transfer Service (New in 2.0, Stable)

A unified content transfer abstraction replacing fragmented client-side image pull/push logic:

```go
type Transferrer interface {
    Transfer(ctx context.Context, source any, destination any, opts ...Opt) error
}
```

Through generic Source/Destination types, it unifies Pull, Push, Import, Export, and Tag operations, supporting streaming transfer and progress callbacks.

**Operations:**

| Operation | Source | Destination |
|-----------|--------|-------------|
| Pull | `ImageFetcher` | `ImageStorer` |
| Push | `ImageGetter` | `ImagePusher` |
| Import | `ImageImportStreamer` | `ImageStorer` |
| Export | `ImageLookup` | `ImageExportStreamer` |
| Tag | `ImageGetter` | `ImageStorer` |

The transfer service works with the **Streaming Service** (`io.containerd.streaming.v1`) which provides a bidirectional streaming abstraction, enabling content to be streamed between client and daemon without requiring the entire payload to be in memory.

### NRI (Node Resource Interface)

Allows external plugins to **intercept and modify container configurations** during lifecycle events (resource limits, environment variables, mounts, OCI hooks, devices, etc.).

**Plugin types:**
- **Pre-registered**: Symlinks in `/opt/nri/plugins/`, started automatically at boot. Named with priority index (e.g., `00-logger`)
- **Externally launched**: Connect to `/var/run/nri/nri.sock`, can be started/stopped independently

**NRI plugin capabilities:** Receive lifecycle events, inject OCI hooks, modify resource limits (CPU, memory, devices), add environment variables and mounts, adjust seccomp profiles, modify namespace settings, add annotations.

Enabled by default since 2.0.

### Sandbox Service (New in 2.0)

Decouples **Pod lifecycle from container lifecycle**, supporting mutable attributes via an `Update` API. This enables VM-level isolation (Firecracker, Kata) to integrate natively. The sandboxed CRI mode is enabled by default in 2.0.

### Client-Server Communication (gRPC API)

containerd exposes its API over a **Unix domain socket** (default: `/run/containerd/containerd.sock`) using gRPC. Key services:

| Service | Operations |
|---------|-----------|
| Containers | Get, List, Create, Update, Delete |
| Content | Info, Update, List, Delete, Read, Write, Status, Abort |
| Diff | Apply, Diff |
| Events | Publish, Forward, Subscribe |
| Images | Get, List, Create, Update, Delete |
| Leases | Create, Delete, List, AddResource, DeleteResource |
| Namespaces | Get, List, Create, Update, Delete |
| Snapshots | Prepare, View, Mounts, Commit, Remove, Stat, Update, List |
| Tasks | Create, Start, Delete, Kill, Exec, Pause, Resume, Wait, CloseIO, Checkpoint |
| Transfer | Transfer |
| Sandbox | Create, Start, Stop, Wait, Status, Shutdown |

**Namespace** is set as a gRPC metadata header (`containerd-namespace`) on every request. **Leases** are also passed as a gRPC header (`containerd-lease`) to protect resources from GC.

---

## How Well / How Much - Adoption and Quality

### Adoption Scale

- **GitHub**: 20,500+ stars, 3,800+ forks, 580+ open issues
- **Market share**: containerd adoption grew from 23% to 53% year-over-year, the most significant container runtime consolidation in the ecosystem
- **Cloud provider coverage**: AWS EKS (default since 1.24), Google GKE (the only supported runtime), Azure AKS (default)
- This means containerd runs **the vast majority of production Kubernetes workloads worldwide**
- Container technology market: $1.22 billion in 2026, projected to $6.43 billion by 2035

### Quality and Stability

- **CNCF Graduated** (February 2019, one of the earliest projects to graduate)
- **API stability**: gRPC API stable since v1.0, CRI gRPC API stable since v1.6, Go client API stable since v2.0
- **Security track record**: Few CVEs, promptly addressed. Has a formal Security Advisors role with embargo and responsible disclosure processes
- **Performance**: containerd + runc is the fastest combination in random read/write benchmarks; 2.0 added Intel ISA-L igzip for faster image pull decompression
- **Contributor growth**: Nearly 300% expansion in individual contributors over its lifecycle

### Evolution Milestones

| Event | Date |
|-------|------|
| First release | 2015.12 |
| 1.0 release | 2017.12 |
| Donated to CNCF | 2017.3 |
| CNCF Graduation | 2019.2 |
| 2.0 release (first major version in 7 years) | 2024.11 |
| 2.3 LTS (first release under new cadence) | 2026.4 (upcoming) |

### Key Changes in 2.0

- Sandbox Service stabilized
- Transfer Service stabilized
- NRI / CDI enabled by default
- Image verifier plugins
- CRI User Namespaces
- Removed legacy shim v1, CRI v1alpha2, AUFS snapshotter
- Removed `io_uring_*` syscalls from default seccomp profile (security hardening)
- New 4-month release cadence synchronized with Kubernetes releases

### Ecosystem

| Project | Stars | Purpose |
|---------|-------|---------|
| nerdctl | 9,976 | Docker-compatible CLI for containerd |
| stargz-snapshotter | 1,511 | Lazy-pulling image distribution |
| runwasi | 1,293 | WebAssembly/WASI workloads on containerd |
| cgroups | 1,180 | Go cgroups library |
| ttrpc | 645 | Lightweight gRPC alternative for shim communication |
| imgcrypt | 424 | OCI image encryption |
| nri | 376 | Node Resource Interface |
| overlaybd | 347 | Block-based remote image format |
| nydus-snapshotter | 238 | Lazy-pulling with P2P and dedup |

### Governance Model

- **Committers**: Write access and voting rights. Changes require 2/3 committer approval
- **Reviewers**: Core maintainers without write access. Their LGTM counts toward merge requirements
- **Security Advisors**: Advisory role for embargoed security disclosures
- All decisions flow through pull requests. Scope changes require 100% maintainer vote

---

## Summary

containerd has grown from a Docker internal component to the cornerstone container runtime of the cloud-native ecosystem. Its plugin-based architecture, shim decoupling design, content-addressable storage, and namespace multi-tenancy mechanism enable it to support both Docker's user experience and Kubernetes' large-scale orchestration needs. The 2.x series, through the stabilization of Sandbox/Transfer/NRI/CDI, has evolved containerd from a "container runtime" into a "container platform foundation."
