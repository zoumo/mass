import "../../shared/models.tsp";
import "./session-config.tsp";
import "./interactions.tsp";

namespace MASS.Design;

enum SessionEventType {
  userMessage: "user.message",
  agentMessage: "agent.message",
  agentThinking: "agent.thinking",
  agentToolCall: "agent.tool_call",
  agentToolResult: "agent.tool_result",
  agentMessageChunk: "agent.message_chunk",
  agentThinkingChunk: "agent.thinking_chunk",
  agentToolCallChunk: "agent.tool_call_chunk",
  agentPlan: "agent.plan",
  sessionStateUpdate: "session.state_update",
  sessionError: "session.error",
  sessionInfoUpdate: "session.info_update",
  sessionConfigOptionUpdate: "session.config_option_update",
  sessionAvailableCommandsUpdate: "session.available_commands_update",
  sessionUsageUpdate: "session.usage_update",
  interactionRequested: "session.interaction_requested",
  interactionClosed: "session.interaction_closed",
}

/** 实时、持久存储与历史读取共用的信封；adapter 根据打开或恢复时固定的名称补齐后交付，不要求补查 MetaStore。 */
model SessionEventBase<Type extends SessionEventType = SessionEventType> {
  /** adapter 创建统一事件时生成一次 UUID；同一事件发布、持久保存与重试保持不变。
   * 仅用于唯一标识，不表示顺序，不替代分页游标；暂不做原生事件去重。
   * 不承诺重新接收同一原生通知时恢复为相同 UUID。 */
  @minLength(1)
  @format("uuid")
  eventId: string;

  @minLength(1)
  `namespace`: string;

  @minLength(1)
  sessionName: string;

  sessionId: string;
  type: Type;

  /** adapter 优先使用原始事件时间；缺失时由 adapter 生成，Service/Store 不改写。 */
  time: utcDateTime;

  /** 条目事件提供；预览与完整事实相同，工具调用与结果共享条目身份。 */
  itemId?: string;

  /** 扩展 map：字符串键、任意类型值，对应 Go map[string]any。
   * unknown 在此表示明确允许的任意扩展值，不是待决占位。 */
  _meta?: Record<unknown>;
}

/** 复用公共信封，但每个联合分支固定 type 与 payload；不再允许任意配对。 */
model SessionEventOf<Type extends SessionEventType, Payload extends {}>
  extends SessionEventBase<Type> {
  payload: Payload;
}

/** 消息、工具与计划均需条目身份；会话事件不虚构 itemId。 */
model ItemSessionEventOf<
  Type extends SessionEventType,
  Payload extends {}
> extends SessionEventOf<Type, Payload> {
  itemId: string;
}

union SessionEvent {
  userMessage: ItemSessionEventOf<
    SessionEventType.userMessage,
    MessageEventPayload
  >,
  agentMessage: ItemSessionEventOf<
    SessionEventType.agentMessage,
    MessageEventPayload
  >,
  agentThinking: ItemSessionEventOf<
    SessionEventType.agentThinking,
    MessageEventPayload
  >,
  agentMessageChunk: ItemSessionEventOf<
    SessionEventType.agentMessageChunk,
    MessageEventPayload
  >,
  agentThinkingChunk: ItemSessionEventOf<
    SessionEventType.agentThinkingChunk,
    MessageEventPayload
  >,
  agentToolCall: ItemSessionEventOf<
    SessionEventType.agentToolCall,
    ToolCallEventPayload
  >,
  agentToolResult: ItemSessionEventOf<
    SessionEventType.agentToolResult,
    ToolResultEventPayload
  >,
  agentToolCallChunk: ItemSessionEventOf<
    SessionEventType.agentToolCallChunk,
    ToolChunkEventPayload
  >,
  agentPlan: ItemSessionEventOf<SessionEventType.agentPlan, PlanEventPayload>,
  sessionStateUpdate: SessionEventOf<
    SessionEventType.sessionStateUpdate,
    SessionStateEventPayload
  >,
  sessionError: SessionEventOf<
    SessionEventType.sessionError,
    SessionErrorEventPayload
  >,
  sessionInfoUpdate: SessionEventOf<
    SessionEventType.sessionInfoUpdate,
    SessionInfoEventPayload
  >,
  sessionConfigOptionUpdate: SessionEventOf<
    SessionEventType.sessionConfigOptionUpdate,
    SessionConfigEventPayload
  >,
  sessionAvailableCommandsUpdate: SessionEventOf<
    SessionEventType.sessionAvailableCommandsUpdate,
    SessionCommandsEventPayload
  >,
  sessionUsageUpdate: SessionEventOf<
    SessionEventType.sessionUsageUpdate,
    SessionUsageEventPayload
  >,
  interactionRequested: SessionEventOf<
    SessionEventType.interactionRequested,
    InteractionRequestedEventPayload
  >,
  interactionClosed: SessionEventOf<
    SessionEventType.interactionClosed,
    InteractionClosedEventPayload
  >,
}

