export interface OutputTarget { source: string; threadId: string }
export interface OutputAudit extends OutputTarget {
  consumer: string; readAt: string; outcome?: string; reference?: string; confirmedAt?: string;
}
export interface OutputRecord {
  id: string; sequence: number; projectId: string; category: 'reply' | 'fact' | 'other'; kind: string;
  triggerId?: string; ticketId?: string; factId?: string; summary: string; evidence: string[];
  targets: OutputTarget[]; recordedAt: string; audit: OutputAudit[];
}
export const outputKindLabels: Record<string, string> = {
  'reply.accepted': '显式回复 · 已接单', 'reply.clarification': '显式回复 · 需要澄清',
  'reply.no_execution': '显式回复 · 无须执行', 'reply.summary': '显式回复 · 结果摘要',
}
