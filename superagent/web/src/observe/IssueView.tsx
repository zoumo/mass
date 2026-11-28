import CopyLink from './CopyLink'
import { useState, type ReactNode } from 'react'
import { AlertTriangle, ChevronRight, CircleDot, Clock3 } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, eventStatusLabel, stateLabel, type WorkbenchRow } from '../workbench'
import ExecutionProfileFields from './ExecutionProfileFields'
import ObjectWorkspace from './ObjectWorkspace'
import PerspectiveFlow, { type FlowLane, type FlowNode } from './PerspectiveFlow'
import { observerHome, sessionChatHref, usePages, useRecord, viewHref, type ViewProps } from './observer'
import { ObjectAvatar } from './ConversationCard'
import StructuredContent, { type StructuredBody } from './StructuredContent'
import { TraceLinks, type TraceAssociation } from './TraceLinks'
import './issue.css'

type Report = {
  id: string; sessionBindingId: string; createdAt: string; current: boolean
  traceAssociations?: TraceAssociation[]
  summary: string; details?: string; evidence?: string[]; questions?: string[]; goalRevision?: number
}
type InternalEvent = { traceAssociations?: TraceAssociation[]; event_id: string; createdAt: string; status: string; kind?: string; revision?: number; goalUpdate?: boolean; recipient?: { kind?: string; session_binding_id?: string } }
type Issue = WorkbenchRow & {
  id: string; title: string; topicId: string; createdAt: string
  lifecycleTraces?: Record<string, TraceAssociation[]>
  latestRequirementsNotification?: InternalEvent
  resolvedBlockers?: { id: string; reason: string; resolution?: { at: string; reason: string; messageId?: string } }[]
}
type SessionBinding = WorkbenchRow & { id: string; createdAt: string; startEventId?: string; startGoalRevision?: number; state: string }
type Event = { id: string; at: string; title: string; detail?: ReactNode; traces?: TraceAssociation[] }

function recordLink(view: string, id: string) {
  return observerHome + '?' + new URLSearchParams({ view, id, mode: 'records' })
}
function Evidence({ items }: { items?: string[] }) {
  return items?.length ? <ul>{items.map((item, i) => <li key={i}>{item}</li>)}</ul> : null
}
function InstructionContent({ eventId }: { eventId: string }) {
  const event = useRecord<{ content?: StructuredBody; body?: { instruction?: string } }>('internal-events/' + encodeURIComponent(eventId))
  if (event.error) return <p role="alert" className="read-error">指令读取失败 · {event.error}</p>
  if (!event.data) return <p>正在读取指令…</p>
  if (event.data.content) return <StructuredContent content={event.data.content}/>
  return event.data.body?.instruction ? <p>{event.data.body.instruction}</p> : null
}

function sessionLabel(issue: Issue, sessionBindingId: string) {
  return sessionBindingId === issue.currentSessionBindingId ? '当前会话' : '历史会话'
}

function collectEvents(issue: Issue, sessionBindings: SessionBinding[], reports: Report[], internalEvents: InternalEvent[]): Event[] {
  const events: Event[] = [{ id: 'created', at: issue.createdAt, title: 'Topic 创建 Issue', traces: issue.traceAssociations }]
  for (const binding of sessionBindings) {
    events.push({ id: binding.id, at: binding.createdAt, traces: binding.traceAssociations, title: '启动 Issue Session', detail: <>
      <p>{sessionLabel(issue, binding.id)} · {stateLabel(binding.state)}{binding.startGoalRevision ? ' · 启动时目标版本 ' + binding.startGoalRevision : ''}</p>
      {binding.startup && <p>启动事件 · {eventStatusLabel(binding.startup.status)}</p>}
      {binding.startEventId && <>
        <InstructionContent eventId={binding.startEventId}/>
        <a href={recordLink('internal-events', binding.startEventId)}>查看启动事件</a>
      </>}
      <a href={recordLink('session-bindings', binding.id)}>查看 SessionBinding</a>
    </> })
  }
  for (const report of reports) {
    events.push({ id: report.id, at: report.createdAt, traces: report.traceAssociations, title: 'Issue Session 提交报告 · ' + report.summary, detail: <>
      <p>{report.current ? '当前报告' : '历史报告'}{report.goalRevision ? ' · 目标版本 ' + report.goalRevision : ''}</p>
      {report.details && <p>{report.details}</p>}<Evidence items={report.evidence}/><Evidence items={report.questions}/>
      <a href={recordLink('reports', report.id)}>查看报告记录</a>
      <a href={recordLink('session-bindings', report.sessionBindingId)}>查看提交报告的会话</a>
    </> })
  }
  const instructionEvents = internalEvents.filter(event => event.recipient?.kind === 'issue' &&
    (event.kind === 'issue_session_update' || event.kind === 'context_supplement'))
  const updates = new Map(instructionEvents.map(event => [event.event_id, event]))
  const recent = issue.latestRequirementsNotification
  if (recent?.event_id && !updates.has(recent.event_id)) updates.set(recent.event_id, { ...recent, goalUpdate: true })
  for (const event of updates.values()) {
    const title = event.goalUpdate ? 'Topic 发出目标修订通知' : event.kind === 'context_supplement' ? '补充 Issue Session 上下文' : 'Topic 向 Issue Session 发送指令'
    const sessionBindingId = event.recipient?.session_binding_id
    if (event.createdAt) events.push({ id: event.event_id, at: event.createdAt, traces: event.traceAssociations, title,
      detail: <><p>{event.revision ? `目标版本 ${event.revision} · ` : ''}{eventStatusLabel(event.status)}</p>
        <InstructionContent eventId={event.event_id}/>
        <a href={recordLink('internal-events', event.event_id)}>查看投递事件</a>
        {sessionBindingId && <a href={recordLink('session-bindings', sessionBindingId)}>查看接收指令的会话</a>}</> })
  }
  for (const b of issue.resolvedBlockers || []) {
    const r = b.resolution
    if (r) events.push({ id: b.id, at: r.at, title: '解除阻塞 · ' + b.reason,
      detail: <><p>{r.reason}</p>{r.messageId && <a href={viewHref('message', r.messageId)}>查看解除依据</a>}</> })
  }
  if (issue.cancellation) events.push({ id: 'cancel', at: issue.cancellation.at, traces: issue.lifecycleTraces?.cancellation, title: 'Topic 取消 Issue', detail: issue.cancellation.reason })
  if (issue.reopening) events.push({ id: 'reopen', at: issue.reopening.at, traces: issue.lifecycleTraces?.reopening, title: 'Topic 重开 Issue', detail: issue.reopening.reason })
  if (issue.completion) events.push({ id: 'accepted', at: issue.completion.completedAt, traces: issue.lifecycleTraces?.completion, title: 'Topic 已验收',
    detail: <><p>{issue.completion.reason}</p><Evidence items={issue.completion.evidence}/></> })
  return events.sort((a, b) => b.at.localeCompare(a.at) || b.id.localeCompare(a.id))
}

