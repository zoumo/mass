import "../modules/service/main.tsp";

namespace MASS.Design.RPC;

/** MASS 方法集版本；与信封中的 JSON-RPC 2.0 分开表达。 */
alias ProtocolVersion = "1";

@minValue(0)
@maxValue(9007199254740991)
scalar NumericRequestId extends int64;

/** 首版接受字符串或非负安全整数，不接受 null 请求 ID。 */
alias RequestId = string | NumericRequestId;

model PeerInfo {
  name: string;
  version: string;
}

model Initialized {
  protocolVersion: ProtocolVersion;
  serverInfo: PeerInfo;
}

model ProtocolErrorData {
  reason: string;
}

model Error {
  code: int32;
  message: string;
  data?: MASS.Design.OperationError | ProtocolErrorData;
}

/** Params/Result 由方法声明决定，T 不是任意语言对象。 */
model Request<T> {
  jsonrpc: "2.0";
  id: RequestId;
  method: string;
  params?: T;
}

model Success<T> {
  jsonrpc: "2.0";
  id: RequestId;
  result: T;
}

/** 无法可靠关联的协议错误用 null ID；业务错误回显请求 ID。 */
model Failure {
  jsonrpc: "2.0";
  id: RequestId | null;
  error: Error;
}

model Notification<T> {
  jsonrpc: "2.0";
  method: string;
  params: T;
}

model Subscribed {
  subscriptionId: string;
}

/** 参数列表映射为命名 params 对象；RPC 错误由 Failure 表达。 */
interface Methods {
  initialize(
    protocolVersion: ProtocolVersion,
    clientInfo?: PeerInfo,
  ): Initialized;

  @parameterVisibility(Lifecycle.Create)
  `runtime.create`(input: RuntimeCreate): Runtime;
  `runtime.get`(runtimeRef: string): Runtime;
  `runtime.list`(query?: RuntimeQuery): Runtime[];
  `runtime.update`(runtimeRef: string, spec: RuntimeSpec): Runtime;
  `runtime.start`(runtimeRef: string): Runtime;
  `runtime.stop`(runtimeRef: string): Runtime;
  `runtime.delete`(runtimeRef: string): null;

  @parameterVisibility(Lifecycle.Create)
  `session.create`(input: SessionCreate): SessionCreated;
  `session.get`(session: SessionRef): Session;
  `session.list`(query?: SessionQuery): Page<Session>;
  `session.resume`(session: SessionRef, input?: SessionResume): SessionResumed;
  `session.close`(session: SessionRef): Session;
  `session.prompt`(session: SessionRef, input: SessionInput): InputReceipt;
  `session.answer`(
    session: SessionRef,
    interaction: InteractionRef,
    answer: InteractionAnswer,
  ): InteractionReceipt;
  `session.cancel`(session: SessionRef): CancelReceipt;
  `session.history`(session: SessionRef, query?: HistoryQuery): HistoryPage;

  @parameterVisibility(Lifecycle.Create)
  `session.load`(input: SessionLoad): SessionLoadAccepted;
  `session.cancelLoad`(session: SessionRef, loadId: string): HistoryLoadStatus;
  `session.subscribe`(session: SessionRef): Subscribed;
  `session.unsubscribe`(subscriptionId: string): null;
}

/** 服务端通知无 ID；没有要求客户端回执的反向业务请求。 */
interface Notifications {
  `session.event`(subscriptionId: string, event: SessionEvent): void;
  `subscription.closed`(
    subscriptionId: string,
    error?: MASS.Design.OperationError,
  ): void;
}
