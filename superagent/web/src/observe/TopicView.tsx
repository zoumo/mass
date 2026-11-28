import CopyLink from './CopyLink'
import { useState, type ReactNode } from 'react'
import { Activity, ChevronDown, CircleDot, Mail } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, eventStatusLabel, messageBody, sourceLabel, stateLabel, type Page, type WorkbenchRow } from '../workbench'
import ObjectWorkspace from './ObjectWorkspace'
import { ObjectAvatar } from './ConversationCard'
import PerspectiveFlow, { type FlowLane, type FlowNode } from './PerspectiveFlow'
import { sessionChatHref, usePages, useRecord, viewHref, type ViewProps } from './observer'
import StructuredContent, { type StructuredBody } from './StructuredContent'
import { TraceLinks, type TraceAssociation } from './TraceLinks'
import './topic.css'

const actions = {
  routed_message: '投往 Topic', route_request: 'Topic 退回', issue_created: '创建 Issue',
  issue_session_started: '启动 Issue Session', goal_notification: '通知 Issue Session 当前目标版本',
  issue_session_update: '通知 Issue Session 继续', context_supplement: '补充 Issue Session 上下文',
  report_submitted: '提交报告', blocker_resolved: '解除阻塞', completion: '业务验收',
  cancellation: '取消 Issue', reopening: '重开 Issue', issue_snapshot: '已保存的 Issue 状态',
} as const

type Event = {
  id: string; kind: keyof typeof actions; at: string; actor?: string; traceAssociations?: TraceAssociation[]
  title?: string; issueId?: string; messageId?: string; sessionBindingId?: string; reportId?: string
  eventId?: string; returnedEventId?: string; blockerId?: string
  summary?: string; status?: string; issueStatus?: string; goalRevision?: number; historical?: boolean
}
type Detail = WorkbenchRow & {
  details?: string; evidence?: string[]; current?: boolean; resolvedBlockers?: {
    id: string; reason: string; resolution?: { at: string; reason: string; messageId?: string }
  }[]
  body?: string
  content?: StructuredBody
  sentAt?: string
}
const actors: Record<string, string> = { core: 'Core', topic: 'Topic', router: 'Router', issue: 'Issue Session' }
const categories = { all: '全部', message: '消息', issue: 'Issue' } as const
const recordHref = (view: string, filters: Record<string, string>) =>
  '/?' + new URLSearchParams({ view, ...filters })

function ReadError({ message, stale }: { message: string; stale?: boolean }) {
  return <p role="alert" className="read-error">{message}{stale && ' · 快照已过时'} · 可点「刷新」重试</p>
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <div className="topic-fact"><dt>{label}</dt><dd>{children}</dd></div>
}

function MessageFacts({ row }: { row: Event }) {
  const internalEvent = useRecord<Detail>('internal-events/' + encodeURIComponent(row.eventId!))
  const message = useRecord<Detail>(row.messageId ? 'messages/' + encodeURIComponent(row.messageId) : '')
  const reason = internalEvent.data?.body
  return <>
    {internalEvent.error && <ReadError message={internalEvent.error} stale={!!internalEvent.data}/>}
    {message.error && <ReadError message={message.error} stale={!!message.data}/>}
    <dl>
      <Fact label="发送状态">{eventStatusLabel(internalEvent.data?.status || row.status)}</Fact>
      <Fact label="登记时间">{row.at}</Fact>
      {internalEvent.data?.sentAt && <Fact label={row.kind === 'route_request' ? 'Router Session 受理' : 'Topic Session 受理'}>{internalEvent.data.sentAt}</Fact>}
      {typeof reason === 'string' && <Fact label={row.kind === 'route_request' ? '退回原因' : '路由依据'}>{reason}</Fact>}
      {message.data && <><Fact label="来源">{sourceLabel(message.data.source)}</Fact>
        <Fact label="消息原文"><pre>{messageBody(message.data) || '未记录正文'}</pre></Fact></>}
    </dl>
    {(internalEvent.loading && !internalEvent.data || row.messageId && message.loading && !message.data) && <p>正在读取详情…</p>}
    <div className="topic-links">
      {row.messageId && <a href={viewHref('message',row.messageId, {threadId: message.data?.threadId || ''})}>查看此消息 →</a>}
      {row.returnedEventId && <a href={recordHref('internal-events',{ id: row.returnedEventId })}>查看原事件 →</a>}
      <a href={recordHref('internal-events',{ id: row.eventId! })}>事件记录 →</a>
    </div>
  </>
}