function IssueFlow({ issue, topicTitle, sessionBindings, reports }: { issue: Issue; topicTitle?: string; sessionBindings: SessionBinding[]; reports: Report[] }) {
  const current: FlowNode = { kind: 'issue', label: issue.title || issue.id }
  const topic: FlowNode = { kind: 'topic', label: topicTitle || issue.topicId, href: viewHref('topic', issue.topicId) }
  const lanes: FlowLane[] = [{
    id: issue.id, nodes: [topic, current, { kind: 'state', label: stateLabel(issue.status) }],
    edges: ['创建', '当前状态'], detail: <>{issue.goal}<p>创建于 {formatTime(issue.createdAt)}</p>
      {issue.blockers?.map(b => <p key={b.id}>{b.reason}</p>)}</>,
  }]
  const sessionNode = (id: string): FlowNode => ({ kind: 'session', label: sessionLabel(issue, id), href: recordLink('session-bindings', id) })
  for (const binding of sessionBindings) {
    if (!reports.some(report => report.sessionBindingId === binding.id)) {
      lanes.push({ id: binding.id, nodes: [current, sessionNode(binding.id)], edges: ['启动'],
        detail: <p>启动于 {formatTime(binding.createdAt)} · {eventStatusLabel(binding.startup?.status)}</p> })
    }
  }
  for (const report of reports) {
    lanes.push({ id: report.id, nodes: [current, sessionNode(report.sessionBindingId), { kind: 'report', label: report.summary, href: recordLink('reports', report.id) }],
      edges: ['启动', 'Issue Session 提交'], detail: <p>{formatTime(report.createdAt)} · {report.details || report.summary}</p> })
  }
  if (issue.completion) lanes.splice(1, 0, { id: 'accepted', nodes: [topic, current], edges: ['业务验收'],
    detail: <><p>{issue.completion.reason}</p><Evidence items={issue.completion.evidence}/></> })
  for (const [index, material] of (issue.materials || []).entries()) {
    if (material.messageId) lanes.push({ id: 'material-' + index, nodes: [current, {
      kind: 'message', label: material.label || material.messageId, href: viewHref('message', material.messageId),
    }], edges: ['材料引用'], detail: <p>{material.messageId}</p> })
  }
  return <PerspectiveFlow title="Issue 进展流程" lanes={lanes}/>
}

