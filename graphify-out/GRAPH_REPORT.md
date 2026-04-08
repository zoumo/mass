# Graph Report - .  (2026-04-08)

## Corpus Check
- 108 files · ~128,107 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1119 nodes · 1627 edges · 104 communities detected
- Extraction: 65% EXTRACTED · 35% INFERRED · 0% AMBIGUOUS · INFERRED: 566 edges (avg confidence: 0.51)
- Token cost: 0 input · 0 output

## God Nodes (most connected - your core abstractions)
1. `SpecSuite` - 30 edges
2. `newTestHarness()` - 27 edges
3. `newMockShimServer()` - 23 edges
4. `connHandler` - 21 edges
5. `newTestTerminalManager()` - 21 edges
6. `replyError()` - 18 edges
7. `ConfigSuite` - 18 edges
8. `ProcessManager` - 18 edges
9. `validWorkspaceSpec()` - 16 edges
10. `unmarshalParams()` - 16 edges

## Surprising Connections (you probably didn't know these)
- `OAR Layered Architecture` --semantically_similar_to--> `containerd Runtime Shim v2`  [INFERRED] [semantically similar]
  README.md → docs/research/containerd.md
- `main()` --calls--> `newRootCmd()`  [INFERRED]
  internal/testutil/mockagent/main.go → cmd/agent-shim/main.go
- `main()` --calls--> `dial()`  [INFERRED]
  internal/testutil/mockagent/main.go → cmd/agent-shim-cli/main.go
- `main()` --calls--> `runPrompt()`  [INFERRED]
  internal/testutil/mockagent/main.go → cmd/agent-shim-cli/main.go
- `main()` --calls--> `runChat()`  [INFERRED]
  internal/testutil/mockagent/main.go → cmd/agent-shim-cli/main.go

## Hyperedges (group relationships)
- **Agent Protocol Stack (A2A + ACP + MCP)** — a2a_protocol, acp_protocol_overview, mcp_protocol, a2a_protocol_stack [EXTRACTED 1.00]
- **OAR Architecture Mirrors containerd Design** — readme_layered_architecture, containerd_shim_v2, containerd_thin_daemon_smart_client, containerd_events_system, rationale_containerd_inspiration [EXTRACTED 0.95]
- **OCI Specification Ecosystem** — oci_runtime_spec, oci_image_spec, runc_research, containerd_research, oci_image_conversion [EXTRACTED 1.00]
- **Bootstrap Contract Convergence (session/new, session/prompt, cwd resolution)** — contract_convergence_bootstrap_contract, agentd_bootstrap_contract, design_rationale_bootstrap_phases, runtime_spec_lifecycle, agentd_session_new, agentd_session_prompt [EXTRACTED 0.90]
- **Desired vs Realized Room Model (Room Spec / agentd / ARI)** — contract_convergence_desired_vs_realized_room, room_spec_desired_vs_realized, agentd_realized_room_manager, ari_spec_room_methods, unified_plan_dec001 [EXTRACTED 0.90]
- **Shim Recovery Protocol (status + history + subscribe)** — shim_rpc_spec_recovery_semantics, shim_rpc_spec_runtime_status, shim_rpc_spec_runtime_history, shim_rpc_spec_session_subscribe, agent_shim_blast_radius [EXTRACTED 0.95]

## Communities

### Community 0 - "Workspace Handlers & Tests"
Cohesion: 0.03
Nodes (3): WorkspaceError, equalStrings(), TestBuildCloneArgs()

### Community 1 - "Shim RPC Server Tests"
Cohesion: 0.08
Nodes (55): assertRPCCode(), findMockagentBinary(), findProjectRoot(), findShimBinary(), intPtr(), newNotifHandler(), newServerHarness(), newSessionTestHarness() (+47 more)

### Community 2 - "ACP Types & Data Models"
Cohesion: 0.03
Nodes (44): AcpAgent, AcpProcess, AcpSession, AgentRoot, CommandEvent, Config, ErrorEvent, Event (+36 more)

