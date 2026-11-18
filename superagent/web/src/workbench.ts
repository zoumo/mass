export const workbenches = {
 topics: { label: 'Topic', title: 'Topic 工作台', description: '查看工作进展、路由决定和关联事项。', icon: 'pi pi-compass', list: '最近的 Topic' },
 issues: { label: 'Issue', title: 'Issue 工作台', description: '查看要求、执行记录、报告和业务验收。', icon: 'pi pi-check-square', list: '最近的 Issue' },
 messages: { label: 'Trigger 消息', title: 'Trigger 消息工作台', description: '追踪渠道消息的接收和路由过程。', icon: 'pi pi-inbox', list: '最近的消息' },
} as const

export type Workbench = keyof typeof workbenches
export const isWorkbench = (view: string): view is Workbench => Object.hasOwn(workbenches, view)

export type RoutingRelation = {
 id: string
 topicId: string
 action: string
 round: number
 reason: string
 decisionId?: string
 messageId?: string
 createdAt?: string
}

export type WorkbenchRow = {
 id?: string; key?: string; title?: string; text?: string; actor?: string; source?: string
 topicId?: string; messageId?: string; threadKey?: string; createdAt?: string; receivedAt?: string
 status?: string; kind?: string; at?: string; updatedAt?: string; summary?: string
 description?: string; acceptance?: string; addressed?: string; requirements?: string
 requirementsRevision?: number; currentExecutionId?: string; executionId?: string; bindingId?: string
 reason?: string; decisionId?: string; action?: string; generation?: number; authority?: string
 completion?: { reason: string; evidence: string[]; completedAt: string }
 cancellation?: { reason: string; at: string; bindingId: string }
 reopening?: { reason: string; at: string }
 blockers?: { id: string; kind: 'human' | 'dependency'; reason: string; dependencyIssueId?: string }[]
 materials?: { label?: string; uri?: string; messageId?: string }[]
 workerSession?: { state: string; observedAt?: string; observationGap?: string }
 preparation?: WorkbenchRow; startup?: WorkbenchRow
 targets?: { topicId: string; messageIds: string[]; create?: { title: string } }[]
 echo?: {
  status: string; resolvedAt?: string; releaseReason?: string
  match?: { outboxId: string; attemptId: string }
  candidates?: { outboxId: string; attemptId: string }[]
  deadline?: string; routingEventId?: string
 }
 round?: number; rounds?: Record<string, number>; messageIds?: string[]
 relations?: RoutingRelation[]
 routing?: { round: number; state: string }
 [key: string]: unknown
}
export type Page<T = WorkbenchRow> = { items: T[]; nextCursor?: string }
export type RelatedKind = 'issues' | 'subscriptions' | 'relations' | 'decisions' | 'outbox' | 'reports' | 'executions'
export type RelatedPages = Partial<Record<RelatedKind, Page | Error>>
export type RecordLinks = {
 href: (view: string, id?: string, filter?: string, scope?: 'topicId' | 'issueId' | 'messageId' | 'threadKey') => string
 follow: (event: MouseEvent, view: string, id?: string, filter?: string, scope?: 'topicId' | 'issueId' | 'messageId' | 'threadKey') => void
}

export function relatedQueries(view: Workbench, id: string): [RelatedKind, string][] {
 const topic = new URLSearchParams({ topicId: id, order: 'newest', limit: '6', preview: 'true' })
 const issue = new URLSearchParams({ issueId: id, order: 'newest', limit: '4', preview: 'true' })
 const message = new URLSearchParams({ messageId: id, order: 'newest', limit: '6', preview: 'true' })
 switch (view) {
  case 'topics': return ['subscriptions', 'issues', 'relations', 'decisions', 'outbox'].map(kind => [kind as RelatedKind, String(topic)])
  case 'issues': return [['executions', String(issue)], ['reports', String(issue)]]
  case 'messages': return [['decisions', String(message)], ['outbox', String(message)]]
 }
}

const dateTime = new Intl.DateTimeFormat('zh-CN', {
 month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
})
export function formatTime(at?: string) {
 const time = Date.parse(at || '')
 return Number.isFinite(time) ? dateTime.format(time) : '未记录时间'
}

const statusLabels: Record<string, string> = {
 active: '进行中', completed: '已完成', blocked: '受阻', canceled: '已取消', reopened: '已重开',
 ready: '已就绪', pending: '待处理', sent: '已送达', failed: '失败', unknown: '未知',
 held: '等待发送结果', outbound_echo: '自身回声', echo_unknown: '回声状态未知',
 cleared: '确认发送结果后已路由', released: '已人工放行',
 accepted: '已接收', ignored: '已忽略', routed: '已路由', received: '已收到', returned: '已退回',
 route: '路由', ignore: '忽略', progress: '进展', current: '当前执行', historical: '历史执行',
 dispatching: '投递中', delivered: '已投递', blocked_unknown: '原生执行结果未确认',
 transport_unknown: '传输结果未确认', superseded: '已替代', reconciling: '核对中', sending: '发送中',
 not_started: '未启动', connecting: '正在连接', connected: '已连接', disconnected: '已断开', stale: '已过期',
 unavailable: '不可用', waiting: '等待接收', buffer_full: '缓冲区已满', admission_failed: '接收失败',
 scheduled: '已安排', fired: '已触发', revoked: '已撤销', open: '待执行',
 running: '运行中', busy: '忙碌', idle: '空闲', stopped: '已停止', preparing: '准备中',
 noActiveWork: '无活跃任务', never_dispatched: '从未投递', requested: '已请求', observed: '已观测',
 router: '路由器', topic: 'Topic', worker: '执行器',
 send: '发送', query: '查询', started: '已开始', task: '已登记渠道任务', skipped: '已跳过',
 ambiguous: '结果不明', permanent_failure: '永久失败',
}
export function stateLabel(state?: string) {
 if (!state) return '未知'
 return Object.hasOwn(statusLabels, state) ? statusLabels[state]! : state
}

export function messageTitle(row: WorkbenchRow) {
 const firstLine = row.text?.trim().split('\n')[0]
 if (!firstLine) return '无正文消息'
 const subject = firstLine.split(/[。！？]|[.!?](?=\s|$)/, 1)[0]?.trim() || firstLine
 const characters = Array.from(subject)
 return characters.length > 88 ? characters.slice(0, 87).join('').trimEnd() + '…' : subject
}

export function sourceLabel(source?: string) {
 if (source === 'dws') return 'DWS'
 if (source === 'pr:url') return '拉取请求'
 return source || '未记录来源'
}

export function threadLabel(key?: string) {
 if (!key) return '线程'
 if (/^https?:\/\//i.test(key)) {
  const url = new URL(key)
  return url.host + url.pathname + url.search + url.hash
 }
 return `${sourceLabel(key.split(':')[0])} 线程`
}
