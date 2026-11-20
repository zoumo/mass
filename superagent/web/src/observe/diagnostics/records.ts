import { stateLabel, routingDeliveryLabel, type WorkbenchRow } from '../../workbench'
import type { StructuredBody } from '../StructuredContent'

export type DiagnosticRow = WorkbenchRow & {
  type?: string
  canonicalUrl?: string
  lastMessageAt?: string
  subscriptionId?: string
  pullRequestId?: string
  ref?: { source: string; id: string }
  close?: { at: string; resourceVersion?: string; method: string; reusable: boolean }
  resolvedBlockers?: {
    id: string
    reason: string
    resolution?: { reason: string; at: string; messageId?: string }
  }[]
  latestRequirementsNotification?: DiagnosticRow
  followup?: DiagnosticRow
  triggerMessageId?: string
  workObservation?: {
    state: string
    observedAt: string
    sessionId: string
    sessionUid: string
    generation: number
    workId?: string
    provider?: string
    continuous: boolean
    observationGap?: string
    end?: { eventId: string; at: string; reason: string }
  }
  reminder?: DiagnosticRow
  evidence?: string[]
  questions?: string[]
  preparation?: DiagnosticRow
  startup?: DiagnosticRow
  messageId?: string
  instanceId?: string
  connection?: string
  admission?: string
  role?: string
  possibleDuplicate?: boolean
  errorCode?: string
  retryAt?: string
  channel?: {
    type: string
    conversationId: string
    messageId: string
    actorId?: string
    address?: Record<string, unknown>
  }
  receipt?: {
    taskId?: string
    conversationId?: string
    messageId?: string
    errorCode?: string
    retryAt?: string
    uncertain?: boolean
  }
  blockerRef?: { issueId: string; blockerId: string }
  quotedReply?: { outboxId: string; blockerRef: { issueId: string; blockerId: string } }
  stop?: {
    status: string
    workState: string
    requestedAt: string
    observedAt?: string
    errorCode?: string
    observationGap?: string
    receipt?: { result: string; target?: string }
    observation?: { eventId: string; state: string; at: string; stopReason?: string }
  }
  revision?: number
  sourceMessageId?: string
  state?: string
  lastError?: string
  observationGap?: string
  workspace?: string
  content?: string | StructuredBody
  preview?: string
  dueAt?: string
  firedAt?: string
  details?: string
  historical?: boolean
  eventId?: string
  sessionId?: string
  sessionUid?: string
  runtimeRef?: string
  startEventId?: string
  reminderEventId?: string
  preparationAtRetirement?: string
  reminderEligibility?: string
  delivery_id?: string
  ownerId?: string
}
const workKinds = ['issues', 'executions', 'reports', 'timers', 'pull-requests'] as const
export const kinds = [
  'messages',
  'threads',
  'outbox',
  'topics',
  ...workKinds,
  'routing-deliveries',
  'subscriptions',
  'relations',
  'bindings',
  'internal-deliveries',
] as const
export type Kind = (typeof kinds)[number]
const collectionLabels: Record<Kind, string> = {
  messages: '消息',
  threads: '线程',
  outbox: '回执',
  topics: 'Topic',
  issues: 'Issue',
  executions: '执行记录',
  reports: '报告',
  timers: '定时器',
  'pull-requests': '拉取请求',
  'routing-deliveries': '路由投递',
  subscriptions: '订阅',
  relations: '路由关系',
  bindings: '绑定',
  'internal-deliveries': '内部投递',
}
export const viewLabel = (view: Kind) => collectionLabels[view]
export const reminderBasis: Record<string, string> = {
  execution_retired: '执行已停用',
  report_received: '已收到本次执行的报告',
  already_scheduled: '已安排一次提醒',
  awaiting_correlated_end: '等待 Session 空闲',
  end_unconfirmed: 'Session 空闲状态未确认',
}

export const replyLabel = (status?: string) =>
  status === 'sent' ? '已发送' : status === 'pending' ? '待发送' : stateLabel(status)
export function followupLabel(followup?: DiagnosticRow) {
  return followup ? routingDeliveryLabel(followup.status) : '—'
}

export const identity = (row: DiagnosticRow) => String(row.id || row.key || '')

export type Filter = 'topicId' | 'issueId' | 'messageId' | 'threadKey'
export type OpenCollection = (kind: Kind, filterId?: string, filter?: Filter, id?: string) => void

export type RecordProps = { kind: Kind; detail: DiagnosticRow; navigate: OpenCollection }
