import CopyLink from './CopyLink'
import { useState, type ReactNode } from 'react'
import { AlertTriangle, ChevronRight, CircleDot, Clock3 } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, eventStatusLabel, stateLabel, type WorkbenchRow } from '../workbench'
import ExecutionProfileFields from './ExecutionProfileFields'
import ObjectWorkspace from './ObjectWorkspace'
import PerspectiveFlow, { type FlowLane, type FlowNode } from './PerspectiveFlow'
import { usePages, useRecord, viewHref, type ViewProps } from './observer'
import { TraceLinks, type TraceAssociation } from './TraceLinks'
import { metricsHref } from './MetricsView'
import './issue.css'

type Report = {
  id: string; executionId: string; generation: number; createdAt: string
  traceAssociations?: TraceAssociation[]
  summary: string; details?: string; evidence?: string[]; questions?: string[]; goalRevision?: number
}
type InternalEvent = { traceAssociations?: TraceAssociation[]; event_id: string; createdAt: string; status: string; revision?: number; goalUpdate?: boolean; recipient?: { execution_id?: string } }
type Issue = WorkbenchRow & {
  id: string; title: string; topicId: string; createdAt: string
  lifecycleTraces?: Record<string, TraceAssociation[]>
  reports?: Report[]; latestRequirementsNotification?: InternalEvent
  resolvedBlockers?: { id: string; reason: string; resolution?: { at: string; reason: string; messageId?: string } }[]
}
type Execution = WorkbenchRow & { id: string; createdAt: string; startEventId?: string }
type Event = { id: string; at: string; title: string; detail?: ReactNode; traces?: TraceAssociation[] }