### Community 3 - "Architecture Design Docs"
Cohesion: 0.05
Nodes (58): agent-shim Component Description, agent-shim Blast Radius Isolation, agent-shim Dual Role: ACP Client + Runtime Server, agentd Runtime Realization Daemon, agentd: Process Manager Subsystem, agentd: Realized Room Manager Subsystem, agentd: Workspace Manager Subsystem, ARI - Agent Runtime Interface Spec (+50 more)

### Community 4 - "Agent Protocol Research"
Cohesion: 0.05
Nodes (53): A2A Protocol (Agent2Agent), A2A Protocol Stack Position (Agent-Agent Layer), ACP Capability Negotiation (initialize), ACP Content Blocks (MCP-compatible), ACP Extensibility (_meta and Extension Methods), ACP Permission Model (request_permission), ACP Prompt Turn Lifecycle, ACP Protocol Detailed Specification (+45 more)

### Community 5 - "Process & Recovery Tests"
Cohesion: 0.07
Nodes (28): findMockagentBinary(), findProjectRoot(), findShimBinary(), TestProcessManagerStart(), createRecoveryTestSession(), setupRecoveryTest(), TestRecoverSessions_DeadShim(), TestRecoverSessions_LiveShim() (+20 more)

### Community 6 - "Session Meta Store"
Cohesion: 0.08
Nodes (27): contains(), containsIgnoreCase(), ErrDeleteProtected, ErrInvalidTransition, getClient(), handleError(), isFKViolation(), isValidTransition() (+19 more)

### Community 7 - "Shim Client Tests"
Cohesion: 0.12
Nodes (24): mockShimHandler, mockShimServer, newMockShimServer(), shimNotif, TestShimClientCancel(), TestShimClientClose(), TestShimClientConcurrentCalls(), TestShimClientDial() (+16 more)

### Community 8 - "Event Log System"
Cohesion: 0.09
Nodes (20): countLines(), EventLog, OpenEventLog(), ReadEventLog(), client, dial(), main(), mockAgent (+12 more)

### Community 9 - "Shim RPC Server"
Cohesion: 0.14
Nodes (13): connHandler, New(), replyError(), RuntimeHistoryParams, RuntimeHistoryResult, RuntimeStatusRecovery, RuntimeStatusResult, Server (+5 more)

### Community 10 - "Session Tests"
Cohesion: 0.11
Nodes (26): boolPtr(), cleanupTestWorkspace(), containsFKError(), containsString(), createTestSession(), createTestWorkspace(), newTestMetaStore(), newTestSessionManager() (+18 more)

### Community 11 - "Spec Parsing Tests"
Cohesion: 0.09
Nodes (2): SpecSuite, validWorkspaceSpec()

### Community 12 - "Runtime Manager"
Cohesion: 0.11
Nodes (8): convertMcpServers(), Manager, mergeEnv(), StateChange, StateChangeHook, newManager(), newTestConfig(), RuntimeSuite

### Community 13 - "ARI Registry"
Cohesion: 0.08
Nodes (5): Registry, WorkspaceMeta, isUniqueViolation(), Store, WorkspaceFilter

### Community 14 - "ACP Client Tests"
Cohesion: 0.14
Nodes (18): cleanupManager(), containsSubstring(), containsSubstringHelper(), newTestManager(), TestAcpClient_ReadTextFile_ApproveAll(), TestAcpClient_ReadTextFile_ApproveReads(), TestAcpClient_ReadTextFile_DenyAll(), TestAcpClient_ReadTextFile_FileReadError() (+10 more)

### Community 15 - "Terminal Tests"
Cohesion: 0.12
Nodes (21): newTestTerminalManager(), TestTerminalManager_ConcurrentTerminals(), TestTerminalManager_Create_CustomEnv(), TestTerminalManager_Create_DefaultOutputByteLimit(), TestTerminalManager_Create_ExitCodeNonZero(), TestTerminalManager_Create_OutputByteLimit(), TestTerminalManager_Create_PermissionDenied(), TestTerminalManager_Create_Success() (+13 more)

