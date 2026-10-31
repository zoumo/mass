import "./observation.tsp";
import "../runtime/session-events.tsp";
import "../../shared/models.tsp";
import "../runtime/models.tsp";
import "../../shared/history-models.tsp";

namespace MASS.Design;

/** 原因字符串；各具体问题自行定义取值，不维护全局枚举或统一词表。 */
alias Reason = string;

/** 系统分配的 spec 版本，创建为 1；规范化后的 spec 改变时递增。 */
@minValue(1)
scalar Generation extends integer;

/** Kubernetes DNS subdomain 名称：小写字母、数字、连字符与点，最长 253 字符。 */
@minLength(1)
@maxLength(253)
@pattern("^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$")
scalar ResourceName extends string;

/** Kubernetes namespace 名称采用 DNS label，非空且最长 63 字符。 */
@minLength(1)
@maxLength(63)
@pattern("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
scalar NamespaceName extends string;

/** Runtime 与 Session 共用；namespace/name 创建后不可修改。 */
model ObjectMetadata {
  /** Runtime 为空；Session 必须提供非空 namespace。 */
  `namespace`: NamespaceName | "" = "";

  name: ResourceName;

  /** 系统生成，用于区分删除后同名重建的资源，不是名称唯一键。 */
  @visibility(Lifecycle.Read)
  uid: string;

  /** 资源首次持久创建的时间；更新、归档、恢复、启停均不修改。 */
  @visibility(Lifecycle.Read)
  createdAt: utcDateTime;

  /** 系统维护的本资源 spec 版本。 */
  @visibility(Lifecycle.Read)
  generation: Generation;

  /** MetaStore 分配的不透明并发版本，与 generation 独立；从元数据权威后端恢复时保留；事件 SQL 重建不改变此值。
   * 对外资源只读；MetaStore 条件写入使用读取快照中的值。 */
  @visibility(Lifecycle.Read)
  resourceVersion: string;
}

/** 两种寻址形式互斥；sessionId 在全部 Runtime 和 namespace 中唯一。 */
union SessionRef {
  byName: {
    `namespace`: NamespaceName,
    name: ResourceName,
    sessionId?: never,
  },
  byId: {
    /** 原始 ID 不得为 . 或 ..；由 Service 在查询或副作用前校验。 */
    sessionId: string,

    `namespace`?: never,
    name?: never,
  },
}

/** 类型专属配置只保存秘密引用，不返回解析后的秘密。 */
model RuntimeSpec {
  type: RuntimeType;
  config: RuntimeConfig;
}

model RuntimeCreate {
  metadata: ObjectMetadata;
  spec: RuntimeSpec;
}

model RuntimeQuery {
  /** 按期望 spec.type 筛选，不代表当前运行使用的类型。 */
  type?: RuntimeType;

  state?: RuntimeState;
}

model Runtime {
  metadata: ObjectMetadata;
  spec: RuntimeSpec;
  status: RuntimeStatus;
}

model RuntimeStatus {
  state: RuntimeState;

  /** 当前运行已成功加载的 spec 版本；未启动或确认停止后不提供。 */
  appliedGeneration?: Generation;

  /** 实际运行能力；尚未建立运行时不宣称已协商能力。 */
  capabilities?: CapabilitySet;

  reason?: Reason;
}

/** 会话运行选项来自运行时合同；资源层补充实例归属。 */
model SessionSpec extends Execution.DriverSessionSpec {
  /** 持久 Runtime 名称归属；不绑定或比较该 Runtime 的 metadata.uid。 */
  runtimeRef: string;
}

/** 创建名称须符合 ResourceName 且不得为 UUID 形式；由 SessionService 按 session.md 的创建名称校验规则在副作用前拒绝。 */
model SessionCreate {
  metadata: ObjectMetadata;
  spec: SessionSpec;
}

/** 恢复已知原生会话并加载历史到新空目标；不发送业务输入。 */
model SessionLoad {
  metadata: ObjectMetadata;
  runtimeRef: string;
  cwd: string;

  /** 待原生验证的已知 ID，不由 MASS 分配。 */
  sessionId: string;
}

model HistoryLoadStatus {
  loadId: string;
  phase:
    | "accepted"
    | "replaying"
    | "native_completed"
    | "completed"
    | "failed"
    | "canceled";
  nativeVerified: boolean;

  /** 已恢复同一个原生会话；成功后保留该运行供后续输入。 */
  nativeResumed: boolean;

  nativeCompleted: boolean;

  /** 全部提交与 publish 调用结算，不等于外部或异步动作读完。 */
  deliveryCompleted: boolean;

  /** 失败/取消结果不证明释放；清理结算后为 true。成功 load 保留运行，不要求此值为 true。 */
  cleanupCompleted: boolean;

  lastCommittedEventId?: string;
  error?: OperationError;
}

model SessionLoadAccepted {
  session: Session;
  loadId: string;
}

model SessionQuery {
  /** 省略时查询全部 namespace。 */
  `namespace`?: NamespaceName;

  runtimeRef?: string;
  state?: SessionState;
  page?: PageRequest;
}

model Session {
  metadata: ObjectMetadata;
  spec: SessionSpec;
  status: SessionStatus;
}

model SessionStatus {
  historyLoad?: HistoryLoadStatus;

  /** 跨 Runtime 全局唯一的会话标识；原始值不得为 . 或 ..；归档、离线与恢复均保留。 */
  sessionId: string;

  /** 当前可证明的状态；重启不沿用旧 idle 作为可用证明，按 Service 启动规则校正为 offline/archived/unknown。 */
  state: SessionState;

  /** 本次会话运行已应用的 Session spec 版本，不是 Runtime 版本。 */
  appliedGeneration?: Generation;

  /** 本次会话运行所绑定的 Runtime spec 版本。 */
  runtimeGeneration?: Generation;

  capabilities?: CapabilitySet;
  reason?: Reason;
}

model Page<T> {
  items: T[];
  next?: PageCursor;
}
