import { stateLabel, type WorkbenchRow } from '../../workbench'
import type { StructuredBody } from '../StructuredContent'

export type DiagnosticRow = WorkbenchRow & {
  traceAssociations?: { traceId: string; phase: string }[]
  type?: string
  canonicalUrl?: string
  lastMessageAt?: string
  subscriptionId?: string
  pullRequestId?: string
  close?: { at: string; resourceVersion?: string; method: string; reusable: boolean }
  resolvedBlockers?: {
    id: string
    reason: string
    resolution?: { reason: string; at: string; messageId?: string }
  }[]
  latestRequirementsNotification?: DiagnosticRow
  workObservation?: {
    state: string
    observedAt: string
    sessionId: string
    sessionUid: string
    workId?: string
    provider?: string
    continuous: boolean
    observationGap?: string
    end?: { eventId: string; at: string; reason: string }
  }
  reminder?: DiagnosticRow
  evidence?: string[]
  questions?: string[]
  messageId?: string
  instanceId?: string
  connection?: string
  admission?: string
  role?: string
  possibleDuplicate?: boolean
  errorCode?: string
  retryAt?: string
  receipt?: {
    taskId?: string
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
  details?: string
  eventId?: string
  sessionId?: string
  sessionUid?: string
  runtimeRef?: string
  startEventId?: string
  reminderEventId?: string
  startGoalRevision?: number
  retiredAt?: string
  previousSessionBindingId?: string
  recovery?: { status: string; mode: string; createdAt: string; completedAt?: string; lastError?: string; replacementSessionBindingId?: string }
  recipient?: { session_binding_id?: string }
  sender?: { session_binding_id?: string }
  current?: boolean
  reminderEligibility?: string
  event_id?: string
  ownerId?: string
}
const workKinds = ['issues', 'reports'] as const
export const kinds = [
  'messages',
  'threads',
  'outbox',
  'topics',
  ...workKinds,
  'session-bindings',
  'internal-events',
] as const
export type Kind = (typeof kinds)[number]
const collectionLabels: Record<Kind, string> = {
  messages: '消息',
  threads: '线程',
  outbox: '回执',
  topics: 'Topic',
  issues: 'Issue',
  reports: '报告',
  'session-bindings': '会话绑定',
  'internal-events': '内部事件',
}
export const viewLabel = (view: Kind) => collectionLabels[view]
export const reminderBasis: Record<string, string> = {
  session_binding_retired: '会话绑定已停用',
  report_received: '已收到当前报告',
  already_scheduled: '已安排一次提醒',
  awaiting_correlated_end: '等待 Session 空闲',
  end_unconfirmed: 'Session 空闲状态未确认',
}

export const replyLabel = (status?: string) =>
  status === 'sent' ? '已发送' : status === 'pending' ? '待发送' : stateLabel(status)
export const identity = (row: DiagnosticRow) => String(row.event_id || row.id || '')

export type Filter = 'topicId' | 'issueId' | 'messageId' | 'threadId'
export type OpenCollection = (kind: Kind, filterId?: string, filter?: Filter, id?: string) => void

export type RecordProps = { kind: Kind; detail: DiagnosticRow; navigate: OpenCollection }