### Community 16 - "Shim Client"
Cohesion: 0.1
Nodes (14): clientHandler, Dial(), dialInternal(), DialWithHandler(), NotificationHandler, RuntimeHistoryParams, RuntimeHistoryResult, RuntimeStatusRecovery (+6 more)

### Community 17 - "Config Tests"
Cohesion: 0.15
Nodes (3): ConfigSuite, validConfig(), writeConfigFile()

### Community 18 - "Process Manager"
Cohesion: 0.13
Nodes (3): EventHandler, ProcessManager, ShimProcess

### Community 19 - "ACP Client"
Cohesion: 0.11
Nodes (8): acpClient, Client, convertEnvVariables(), readFile(), rpcError, rpcRequest, rpcResponse, writeFile()

### Community 20 - "Event Translator Tests"
Cohesion: 0.25
Nodes (17): drainEnvelope(), makeNotif(), sessionPayload(), TestFanOut_ThreeSubscribers(), TestNotifyStateChange(), TestNotifyTurnStartAndEnd(), TestSubscribeFromSeq_BackfillAndLive(), TestSubscribeFromSeq_EmptyLog() (+9 more)

### Community 21 - "Event Envelope"
Cohesion: 0.12
Nodes (9): decodeEventPayload(), Envelope, NewSessionUpdateEnvelope(), newTypedEvent(), RuntimeStateChangeParams, SequenceMeta, sequenceParams, SessionUpdateParams (+1 more)

### Community 22 - "Event Translator"
Cohesion: 0.17
Nodes (4): safeBlockText(), safeStatus(), translate(), Translator

### Community 23 - "Terminal Manager"
Cohesion: 0.19
Nodes (5): min(), Terminal, TerminalManager, TerminalState, truncatingWriter

### Community 24 - "State Persistence Tests"
Cohesion: 0.21
Nodes (2): sampleState(), StateSuite

### Community 25 - "Metadata Store"
Cohesion: 0.26
Nodes (6): buildDSN(), isBenignSchemaError(), NewStore(), splitStatements(), Store, truncate()

### Community 26 - "Event Log Tests"
Cohesion: 0.27
Nodes (8): TestEventLog_AppendAfterDamagedTail(), TestEventLog_AppendAndRead(), TestEventLog_FromSeq(), TestEventLog_SeqContinuesAfterReopen(), TestReadEventLog_DamagedTailReturnsPartial(), TestReadEventLog_DamagedTailTolerated(), TestReadEventLog_MidFileCorruptionFails(), testTime()

### Community 27 - "Git Workspace Handler"
Cohesion: 0.25
Nodes (6): buildCloneArgs(), getExitCode(), GitError, GitHandler, isCommitSHA(), isHexChar()

### Community 28 - "Workspace Meta Tests"
Cohesion: 0.18
Nodes (0): 

### Community 29 - "Config Parsing"
Cohesion: 0.22
Nodes (7): CapabilitiesConfig, Config, parseMajor(), RuntimeClassConfig, RuntimeConfig, SessionPolicyConfig, ValidateConfig()

### Community 30 - "Room Models"
Cohesion: 0.2
Nodes (7): CommunicationMode, Room, Session, SessionState, Workspace, WorkspaceRef, WorkspaceStatus

### Community 31 - "Room Tests"
Cohesion: 0.2
Nodes (0): 

### Community 32 - "Workspace Manager"
Cohesion: 0.33
Nodes (2): isManaged(), WorkspaceManager

### Community 33 - "Restart Tests"
Cohesion: 0.5
Nodes (7): countEvents(), readEventSeqs(), startAgentd(), stopAgentd(), TestAgentdRestartRecovery(), verifyNoSeqGaps(), waitForSessionState()

### Community 34 - "Workspace Hooks"
Cohesion: 0.29
Nodes (2): HookError, HookExecutor

### Community 35 - "State Persistence"
Cohesion: 0.29
Nodes (0): 

