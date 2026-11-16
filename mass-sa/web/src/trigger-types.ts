export interface TriggerEvidence {
  reason: string; action?: string; decision: string; matchedTicketIds: string[]; at: string;
  actor: { kind: string; sessionId?: string };
}
export interface TriggerTicket { id: string; title: string; currentAssignmentId?: string; currentSessionId?: string }
export interface TriggerRecord {
  triggerId: string; projectId: string; source: string; sourceEventId: string; threadId: string;
  kind: string; summary: string; replyCount: number; acceptedAt: string; decision: string; routes: string[];
  deliveryStates: string[]; tickets: TriggerTicket[]; historyRecorded: boolean; entryReason: string;
}
export interface TriggerBatch {
  attempted: boolean; targetResolution: string;
  id: string; state: string; executionId?: string; assignmentId?: string; sessionId?: string;
  createdAt: string; updatedAt: string; deliveredAt?: string; errorReason?: string;
  inputs: { inboxId: string; kind: string; triggerId?: string; ticketId?: string }[];
}
export interface TriggerDelivery {
  inboxId: string; ticketId?: string; route: string; state: string; acceptedAt: string; updatedAt: string; deliveredAt?: string;
  attempted: boolean; targetResolution: string; assignmentId?: string; sessionId?: string; executionId?: string;
  errorReason?: string; batches: TriggerBatch[];
}
export interface TriggerDetail extends TriggerRecord {
  content: string; replyRequested: boolean; echoOfOutputId?: string; errorReason?: string;
  admission?: TriggerEvidence; decisions: TriggerEvidence[]; deliveries: TriggerDelivery[]; observedAt: string; sourceKind: string;
}
export const reasonLabels: Record<string, string> = {
  no_association: '无关联，交 Router', ambiguous_association: '关联歧义，交 Router', unique_association: '唯一关联',
  association_unavailable: '关联工作不可用，交 Router', reply_requested: 'replyRequested，交 Router',
  proven_output_echo: '已证实输出回流，忽略', history_not_recorded: '历史依据未记录',
  explicit_decision: '显式业务决定', creation_result: '创建结果',
}
export const routeLabels: Record<string, string> = { router: '经 Router', direct: 'Worker 输入', ignored: '忽略', unknown: '路径未知' }