/** message 与 thinking 共用同一消息模型，仅由事件 type 区分含义。
 * messageId/itemId 定位条目；内部多个独立 ContentBlock 按同一规则保序处理。
 * chunk 的 content 只表示本次追加；MASS 不跨事件保存或拼接 delta。 */
model MessageEventPayload {
  messageId: string;
  content: ContentBlock[];
  phase?: string;
}

alias ToolStatus =
  | "pending"
  | "in_progress"
  | "completed"
  | "failed"
  | "declined"
  | "unknown";

/** 调用与结果共有的可选描述和实际输出；不包装 ContentBlock。 */
model ToolEventFields {
  id: string;
  name?: string;
  title?: string;
  phase?: string;
  inputRepresentation?: string;
  rawOutput?: unknown;
  content?: ContentBlock[];
  exitCode?: integer;

  @minValue(0)
  durationMs?: numeric;

  output?: {
    availability: "provided" | "unavailable";
    completeness: "retained" | "truncated" | "unknown";
    truncation?: {
      @minValue(0)
      retainedBytes: integer;

      @minValue(0)
      omittedBytes?: integer;
    };
  };
}

model ToolCallEventPayload extends ToolEventFields {
  kind: string;
  rawInput: unknown;
  status?: ToolStatus;
}

model ToolResultEventPayload extends ToolEventFields {
  kind: string;
  status: ToolStatus;
  rawInput?: unknown;
}

/** 参数片段或工具输出片段均为追加内容；参数片段不要求已是合法 JSON。 */
model ToolChunkEventPayload {
  id: string;
  content: ContentBlock[];
}

model PlanEventPayload {
  planId: string;
  entries: {
    content: string;
    priority: "low" | "medium" | "high";
    status: "pending" | "in_progress" | "completed";
  }[];
}

/** adapter 只报告工作观察，不决定 Service 的打开、关闭或归档状态。
 * 未知用 null，不虚构 idle；共享值类型不依赖 Service 资源 DTO。 */
alias ObservedSessionState =
  | SessionState.idle
  | SessionState.running
  | SessionState.requires_action
  | null;

model SessionStateEventPayload {
  previousState: ObservedSessionState;
  state: ObservedSessionState;
  stopReason?: string;
  error?: {
    code?: integer | string;
    message: string;
  };
}

model SessionErrorEventPayload {
  message: string;
  reason?: string;
}

/** 至少提供一个观察字段。 */
union SessionInfoEventPayload {
  title: {
    title: string,
    updatedAt?: utcDateTime,
  },
  timestamp: {
    title?: string,
    updatedAt: utcDateTime,
  },
}

/** 配置通知是完整列表快照；这与消息 chunk 的追加语义不同。 */
model SessionConfigEventPayload {
  configOptions: SessionConfigOption[];
}

model SessionCommandsEventPayload {
  availableCommands: {
    name: string;
    description?: string;
  }[];
}

model SessionUsageEventPayload {
  used: integer;
  size: integer;
  cost?: {
    amount: decimal;
    currency: string;
  };
}