### Community 36 - "RuntimeClass Tests"
Cohesion: 0.29
Nodes (0): 

### Community 37 - "RuntimeClass Registry"
Cohesion: 0.29
Nodes (3): Capabilities, RuntimeClass, RuntimeClassRegistry

### Community 38 - "Store Tests"
Cohesion: 0.33
Nodes (0): 

### Community 39 - "Shim RPC Recovery Design"
Cohesion: 0.33
Nodes (6): Shim RPC Recovery/Reconnect Semantics, Shim RPC runtime/history Method, Shim RPC runtime/status Method, Shim RPC session/subscribe Method, DEC-006: Atomic Event Recovery Decision, DES-006: Event Recovery Race Condition

### Community 40 - "Real CLI Integration Tests"
Cohesion: 0.8
Nodes (4): runRealCLILifecycle(), setupAgentdTestWithRuntimeClass(), TestRealCLI_ClaudeCode(), TestRealCLI_GsdPi()

### Community 41 - "State Types"
Cohesion: 0.4
Nodes (3): LastTurn, State, Status

### Community 42 - "Room Store"
Cohesion: 0.4
Nodes (1): Store

### Community 43 - "Recovery Posture"
Cohesion: 0.4
Nodes (3): RecoveryInfo, RecoveryOutcome, RecoveryPhase

### Community 44 - "Lifecycle State Machines"
Cohesion: 0.4
Nodes (5): A2A Task State Machine, containerd Container Lifecycle (7 Steps), Session/Process Separation Pattern, OCI Container Lifecycle, runc Container Creation Flow (Dual-Stage Init)

### Community 45 - "EmptyDir Handler"
Cohesion: 0.5
Nodes (1): EmptyDirHandler

### Community 46 - "Local Handler"
Cohesion: 0.5
Nodes (1): LocalHandler

### Community 47 - "Recovery Process Manager"
Cohesion: 0.83
Nodes (1): ProcessManager

### Community 48 - "SQLite Backend Decisions"
Cohesion: 0.5
Nodes (4): SQLite WAL Journal Mode Pattern, D009: SQLite as Metadata Backend, D032: Session Recovery Config Persistence, Rationale: Retain SQLite Over BoltDB

### Community 49 - "Bootstrap Contract"
Cohesion: 0.5
Nodes (4): agentd: Bootstrap Contract, Bootstrap Contract (Convergence), Rationale: Bootstrap Phases, Runtime Spec: Lifecycle State Machine

### Community 50 - "Daemon CLI"
Cohesion: 0.67
Nodes (0): 

### Community 51 - "E2E Tests"
Cohesion: 1.0
Nodes (2): TestEndToEndPipeline(), waitForSocket()

### Community 52 - "Concurrent Tests"
Cohesion: 0.67
Nodes (0): 

### Community 53 - "Integration Tests"
Cohesion: 0.67
Nodes (0): 

### Community 54 - "Server Internal Tests"
Cohesion: 0.67
Nodes (0): 

### Community 55 - "Bootstrap Identity Decisions"
Cohesion: 0.67
Nodes (3): D018: Runtime Bootstrap and Identity Contract, D020: Shim Runtime Control Surface Split, Rationale: session/new as Config-Only Bootstrap

### Community 56 - "Workspace Env & Hooks"
Cohesion: 0.67
Nodes (3): agentd: Environment Precedence Order, Workspace Hooks (setup/teardown), Host-Impact Boundary Rules

### Community 57 - "Source Handler Interface"
Cohesion: 1.0
Nodes (1): SourceHandler

### Community 58 - "Room Filter"
Cohesion: 1.0
Nodes (1): RoomFilter

### Community 59 - "OCI Image Conversion"
Cohesion: 1.0
Nodes (2): OCI Image to Runtime Bundle Conversion, OCI Filesystem Bundle (config.json + rootfs)

### Community 60 - "Room & Workspace Concepts"
Cohesion: 1.0
Nodes (2): Room (Multi-Agent Collaboration Unit), Shared Workspace Reuse and Access