function IssueFacts({ row }: { row: Event }) {
  const notification = ['goal_notification','issue_session_update','context_supplement'].includes(row.kind)
  const collection = row.reportId ? 'reports' : row.kind === 'issue_session_started' ? 'session-bindings' : notification ? 'internal-events' : 'issues'
  const id = row.reportId || (row.kind === 'issue_session_started' ? row.sessionBindingId : notification ? row.eventId : row.issueId)
  const detail = useRecord<Detail>(collection + '/' + encodeURIComponent(id!))
  const data = detail.data
  const currentReport = data?.current ?? (row.historical ? false : undefined)
  const change = row.kind === 'completion' ? data?.completion : row.kind === 'cancellation' ? data?.cancellation : row.kind === 'reopening' ? data?.reopening : undefined
  const changeTime = change && ('completedAt' in change ? change.completedAt : change.at)
  const blocker = data?.resolvedBlockers?.find(item => item.id === row.blockerId && item.resolution?.at === row.at)
  return <>
    {detail.error && <ReadError message={detail.error} stale={!!data}/>}
    {!data && !detail.error && <p>正在读取详情…</p>}
    <dl>
      <Fact label={row.kind === 'issue_snapshot' ? '快照时间' : '记录时间'}>{row.at}</Fact>
      {row.sessionBindingId && <Fact label="SessionBinding">{row.sessionBindingId}</Fact>}
      {row.goalRevision && <Fact label="记录的目标版本">{row.goalRevision}</Fact>}
      {currentReport !== undefined && <Fact label="报告状态">{currentReport ? '当前报告' : '历史报告'}</Fact>}
      {row.summary && <Fact label="报告摘要">{data?.summary || row.summary}</Fact>}
      {data?.details && <Fact label="报告详情">{data.details}</Fact>}
      {data?.evidence?.length ? <Fact label="报告证据">{data.evidence.join('\n')}</Fact> : null}
      {data?.startup?.status && <Fact label="启动事件">{eventStatusLabel(data.startup.status)}</Fact>}
      {notification && data && <><Fact label="通知事件">{eventStatusLabel(data.status)}</Fact>
        {data.content && <Fact label="补充说明"><StructuredContent content={data.content}/></Fact>}</>}
      {change && changeTime === row.at && <><Fact label="依据">{change.reason}</Fact>
        {'evidence' in change && <Fact label="验收证据">{change.evidence.join('\n')}</Fact>}</>}
      {blocker?.resolution && <><Fact label="原阻塞">{blocker.reason}</Fact><Fact label="解除依据">{blocker.resolution.reason}</Fact></>}
      {collection === 'issues' && data && <><Fact label="Issue 当前状态">{stateLabel(data.status)}</Fact>
        {data.goal && <Fact label={'当前目标' + (data.goalRevision ? ' · 版本 ' + data.goalRevision : '')}>{data.goal}</Fact>}
        {data.blockers?.map(block => <Fact key={block.id} label={block.kind === 'dependency' ? '当前依赖阻塞' : '当前人工阻塞'}>
          {block.reason} {block.dependencyIssueId && <a href={viewHref('issue',block.dependencyIssueId)}>查看依赖 Issue →</a>}
        </Fact>)}</>}
    </dl>
    <div className="topic-links">
      <a href={viewHref('issue',row.issueId)}>查看此 Issue →</a>
      {row.reportId && <a href={recordHref('reports',{ id: row.reportId })}>查看报告 →</a>}
      {row.sessionBindingId && <a href={recordHref('session-bindings',{ id: row.sessionBindingId })}>查看 SessionBinding →</a>}
      {row.eventId && <a href={recordHref('internal-events',{ id: row.eventId })}>查看事件 →</a>}
      {blocker?.resolution?.messageId && <a href={viewHref('message',blocker.resolution.messageId)}>解除依据消息 →</a>}
    </div>
  </>
}

type Category = keyof typeof categories
type ActivityRead = { data?: Event[]; error?: string }
const activityPath = (id: string, filter: Category) =>
  'topics/' + encodeURIComponent(id) + '/activity?' + new URLSearchParams({ filter, limit: '10' })