function IssueDetail({ id, scope }: { id: string; scope: string }) {
  const record = useRecord<Issue>('issues/' + encodeURIComponent(id) + '?trace=true')
  const issue = record.data
  const topic = useRecord<WorkbenchRow>(issue?.topicId ? 'topics/' + encodeURIComponent(issue.topicId) : '')
  const params = new URLSearchParams({ issueId: id, order: 'newest', limit: '20', trace: 'true' })
  const sessionBindings = usePages<SessionBinding>('session-bindings?' + params + '&preview=true')
  const reports = usePages<Report>('reports?' + params)
  const internalEvents = usePages<InternalEvent>('internal-events?' + new URLSearchParams({ issueId: id, order: 'newest', preview: 'true', limit: '20', trace: 'true' }))
  const [expanded, setExpanded] = useState('')
  if (!issue) return <p className="panel empty" role={record.error ? 'alert' : 'status'}>{record.error || '正在读取 Issue…'}</p>
  const events = collectEvents(issue, sessionBindings.data || [], reports.data || [], internalEvents.data || [])
  const latest = events[0]
  return <article aria-label="当前 Issue" className="issue-detail">
    {record.error && <p role="alert" className="read-error">{record.error} · 快照已过时</p>}
    <section className="panel issue-overview">
      <div className="source-line"><ObjectAvatar kind="issue" label="Issue" href={sessionChatHref('issue', id)} title="与当前 Worker Session 对话"/>
        <span className="issue-status"><CircleDot size={14}/>{stateLabel(issue.status)}</span>
        <a href={viewHref('topic', issue.topicId)}>{topic.data?.title || issue.topicId}<ChevronRight size={14}/></a></div>
      <div className="observable-actions"><CopyLink href={viewHref('issue',id,{topicId:scope})}/></div>
      <h2>{issue.title}</h2><p className="issue-goal">{issue.goal || '目标未记录'}</p>
      {issue.currentSessionBindingId && <div className="issue-links">
        <a href={recordLink('session-bindings', issue.currentSessionBindingId)}>查看当前会话 →</a>
        {issue.issueSession && <span>{stateLabel(issue.issueSession.state)}</span>}
      </div>}
      <div className="issue-latest"><Clock3 size={15}/><span>最近进展</span>
        <strong>{latest?.title}</strong><time dateTime={latest?.at}>{formatTime(latest?.at)}</time></div>
      {!!issue.blockers?.length && <div className="issue-blockers"><h3><AlertTriangle size={15}/>当前阻塞</h3>
        <ul>{issue.blockers.map(b => <li key={b.id}><strong>{b.kind === 'human' ? '等待人工处理' : '依赖 Issue'}</strong> · {b.reason}
          {b.dependencyIssueId && <a href={viewHref('issue', b.dependencyIssueId)}>查看依赖 Issue</a>}</li>)}</ul></div>}
    </section>
    {issue.executionProfile && <section className="panel issue-profile" aria-labelledby="issue-profile">
      <div className="panel-heading"><h2 id="issue-profile">执行配置</h2></div>
      <dl><ExecutionProfileFields profile={issue.executionProfile}/></dl>
    </section>}
    <IssueFlow issue={issue} topicTitle={topic.data?.title} sessionBindings={sessionBindings.data || []} reports={reports.data || []}/>
    <section className="panel"><div className="panel-heading"><h2>进展记录</h2><span>目标版本 {issue.goalRevision ?? '未记录'}</span></div>
      {sessionBindings.error && <p role="alert" className="read-error">会话记录读取失败 · {sessionBindings.error}</p>}
      {reports.error && <p role="alert" className="read-error">报告读取失败 · {reports.error}</p>}
      {(!sessionBindings.data && !sessionBindings.error || !reports.data && !reports.error) && <p className="empty">正在读取进展…</p>}
      <ol className="issue-events">{events.map(e => <li key={e.id}>
        <time dateTime={e.at} title={e.at}>{formatTime(e.at)}</time>
        {e.detail ? <><button type="button" aria-expanded={expanded === e.id} onClick={() => setExpanded(expanded === e.id ? '' : e.id)}>
          {e.title}<ChevronRight size={15}/></button>{expanded === e.id && <div className="issue-event-detail">{e.detail}</div>}</> : <strong>{e.title}</strong>}
        <div><TraceLinks associations={e.traces} at={e.at}/></div>
      </li>)}</ol>
      {internalEvents.error && <p role="alert" className="read-error">指令投递记录读取失败 · {internalEvents.error}</p>}
      {internalEvents.next && <Button variant="ghost" onClick={internalEvents.more} disabled={internalEvents.loading}>加载更早事件</Button>}
      {sessionBindings.next && <Button variant="ghost" onClick={sessionBindings.more} disabled={sessionBindings.loading}>加载更早会话</Button>}
      {reports.next && <Button variant="ghost" onClick={reports.more} disabled={reports.loading}>加载更早报告</Button>}
    </section>
    <section className="panel issue-sources"><details><summary>来源材料（{issue.materials?.length || 0}）</summary>
      {issue.materials?.length ? <ul>{issue.materials.map((m, i) => <li key={i}>{m.messageId ? <a href={viewHref('message', m.messageId)}>{m.label || '查看来源 Message'}</a> : [m.label, m.uri].filter(Boolean).join(' · ')}</li>)}</ul> : <p>未记录来源材料</p>}</details>
      <div className="issue-links"><a href={recordLink('issues', issue.id)}>查看完整记录</a>
      {issue.pullRequests?.map(pr => <a key={pr.id} href={pr.canonicalUrl}>{pr.canonicalUrl}</a>)}</div>
    </section>
  </article>
}
export default function IssueView({ query }: ViewProps) {
  return <ObjectWorkspace view="issue" query={query}>{id => <IssueDetail key={id} id={id} scope={query.get('topicId') || ''}/>}</ObjectWorkspace>
}