function recordLink(view: string, id: string) {
  return '/observer/?' + new URLSearchParams({ view, id, mode: 'records' })
}
function Evidence({ items }: { items?: string[] }) {
  return items?.length ? <ul>{items.map((item, i) => <li key={i}>{item}</li>)}</ul> : null
}
function ExecutionDetail({ execution, reports }: { execution: Execution; reports: Report[] }) {
  return <>
    <p>{stateLabel(execution.authority)} · 启动时目标版本 {execution.goalRevision ?? '未记录'}</p>
    {execution.startup && <p>启动事件 · {eventStatusLabel(execution.startup.status)}</p>}
    {execution.startEventId && <a href={recordLink('internal-events', execution.startEventId)}>查看输入事件</a>}
    {!!reports.length && <ul aria-label="本轮报告">{reports.map(r => <li key={r.id}><a href={recordLink('reports', r.id)}>{r.summary}</a></li>)}</ul>}
    <a href={recordLink('executions', execution.id)}>查看执行记录</a>
  </>
}
function collectEvents(issue: Issue, executions: Execution[], internalEvents: InternalEvent[]): Event[] {
  const events: Event[] = [{ id: 'created', at: issue.createdAt, title: 'Topic 创建 Issue', traces: issue.traceAssociations }]
  const reports = issue.reports || []
  for (const execution of executions) {
    events.push({ id: execution.id, at: execution.createdAt, traces: execution.traceAssociations, title: '启动第 ' + execution.generation + ' 轮执行',
      detail: <ExecutionDetail execution={execution} reports={reports.filter(r => r.executionId === execution.id)}/> })
  }
  for (const r of reports) {
    events.push({ id: r.id, at: r.createdAt, traces: r.traceAssociations, title: 'Issue Session 提交报告 · ' + r.summary, detail: <>
      <p>第 {r.generation} 轮执行{r.goalRevision ? ' · 目标版本 ' + r.goalRevision : ''}</p>
      {r.details && <p>{r.details}</p>}<Evidence items={r.evidence}/><Evidence items={r.questions}/>
      <a href={recordLink('reports', r.id)}>查看报告记录</a>
    </> })
  }
  const updates = new Map(internalEvents.filter(n => n.goalUpdate).map(n => [n.event_id, n]))
  const recent = issue.latestRequirementsNotification
  if (recent?.event_id && !updates.has(recent.event_id)) updates.set(recent.event_id, recent)
  for (const n of updates.values()) {
    if (n.createdAt) events.push({ id: n.event_id, at: n.createdAt, traces: n.traceAssociations, title: 'Topic 发出目标修订通知',
      detail: <><p>目标版本 {n.revision ?? '未记录'} · {eventStatusLabel(n.status)}</p>
        <a href={recordLink('internal-events', n.event_id)}>查看修订事件</a>
        {n.recipient?.execution_id && <a href={recordLink('executions', n.recipient.execution_id)}>查看接收通知的执行</a>}</> })
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

function IssueFlow({ issue, topicTitle, executions }: { issue: Issue; topicTitle?: string; executions: Execution[] }) {
  const current: FlowNode = { kind: 'issue', label: issue.title || issue.id }
  const topic: FlowNode = { kind: 'topic', label: topicTitle || issue.topicId, href: viewHref('topic', issue.topicId) }
  const lanes: FlowLane[] = [{
    id: issue.id, nodes: [topic, current, { kind: 'state', label: stateLabel(issue.status) }],
    edges: ['创建', '当前状态'], detail: <>{issue.goal}<p>创建于 {formatTime(issue.createdAt)}</p>
      {issue.blockers?.map(b => <p key={b.id}>{b.reason}</p>)}</>,
  }]
  for (const execution of executions) {
    const run: FlowNode = { kind: 'execution', label: '第 ' + execution.generation + ' 轮', href: recordLink('executions', execution.id) }
    const reports = (issue.reports || []).filter(r => r.executionId === execution.id)
    const base = { nodes: [current, run], edges: ['启动'] }
    if (!reports.length) lanes.push({ id: execution.id, ...base, detail: <p>启动于 {formatTime(execution.createdAt)} · {eventStatusLabel(execution.startup?.status)}</p> })
    for (const r of reports) {
      lanes.push({ id: r.id, nodes: [...base.nodes, { kind: 'report', label: r.summary, href: recordLink('reports', r.id) }],
        edges: [...base.edges, 'Issue Session 提交'], detail: <p>{formatTime(r.createdAt)} · {r.details || r.summary}</p> })
    }
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
  const executions = usePages<Execution>('executions?' + new URLSearchParams({ issueId: id, order: 'newest', preview: 'true', limit: '20', trace: 'true' }))
  const internalEvents = usePages<InternalEvent>('internal-events?' + new URLSearchParams({ issueId: id, order: 'newest', preview: 'true', limit: '20', trace: 'true' }))
  const [expanded, setExpanded] = useState('')
  if (!issue) return <p className="panel empty" role={record.error ? 'alert' : 'status'}>{record.error || '正在读取 Issue…'}</p>
  const events = collectEvents(issue, executions.data || [], internalEvents.data || [])
  const latest = events[0]
  return <article aria-label="当前 Issue" className="issue-detail">
    {record.error && <p role="alert" className="read-error">{record.error} · 快照已过时</p>}
    <section className="panel issue-overview">
      <div className="source-line"><span className="issue-status"><CircleDot size={14}/>{stateLabel(issue.status)}</span>
        <a href={viewHref('topic', issue.topicId)}>{topic.data?.title || issue.topicId}<ChevronRight size={14}/></a></div>
      <div className="observable-actions"><a href={metricsHref('issue', id)}>工具用量</a><CopyLink href={viewHref('issue',id,{topicId:scope})}/></div>
      <h2>{issue.title}</h2><p className="issue-goal">{issue.goal || '目标未记录'}</p>
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
    <IssueFlow issue={issue} topicTitle={topic.data?.title} executions={executions.data || []}/>
    <section className="panel"><div className="panel-heading"><h2>进展记录</h2><span>目标版本 {issue.goalRevision ?? '未记录'}</span></div>
      {executions.error && <p role="alert" className="read-error">执行记录读取失败 · {executions.error}</p>}
      {!executions.data && !executions.error && <p className="empty">正在读取执行记录…</p>}
      <ol className="issue-events">{events.map(e => <li key={e.id}>
        <time dateTime={e.at} title={e.at}>{formatTime(e.at)}</time>
        {e.detail ? <><button type="button" aria-expanded={expanded === e.id} onClick={() => setExpanded(expanded === e.id ? '' : e.id)}>
          {e.title}<ChevronRight size={15}/></button>{expanded === e.id && <div className="issue-event-detail">{e.detail}</div>}</> : <strong>{e.title}</strong>}
        <div><TraceLinks associations={e.traces} at={e.at}/></div>
      </li>)}</ol>
      {internalEvents.error && <p role="alert" className="read-error">修订通知读取失败 · {internalEvents.error}</p>}
      {internalEvents.next && <Button variant="ghost" onClick={internalEvents.more} disabled={internalEvents.loading}>加载更早事件</Button>}
      {executions.next && <Button variant="ghost" onClick={executions.more} disabled={executions.loading}>加载更早执行</Button>}
    </section>
    <section className="panel issue-sources"><details><summary>来源材料（{issue.materials?.length || 0}）</summary>
      {issue.materials?.length ? <ul>{issue.materials.map((m, i) => <li key={i}>{m.messageId ? <a href={viewHref('message', m.messageId)}>{m.label || '查看来源 Message'}</a> : [m.label, m.uri].filter(Boolean).join(' · ')}</li>)}</ul> : <p>未记录来源材料</p>}</details>
      <div className="issue-links"><a href={recordLink('issues', issue.id)}>查看完整记录</a>
      {issue.bindingId && <a href={recordLink('bindings', issue.bindingId)}>查看 Session →</a>}
      {issue.pullRequests?.map(pr => <span key={pr.id}><a href={recordLink('pull-requests', pr.id)}>{pr.canonicalUrl}</a> · <a href={recordLink('subscriptions', pr.subscriptionId)}>查看订阅</a></span>)}</div>
    </section>
  </article>
}
export default function IssueView({ query }: ViewProps) {
  return <ObjectWorkspace view="issue" query={query}>{id => <IssueDetail key={id} id={id} scope={query.get('topicId') || ''}/>}</ObjectWorkspace>
}