function topicLane(row: Event, topic: FlowNode): FlowLane {
  const isMessage = row.kind === 'routed_message' || row.kind === 'route_request'
  const lane = (nodes: FlowNode[], edges: string[]): FlowLane => ({
    id: row.id, nodes, edges, detail: <div className="topic-row-facts">
      {isMessage ? <MessageFacts row={row}/> : <IssueFacts row={row}/>}
    </div>,
  })
  if (isMessage) {
    const message: FlowNode = { kind:'message', label:row.title || row.messageId || '消息原文未记录',
      href:row.messageId ? viewHref('message',row.messageId) : undefined }
    const status = eventStatusLabel(row.status)
    if (row.kind === 'route_request') {
      return lane([topic, message, { kind:'router', label:'Router', href:viewHref('router',row.messageId) }], ['退回消息','请求路由 · ' + status])
    }
    const sender = actors[row.actor || '']
    return lane([message,topic], [(sender ? sender + ' ' : '') + '投递 · ' + status])
  }

  const issue: FlowNode = { kind:'issue', label:row.title || row.issueId || 'Issue', href:row.issueId ? viewHref('issue',row.issueId) : undefined }
  const session: FlowNode = { kind:'session', label:'Issue Session',
    href:row.sessionBindingId ? recordHref('session-bindings',{ id:row.sessionBindingId }) : undefined }
  const actor: FlowNode = row.actor === 'topic' ? topic : { kind:'state', label:actors[row.actor || ''] || '主体未记录' }
  if (row.kind === 'report_submitted') {
    const report: FlowNode = { kind:'report', label:row.summary || '报告', href:row.reportId ? recordHref('reports',{ id:row.reportId }) : undefined }
    return lane([session,report,issue], ['提交报告','报告所属 Issue'])
  }
  if (row.kind === 'issue_snapshot') {
    return lane([issue,{ kind:'state', label:stateLabel(row.issueStatus) }], ['快照状态'])
  }
  if (row.sessionBindingId) {
    const label = row.kind === 'goal_notification' && row.goalRevision ? '通知目标版本 ' + row.goalRevision : actions[row.kind]
    return lane([actor,session,issue], [label,'会话所属 Issue'])
  }
  return lane([actor,issue], [actions[row.kind]])
}

function TopicFlow({ id, title, filter, activity }: { id: string; title: string; filter: Category; activity: ActivityRead }) {
  // A busy Issue can occupy the whole mixed page. Each category has its own
  // bounded preview in the combined diagram. A single filter reuses its page.
  const messages = useRecord<Page<Event>>(filter === 'all' ? activityPath(id,'message') : '')
  const issues = useRecord<Page<Event>>(filter === 'all' ? activityPath(id,'issue') : '')
  const groups: {name:string; rows?:Event[]; error?:string}[] = []
  const messageRows = filter === 'all' ? messages.data?.items : activity.data
  const issueRows = filter === 'all' ? issues.data?.items : activity.data
  const sample: Event[] = []
  if (filter !== 'issue') {
    groups.push({ name:'消息', rows:messageRows, error:filter === 'all' ? messages.error : activity.error })
    for (const kind of ['routed_message','route_request']) {
      const row = messageRows?.slice(0,10).find(item => item.kind === kind)
      if (row) sample.push(row)
    }
  }
  if (filter !== 'message') {
    groups.push({ name:'Issue', rows:issueRows, error:filter === 'all' ? issues.error : activity.error })
    const recent = issueRows?.slice(0,10) || []
    if (recent[0]) {
      sample.push(recent[0])
      const otherActions = recent.filter(row => row.kind !== recent[0]!.kind)
      const second = otherActions.find(row => row.kind === 'report_submitted') || otherActions[0]
      if (second) sample.push(second)
    }
  }
  const states = groups.map(group => {
    if (group.error) return group.name + ' ' + group.error + (group.rows ? ' · 快照已过时' : '') + '，可刷新重试'
    if (!group.rows) return '正在读取' + group.name + '记录…'
    if (!group.rows.length) return '暂无' + group.name + '活动记录'
    return ''
  }).filter(Boolean).join('。')
  const topic: FlowNode = { kind:'topic', label:title }
  return <PerspectiveFlow title="Topic 活动流程" lanes={sample.map(row => topicLane(row,topic))}
    description={'每类取最近 10 条，展示最近的投递、退回和最多两项 Issue 动作。更早的记录可在活动流中翻页。' + (sample.length ? states : '')}
    empty={states || '暂无活动记录'}/>
}