### Community 61 - "Typed Events Spec"
Cohesion: 1.0
Nodes (2): Runtime Spec: Typed Event Stream, Shim RPC Typed Event Surface

### Community 62 - "Config & Runtime Bundle"
Cohesion: 1.0
Nodes (2): Config Spec: agentRoot Section, Runtime Spec: Bundle Concept

### Community 63 - "Cross-Layer State Mapping"
Cohesion: 1.0
Nodes (2): Cross-Layer State Mapping, Runtime Spec: State Mapping and Identity Authority

### Community 64 - "A2A Agent Discovery"
Cohesion: 1.0
Nodes (1): A2A Agent Card Discovery

### Community 65 - "containerd Namespace"
Cohesion: 1.0
Nodes (1): containerd Namespace Multi-Tenancy

### Community 66 - "Thin Daemon Architecture"
Cohesion: 1.0
Nodes (1): containerd Thin Daemon + Smart Client Architecture

### Community 67 - "acpx Queue Owner"
Cohesion: 1.0
Nodes (1): acpx Queue Owner Architecture

### Community 68 - "acpx Flows Engine"
Cohesion: 1.0
Nodes (1): acpx Flows Workflow Engine

### Community 69 - "OCI Lifecycle Hooks"
Cohesion: 1.0
Nodes (1): OCI Lifecycle Hooks

### Community 70 - "OCI Artifacts"
Cohesion: 1.0
Nodes (1): OCI Artifacts (v1.1.0+)

### Community 71 - "Rootless Containers"
Cohesion: 1.0
Nodes (1): runc Rootless Containers

### Community 72 - "Filesystem Isolation"
Cohesion: 1.0
Nodes (1): runc pivot_root Filesystem Isolation

### Community 73 - "ACP File System Methods"
Cohesion: 1.0
Nodes (1): ACP File System Methods

### Community 74 - "ACP Config Options"
Cohesion: 1.0
Nodes (1): ACP Session Config Options

### Community 75 - "Security Boundaries"
Cohesion: 1.0
Nodes (1): Security Boundaries Definition

### Community 76 - "Shim Target Contract"
Cohesion: 1.0
Nodes (1): Shim Target Contract

### Community 77 - "Roadmap Overview"
Cohesion: 1.0
Nodes (1): OAR Development Roadmap

### Community 78 - "Phase 5 Roadmap"
Cohesion: 1.0
Nodes (1): Phase 5: Session Lifecycle Warm/Cold Pause

### Community 79 - "Phase 6 Roadmap"
Cohesion: 1.0
Nodes (1): Phase 6: Production Readiness

### Community 80 - "Workspace Source Spec"
Cohesion: 1.0
Nodes (1): Workspace Source Types (git/emptyDir/local)

### Community 81 - "Session Prompt RPC"
Cohesion: 1.0
Nodes (1): Shim RPC session/prompt Method

### Community 82 - "Runtime Stop RPC"
Cohesion: 1.0
Nodes (1): Shim RPC runtime/stop Method

### Community 83 - "Config Metadata"
Cohesion: 1.0
Nodes (1): Config Spec: metadata Section

### Community 84 - "Runtime State Spec"
Cohesion: 1.0
Nodes (1): Runtime Spec: State Model

### Community 85 - "Filesystem Layout"
Cohesion: 1.0
Nodes (1): Runtime Spec: File System Layout

### Community 86 - "Runtime Operations"
Cohesion: 1.0
Nodes (1): Runtime Spec: Operations (create/start/kill/delete)

### Community 87 - "Authority Boundary"
Cohesion: 1.0
Nodes (1): agent-shim Authority Boundary

### Community 88 - "Room Workspace Intent"
Cohesion: 1.0
Nodes (1): Room Spec: Shared Workspace Intent

### Community 89 - "Room Agents Spec"
Cohesion: 1.0
Nodes (1): Room Spec: Agent Members Definition

### Community 90 - "Room Communication"
Cohesion: 1.0
Nodes (1): Room Spec: Communication Modes (mesh/star/isolated)

### Community 91 - "agentd Session Manager"
Cohesion: 1.0
Nodes (1): agentd: Session Manager Subsystem

### Community 92 - "agentd Session New"
Cohesion: 1.0
Nodes (1): agentd: session/new Semantics

### Community 93 - "agentd Session Prompt"
Cohesion: 1.0
Nodes (1): agentd: session/prompt Semantics

### Community 94 - "Shim RPC Naming"
Cohesion: 1.0
Nodes (1): Redesign: Method Naming Convention

### Community 95 - "Shim Event Model"
Cohesion: 1.0
Nodes (1): Redesign: Event Model (session/update + runtime/update)

### Community 96 - "Unified Plan DES002"
Cohesion: 1.0
Nodes (1): DES-002: Session Metadata Model Incomplete

### Community 97 - "Unified Plan DES003"
Cohesion: 1.0
Nodes (1): DES-003: ACP Boot Sequence Not Converged

### Community 98 - "Plan Phase A"
Cohesion: 1.0
Nodes (1): Unified Plan: Phase A - Critical Bug Fix + Core Contract

### Community 99 - "Plan Phase B"
Cohesion: 1.0
Nodes (1): Unified Plan: Phase B - shim-rpc Convergence

### Community 100 - "EventLog Recovery"
Cohesion: 1.0
Nodes (1): IMP-001: EventLog Corruption Recovery

### Community 101 - "Terminal Operations"
Cohesion: 1.0
Nodes (1): IMP-003: Terminal Operations Not Implemented

### Community 102 - "Session Concept"
Cohesion: 1.0
Nodes (1): Session (OAR Runtime Object)

### Community 103 - "Bundle Concept"
Cohesion: 1.0
Nodes (1): Bundle (Agent Configuration Directory)