function ActivityRow({ row }: { row: Event }) {
  const [open,setOpen] = useState(false)
  const isMessage = row.kind === 'routed_message' || row.kind === 'route_request'
  const Icon = isMessage ? Mail : CircleDot
  return <li data-kind={row.kind}><details onToggle={e => setOpen(e.currentTarget.open)}>
    <summary><Icon size={17} className={isMessage ? 'topic-msg-icon' : 'topic-issue-icon'}/>
      <div className="topic-row-text"><div className="topic-row-meta"><time dateTime={row.at} title={row.at}>{formatTime(row.at)}</time>
        <span>{row.kind === 'issue_snapshot' ? '当前快照' : actors[row.actor || ''] || '主体未记录'}</span></div>
        <strong>{actions[row.kind]}</strong><span className="topic-row-object">{row.title || row.issueId || row.messageId || row.eventId}</span>
        {row.status && <small>{eventStatusLabel(row.status)}</small>}{row.issueStatus && <small>{stateLabel(row.issueStatus)}</small>}
        {row.summary && <p>{row.summary}</p>}
      </div><ChevronDown size={16} className="topic-chevron"/>
    </summary>
    <div className="topic-row-traces"><TraceLinks associations={row.traceAssociations} at={row.at}/></div>
    {open && <div className="topic-row-facts">{isMessage ? <MessageFacts row={row}/> : <IssueFacts row={row}/>}</div>}
  </details></li>
}

function TopicDetail({ id, filter }: {id: string; filter: keyof typeof categories}) {
  const topic = useRecord<WorkbenchRow>('topics/' + encodeURIComponent(id))
  const activity = usePages<Event>(activityPath(id,filter))
  const t = topic.data
  return <article aria-label="当前 Topic" className="message-detail topic-detail">
    {topic.error && <ReadError message={topic.error} stale={!!t}/>}
    {t ? <section className="panel detail-body">
      <div className="source-line"><ObjectAvatar kind="topic" label="Topic" href={sessionChatHref('topic', id)} title="与 Topic Session 对话"/>
        <span className="source-tag">Topic</span><span>{stateLabel(t.status)}</span><CopyLink href={viewHref('topic', id, {filter: filter==='all' ? '' : filter})}/></div>
      <h2 tabIndex={-1}>{t.title || id}</h2>
      <p className="topic-goal"><span className="topic-goal-label">目标</span>{t.goal || '目标未记录'}</p>
      {t.completion && <details className="topic-acceptance"><summary>Topic 业务验收</summary>
        <p>{t.completion.reason}</p>{t.completion.evidence?.map((ref,i) => <p key={i}>{ref}</p>)}</details>}
      <nav className="topic-links" aria-label="Topic 关联记录">
        <a href={viewHref('issue','',{ topicId:id })}>所属 Issue →</a>
        {t.sessionBindingId && <a href={recordHref('session-bindings',{id:t.sessionBindingId})}>查看 Session →</a>}
        <a href={recordHref('outbox',{topicId:id})}>渠道回执 →</a>
      </nav>
      {!!t.pullRequests?.length && <details className="topic-acceptance"><summary>关注的拉取请求（{t.pullRequests.length}）</summary>
        <ul>{t.pullRequests.map(pr => <li className="topic-links" key={pr.id}><a href={pr.canonicalUrl}>{pr.canonicalUrl}</a></li>)}</ul></details>}
    </section> : !topic.error && <p className="panel empty">正在读取 Topic…</p>}
    <TopicFlow id={id} title={t?.title || id} filter={filter} activity={activity}/>
    <section className="panel" aria-label="活动流">
      <header className="panel-heading"><div className="panel-title"><Activity size={17}/><h2>最近活动</h2></div>
        <nav aria-label="筛选活动" className="topic-filters">
          {(Object.keys(categories) as (keyof typeof categories)[]).map(key =>
            <a key={key} aria-current={key===filter ? 'true' : undefined} href={viewHref('topic',id,key==='all'?{}:{ filter:key })}>{categories[key]}</a>)}
        </nav>
      </header>
      {activity.error && <ReadError message={activity.error} stale={!!activity.data}/>}
      {!activity.data && !activity.error && <p className="empty">正在读取活动…</p>}
      {activity.data?.length === 0 && !activity.error && <p className="empty">暂无{filter==='all' ? '' : categories[filter]}活动记录</p>}
      <ol className="topic-feed">{activity.data?.map(row => <ActivityRow key={row.id} row={row}/>)}</ol>
      {activity.next && <Button variant="ghost" className="load-more" disabled={activity.loading} onClick={activity.more}>加载更多活动</Button>}
    </section>
  </article>
}

export default function TopicView({ query }: ViewProps) {
  const filter = query.get('filter')
  return <ObjectWorkspace view="topic" query={query}>{id => <TopicDetail key={id} id={id} filter={filter==='issue'||filter==='message'?filter:'all'}/>}</ObjectWorkspace>
}