## Knowledge Gaps
- **208 isolated node(s):** `rpcRequest`, `rpcResponse`, `rpcError`, `sessionUpdateParams`, `sessionEvent` (+203 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **Thin community `Source Handler Interface`** (2 nodes): `handler.go`, `SourceHandler`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Room Filter`** (2 nodes): `room.go`, `RoomFilter`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `OCI Image Conversion`** (2 nodes): `OCI Image to Runtime Bundle Conversion`, `OCI Filesystem Bundle (config.json + rootfs)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Room & Workspace Concepts`** (2 nodes): `Room (Multi-Agent Collaboration Unit)`, `Shared Workspace Reuse and Access`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Typed Events Spec`** (2 nodes): `Runtime Spec: Typed Event Stream`, `Shim RPC Typed Event Surface`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Config & Runtime Bundle`** (2 nodes): `Config Spec: agentRoot Section`, `Runtime Spec: Bundle Concept`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Cross-Layer State Mapping`** (2 nodes): `Cross-Layer State Mapping`, `Runtime Spec: State Mapping and Identity Authority`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `A2A Agent Discovery`** (1 nodes): `A2A Agent Card Discovery`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `containerd Namespace`** (1 nodes): `containerd Namespace Multi-Tenancy`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Thin Daemon Architecture`** (1 nodes): `containerd Thin Daemon + Smart Client Architecture`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `acpx Queue Owner`** (1 nodes): `acpx Queue Owner Architecture`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `acpx Flows Engine`** (1 nodes): `acpx Flows Workflow Engine`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `OCI Lifecycle Hooks`** (1 nodes): `OCI Lifecycle Hooks`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `OCI Artifacts`** (1 nodes): `OCI Artifacts (v1.1.0+)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Rootless Containers`** (1 nodes): `runc Rootless Containers`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Filesystem Isolation`** (1 nodes): `runc pivot_root Filesystem Isolation`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `ACP File System Methods`** (1 nodes): `ACP File System Methods`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `ACP Config Options`** (1 nodes): `ACP Session Config Options`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Security Boundaries`** (1 nodes): `Security Boundaries Definition`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Shim Target Contract`** (1 nodes): `Shim Target Contract`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Roadmap Overview`** (1 nodes): `OAR Development Roadmap`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Phase 5 Roadmap`** (1 nodes): `Phase 5: Session Lifecycle Warm/Cold Pause`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Phase 6 Roadmap`** (1 nodes): `Phase 6: Production Readiness`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Workspace Source Spec`** (1 nodes): `Workspace Source Types (git/emptyDir/local)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Session Prompt RPC`** (1 nodes): `Shim RPC session/prompt Method`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Runtime Stop RPC`** (1 nodes): `Shim RPC runtime/stop Method`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Config Metadata`** (1 nodes): `Config Spec: metadata Section`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Runtime State Spec`** (1 nodes): `Runtime Spec: State Model`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Filesystem Layout`** (1 nodes): `Runtime Spec: File System Layout`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Runtime Operations`** (1 nodes): `Runtime Spec: Operations (create/start/kill/delete)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Authority Boundary`** (1 nodes): `agent-shim Authority Boundary`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Room Workspace Intent`** (1 nodes): `Room Spec: Shared Workspace Intent`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Room Agents Spec`** (1 nodes): `Room Spec: Agent Members Definition`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Room Communication`** (1 nodes): `Room Spec: Communication Modes (mesh/star/isolated)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `agentd Session Manager`** (1 nodes): `agentd: Session Manager Subsystem`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `agentd Session New`** (1 nodes): `agentd: session/new Semantics`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `agentd Session Prompt`** (1 nodes): `agentd: session/prompt Semantics`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Shim RPC Naming`** (1 nodes): `Redesign: Method Naming Convention`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Shim Event Model`** (1 nodes): `Redesign: Event Model (session/update + runtime/update)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Unified Plan DES002`** (1 nodes): `DES-002: Session Metadata Model Incomplete`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Unified Plan DES003`** (1 nodes): `DES-003: ACP Boot Sequence Not Converged`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Plan Phase A`** (1 nodes): `Unified Plan: Phase A - Critical Bug Fix + Core Contract`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Plan Phase B`** (1 nodes): `Unified Plan: Phase B - shim-rpc Convergence`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `EventLog Recovery`** (1 nodes): `IMP-001: EventLog Corruption Recovery`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Terminal Operations`** (1 nodes): `IMP-003: Terminal Operations Not Implemented`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Session Concept`** (1 nodes): `Session (OAR Runtime Object)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.
- **Thin community `Bundle Concept`** (1 nodes): `Bundle (Agent Configuration Directory)`
  Too small to be a meaningful cluster - may be noise or needs more connections extracted.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `agentd Runtime Realization Daemon` connect `Architecture Design Docs` to `Event Log System`, `Shim RPC Server`, `Shim RPC Server Tests`?**
  _High betweenness centrality (0.059) - this node is a cross-community bridge._
- **Are the 26 inferred relationships involving `newTestHarness()` (e.g. with `TestARIWorkspacePrepareEmptyDir()` and `TestARIWorkspacePrepareGit()`) actually correct?**
  _`newTestHarness()` has 26 INFERRED edges - model-reasoned connections that need verification._
- **Are the 22 inferred relationships involving `newMockShimServer()` (e.g. with `.serve()` and `.close()`) actually correct?**
  _`newMockShimServer()` has 22 INFERRED edges - model-reasoned connections that need verification._
- **Are the 20 inferred relationships involving `newTestTerminalManager()` (e.g. with `TestTerminalManager_Create_Success()` and `TestTerminalManager_Create_PermissionDenied()`) actually correct?**
  _`newTestTerminalManager()` has 20 INFERRED edges - model-reasoned connections that need verification._
- **What connects `rpcRequest`, `rpcResponse`, `rpcError` to the rest of the system?**
  _208 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Workspace Handlers & Tests` be split into smaller, more focused modules?**
  _Cohesion score 0.03 - nodes in this community are weakly interconnected._
- **Should `Shim RPC Server Tests` be split into smaller, more focused modules?**
  _Cohesion score 0.08 - nodes in this community are weakly interconnected._