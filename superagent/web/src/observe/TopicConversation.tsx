import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { ArrowDown, ArrowRight, ChevronRight, ClipboardList, GitBranch, Hash, Network, Plus, Send, X } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, routingDeliveryLabel, sourceLabel, stateLabel, type Page, type WorkbenchRow } from '../workbench'
import CopyLink from './CopyLink'
import StructuredContent, { type StructuredBody } from './StructuredContent'
import { ObjectAvatar, PrototypeCard, type Participant } from './TopicCardsPrototype'
import { navigate, usePages, useRecord, useRefresh, viewHref, type ViewProps } from './observer'
import './conversation.css'

const view = 'topic-conversation'
const activity = (params: Record<string, string>) =>
  'topic-activity?' + new URLSearchParams({ mode: 'conversation', ...params })
const recordHref = (view: string, id: string) => '/observer/?' + new URLSearchParams({ view, id })
const labels = {
  all: '全部', router_routed: 'Router 路由入群', thread_direct: 'Core Thread 直投',
  issue_created: '创建 Issue', worker_dispatch: '任务派发', report: 'Worker Report',
} as const
type Filter = keyof typeof labels

type Topic = { id: string; title: string; status: string; createdAt: string; lastMessageAt?: string }
type EventIdentity = { id: string; topicId: string; at: string }
type IngressEvent = EventIdentity & {
  kind: 'router_routed' | 'thread_direct'; actor: 'router' | 'core'; summary?: string; status: string
  messageId: string; deliveryId: string; threadKey?: string
}
type IssueCreated = EventIdentity & { kind: 'issue_created'; issueId: string; title: string }
type WorkerDispatch = EventIdentity & {
  kind: 'worker_dispatch'; issueId: string; title: string; executionId: string; generation: number
  deliveryId: string; dispatchKind: keyof typeof dispatchLabels; summary?: string; status: string; content?: StructuredBody
}
type WorkerReport = EventIdentity & {
  kind: 'report'; issueId: string; title: string; executionId: string; generation: number
  reportId: string; reportKind: keyof typeof reportLabels; summary: string; deliveryId: string
}
type ConversationEvent = IngressEvent | IssueCreated | WorkerDispatch | WorkerReport
type RoutingDelivery = { status: string; body?: string }
type WorkerDelivery = {
  status: string
  content?: StructuredBody
  body?: { instruction?: string; materials?: WorkbenchRow['materials'] }
  refs?: { kind: string; id: string; goal_revision?: number }[]
}
type Subscription = { id: string; threadKey: string }
type Issue = {
  id: string; title: string; status: string
  completion?: { reason: string; evidence: string[]; completedAt: string }
}
type Report = { summary: string; details?: string; evidence?: string[]; questions?: string[]; goalRevision?: number }
type Execution = { startEventId?: string }

const dispatchLabels = {
  worker_started: '启动 Execution', worker_update: '继续执行',
  context_supplement: '补充上下文', goal_notification: '目标更新',
} as const
const reportLabels = { progress: '进展', ready: '申请验收', blocked: '受阻', failed: '失败' } as const

const day = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'full' })
const clock = new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium', hourCycle: 'h23' })

export default function TopicConversation({ query }: ViewProps) {
  const search = query.get('search') || ''
  return <TopicWorkspace key={search} query={query} search={search}/>
}

function TopicWorkspace({ query, search }: ViewProps & { search: string }) {
  const prototype = import.meta.env.DEV && query.get('variant') === 'A'
  const variant = prototype ? 'A' : ''
  const requestedTopicId = query.get('id') || ''
  const [automaticTopicId, setAutomaticTopicId] = useState('')
  const topics = usePages<Topic>(activity({ search, limit: '20' }))
  const firstTopicId = topics.data?.[0]?.id || ''
  const topicId = requestedTopicId || automaticTopicId || firstTopicId
  useLayoutEffect(() => {
    if (!requestedTopicId && firstTopicId) setAutomaticTopicId(old => old || firstTopicId)
  }, [requestedTopicId, firstTopicId])
  const chosen = query.get('filter') || 'all'
  const filter: Filter = Object.hasOwn(labels, chosen) ? chosen as Filter : 'all'
  const eventId = query.get('event') || ''
  const { online } = useRefresh()
  function href(id: string, nextFilter = 'all', event = '') {
    return viewHref(view, id, { search, filter: nextFilter === 'all' ? '' : nextFilter, event, variant })
  }
  return <div className="conversation-workspace">
    <aside className="panel conversation-sidebar" aria-label="Topics">
      <div className="conversation-sidebar-heading">
        <h2>Topics</h2>
        <form className="conversation-search" onSubmit={event => {
          event.preventDefault()
          const search = String(new FormData(event.currentTarget).get('search') || '').trim()
          navigate(viewHref(view, '', { search, variant }))
        }}>
          <input type="search" name="search" aria-label="搜索 Topic" placeholder="搜索 Topic 名称或 ID" defaultValue={search}/>
          <Button type="submit" variant="outline" size="sm" disabled={!online}>搜索</Button>
        </form>
      </div>
      {topics.error && <ReadError error={topics.error} stale={!!topics.data}/>}
      <ul className="conversation-topics">
        {topics.data?.map(topic => <li key={topic.id}>
          <a href={href(topic.id)} aria-current={topicId === topic.id ? 'true' : undefined}>
            <span className="conversation-avatar" data-actor="topic"><Hash size={16}/></span>
            <span className="conversation-topic-name">
              <strong title={topic.title}>{topic.title}</strong>
              <span className="conversation-topic-meta">
                <small title={topic.id}>{topic.id}</small>
                {topic.lastMessageAt && <time dateTime={topic.lastMessageAt}>{formatTime(topic.lastMessageAt)}</time>}
              </span>
            </span>
          </a>
        </li>)}
      </ul>
      {!topics.data && topics.loading && !topics.error && <p className="empty">正在读取 Topic…</p>}
      {topics.data?.length === 0 && <p className="empty">{search ? '没有匹配的 Topic' : '暂无 Topic'}</p>}
      {topics.next && <div className="conversation-footer"><Button size="sm" variant="outline" disabled={topics.loading || !online} onClick={() => void topics.more()}>加载更多 Topic</Button></div>}
    </aside>
    {topicId && <Conversation key={topicId} topicId={topicId} filter={filter} eventId={eventId} href={href} prototype={prototype}/>}
  </div>
}

type ConversationProps = {
  topicId: string; filter: Filter; eventId: string; prototype: boolean
  href: (id: string, filter?: Filter, event?: string) => string
}

function Conversation({ topicId, filter, eventId, href, prototype }: ConversationProps) {
  const topic = useRecord<Topic>('topics/' + encodeURIComponent(topicId))
  const timeline = usePages<ConversationEvent>(activity({ topicId, filter, limit: '20' }))
  const selected = timeline.data?.find(event => event.id === eventId)
  const event = useRecord<Page<ConversationEvent>>(eventId && timeline.data && !selected ? activity({ topicId, eventId }) : '')
  const focused = selected || event.data?.items[0]
  const context = useRef<HTMLElement>(null)
  useLayoutEffect(() => {
    if (eventId && window.matchMedia('(max-width: 850px)').matches) {
      context.current?.focus({ preventScroll: true })
      context.current?.scrollIntoView({ block: 'start' })
    }
  }, [eventId])
  return <>
    <section className="panel conversation-feed" aria-label="群聊时间线">
      <header className="conversation-group">
        {prototype ? <ObjectAvatar {...topicParticipant(topicId)}/>
          : <span className="conversation-avatar" data-actor="topic"><Hash size={23}/></span>}
        <div><h2>{topic.data?.title || topicId}</h2><code>{topicId}</code></div>
        {topic.data?.status && <span className="conversation-status">{stateLabel(topic.data.status)}</span>}
      </header>
      <nav className="conversation-filters" aria-label="按消息类型筛选">
        {(Object.keys(labels) as Filter[]).map(kind =>
          <a key={kind} href={href(topicId, kind)} aria-current={filter === kind ? 'true' : undefined}>{labels[kind]}</a>)}
      </nav>
      {topic.error && <ReadError error={topic.error} stale={!!topic.data}/>}
      {timeline.error && <ReadError error={timeline.error} stale={!!timeline.data}/>}
      <Timeline key={filter} timeline={timeline} eventId={eventId} eventHref={id => href(topicId, filter, id)} prototype={prototype}/>
    </section>
    <aside ref={context} className="panel conversation-context" aria-label={eventId ? '消息详情' : '群组信息'} tabIndex={-1}>
      <header className="conversation-aside-heading">
        <h2>{eventId ? '消息详情' : '群组信息'}</h2>
        {eventId && <div><CopyLink href={href(topicId, filter, eventId)}/><a href={href(topicId, filter)} aria-label="关闭消息详情"><X size={17}/></a></div>}
      </header>
      {eventId ? <>
        {event.error && <ReadError error={event.error} stale={!!event.data}/>}
        {focused ? <EventDetails key={focused.id} event={focused} previewOptions={prototype}/>
          : !event.error && <p className="empty">正在读取消息…</p>}
      </> : <GroupInfo topicId={topicId} avatars={prototype}/>}
    </aside>
  </>
}

function Timeline({ timeline, eventId, eventHref, prototype }: {
  timeline: ReturnType<typeof usePages<ConversationEvent>>; eventId: string; eventHref: (id: string) => string; prototype: boolean
}) {
  const { online } = useRefresh()
  const viewport = useRef<HTMLDivElement>(null)
  const position = useRef<{ atEnd: boolean; anchor?: string; offset: number }>({ atEnd: true, offset: 0 })
  const previousNewest = useRef<string>(undefined)
  const [hasNew, setHasNew] = useState(false)
  const rows = timeline.data

  function recordPosition() {
    const node = viewport.current
    if (!node) return
    const top = node.getBoundingClientRect().top
    const anchor = Array.from(node.querySelectorAll<HTMLElement>('[data-event-id]')).find(row => row.getBoundingClientRect().bottom > top)
    const atEnd = node.scrollHeight - node.clientHeight - node.scrollTop < 8
    position.current = { atEnd, anchor: anchor?.dataset.eventId, offset: anchor ? anchor.getBoundingClientRect().top - top : 0 }
    if (atEnd) setHasNew(false)
  }

  useLayoutEffect(() => {
    const node = viewport.current
    if (!node || !rows?.length) return
    const { atEnd, anchor, offset } = position.current
    const newest = rows[0].id
    if (atEnd) node.scrollTop = node.scrollHeight
    else {
      const saved = Array.from(node.querySelectorAll<HTMLElement>('[data-event-id]')).find(row => row.dataset.eventId === anchor)
      if (saved) node.scrollTop += saved.getBoundingClientRect().top - node.getBoundingClientRect().top - offset
      if (previousNewest.current && previousNewest.current !== newest) setHasNew(true)
    }
    previousNewest.current = newest
  }, [rows, prototype])

  function jumpToLatest() {
    const node = viewport.current
    if (node) node.scrollTop = node.scrollHeight
    recordPosition()
  }

  function loadEarlier() {
    recordPosition()
    position.current.atEnd = false
    void timeline.more()
  }

  let date = ''
  return <>
    <div className="conversation-scroll" ref={viewport} onScroll={recordPosition} tabIndex={0} aria-label="会话记录">
      {timeline.next && <div className="conversation-earlier">
        <Button size="sm" variant="outline" disabled={timeline.loading || !online} onClick={loadEarlier}>加载更早记录</Button>
      </div>}
      {!rows && timeline.loading && !timeline.error && <p className="empty">正在读取会话…</p>}
      {rows?.length === 0 && <p className="empty">此 Topic 暂无这类消息</p>}
      {rows?.slice().reverse().map(event => {
        const eventDay = day.format(new Date(event.at))
        const separator = date !== eventDay
        date = eventDay
        return <section className="conversation-event" key={event.id} data-event-id={event.id} data-kind={event.kind}>
          {separator && <div className="conversation-day">{eventDay}</div>}
          {prototype
            ? <ConversationCardPreview event={event} href={eventHref(event.id)} selected={eventId === event.id}/>
            : <EventCard event={event} href={eventHref(event.id)} selected={eventId === event.id}/>}
        </section>
      })}
    </div>
    {rows && <footer className="conversation-footer">
      <span>{rows.length} 条记录</span>
      {rows.length > 0 && <Button variant="ghost" size="sm" onClick={jumpToLatest}><ArrowDown/>{hasNew ? '有新消息 · 跳到最新' : '跳到最新'}</Button>}
    </footer>}
  </>
}

function topicParticipant(id: string): Participant {
  return { kind: 'topic', label: 'Topic', href: viewHref('topic', id), title: '查看 Topic · ' + id }
}

function issueParticipant(id: string, title: string): Participant {
  return { kind: 'issue', label: 'Issue', href: viewHref('issue', id), title: '查看 Issue · ' + title }
}

function ConversationCardPreview({ event, href, selected }: { event: ConversationEvent; href: string; selected: boolean }) {
  const topic = topicParticipant(event.topicId)
  const common = { href, selected, at: event.at, time: clock.format(new Date(event.at)) }
  if (event.kind === 'issue_created') {
    return <PrototypeCard {...common} kind={labels.issue_created} recordId={event.issueId} preview={event.title}
      sender={topic} recipient={issueParticipant(event.issueId, event.title)} facts={[]}/>
  }
  if (event.kind === 'worker_dispatch' || event.kind === 'report') {
    const worker: Participant = { kind: 'worker', label: 'Worker' }
    const dispatch = event.kind === 'worker_dispatch'
    const content = dispatch ? event.content : undefined
    const facts: { label: string; value: ReactNode }[] = [
      { label: 'Issue', value: <span className="prototype-card-issue">
        <ObjectAvatar {...issueParticipant(event.issueId, event.title)}/>{event.title}
      </span> },
      { label: 'Execution', value: `第 ${event.generation} 次` },
    ]
    if (dispatch) facts.push({ label: '投递', value: routingDeliveryLabel(event.status) })
    return <PrototypeCard {...common}
      kind={dispatch ? dispatchLabels[event.dispatchKind] : `Worker Report · ${reportLabels[event.reportKind]}`}
      recordId={dispatch ? event.executionId : event.reportId}
      preview={event.summary || event.title} content={content}
      sender={dispatch ? topic : worker} recipient={dispatch ? worker : topic} facts={facts}/>
  }
  const sender: Participant = event.kind === 'router_routed'
    ? { kind: 'router', label: 'Router', href: viewHref('router', event.messageId), title: '查看 Router · ' + event.messageId }
    : { kind: 'core', label: 'Core' }
  const facts: { label: string; value: ReactNode }[] = [{ label: '投递', value: routingDeliveryLabel(event.status) }]
  if (event.threadKey) facts.push({
    label: '来源',
    value: <a href={recordHref('threads', event.threadKey)} title={event.threadKey}>查看 Thread</a>,
  })
  return <PrototypeCard {...common} kind={labels[event.kind]} recordId={event.messageId} preview={event.summary || '暂无消息摘要'}
    sender={sender} recipient={topic} facts={facts}/>
}

function EventCard({ event, href, selected }: { event: ConversationEvent; href: string; selected: boolean }) {
  if (event.kind === 'issue_created') {
    return <div className="conversation-system" data-actor="topic">
      <a href={href} aria-current={selected ? 'true' : undefined}><Plus size={13}/>Topic Session 创建 Issue · {event.title}</a>
      <time dateTime={event.at}>{clock.format(new Date(event.at))}</time>
    </div>
  }
  const dispatch = event.kind === 'worker_dispatch'
  const report = event.kind === 'report'
  const direct = event.kind === 'thread_direct'
  const content = dispatch ? event.content : undefined
  const actor = dispatch ? 'topic' : report ? 'worker' : event.actor
  const ActorIcon = dispatch ? Send : report ? ClipboardList : direct ? Network : GitBranch
  const worker = (dispatch || report) && <span>Worker · {event.title} · 第 {event.generation} 次执行</span>
  return <article className="conversation-row" data-actor={actor}>
    <span className="conversation-avatar"><ActorIcon size={18}/></span>
    <div className="conversation-message">
      <div className="conversation-sender">
        {report ? worker : <strong>{dispatch ? 'Topic Session' : direct ? 'Core' : 'Router'}</strong>}
        <ArrowRight size={12} aria-label="发送给"/>
        {dispatch ? worker : <span>Topic Session</span>}
        {!dispatch && !report && event.threadKey && <ThreadLink threadKey={event.threadKey}/>}
        <time dateTime={event.at}>{clock.format(new Date(event.at))}</time>
      </div>
      <div className={content ? 'conversation-card' : undefined}>
        <a className="conversation-packet" href={href} aria-current={selected ? 'true' : undefined}>
          <span className="conversation-kind">{dispatch ? dispatchLabels[event.dispatchKind] : report ? reportLabels[event.reportKind] : labels[event.kind]}</span>
          {content ? <span className="conversation-preview">{content.type === 'markdown' ? 'Markdown 正文' : '选项卡片'}</span>
            : <span className="conversation-preview">{event.summary || (dispatch || report ? event.title : event.messageId)}</span>}
          {!report && <span className="conversation-delivery">{routingDeliveryLabel(event.status)}</span>}
          <ChevronRight size={14} aria-hidden="true"/>
        </a>
        {content && <div className="conversation-card-body"><StructuredContent content={content} previewOptions/></div>}
      </div>
    </div>
  </article>
}

function ThreadLink({ threadKey }: { threadKey: string }) {
  return <a className="conversation-thread" href={recordHref('threads', threadKey)}><Hash size={12}/><code>{threadKey}</code></a>
}

function EventDetails({ event, previewOptions }: { event: ConversationEvent; previewOptions: boolean }) {
  if (event.kind === 'issue_created') return <IssueCreatedDetails event={event}/>
  if (event.kind === 'worker_dispatch') return <DispatchDetails event={event} previewOptions={previewOptions}/>
  if (event.kind === 'report') return <ReportDetails event={event}/>
  return <IngressDetails event={event}/>
}

function IngressDetails({ event }: { event: IngressEvent }) {
  const delivery = useRecord<RoutingDelivery>('routing-deliveries/' + encodeURIComponent(event.deliveryId))
  const message = useRecord<WorkbenchRow>('messages/' + encodeURIComponent(event.messageId))
  return <>
    {[delivery, message].map((result, i) => result.error && <ReadError key={i} error={result.error} stale={!!result.data}/>)}
    <section className="conversation-section">
      <h3>{labels[event.kind]}</h3><p className="conversation-original">{message.data?.text || event.summary || event.messageId}</p>
    </section>
    <section className="conversation-section">
      <dl>
        <Fact label="登记时间"><time dateTime={event.at}>{formatTime(event.at)}</time></Fact>
        <Fact label="投递状态">{routingDeliveryLabel(delivery.data?.status || event.status)}</Fact>
        {delivery.data?.body && <Fact label="投递理由">{delivery.data.body}</Fact>}
        {message.data && <Fact label="来源">{sourceLabel(message.data.source)}{message.data.actor && ` · ${message.data.actor}`}</Fact>}
        {event.threadKey && <Fact label="来源 Thread"><ThreadLink threadKey={event.threadKey}/></Fact>}
      </dl>
      <div className="conversation-links">
        <a href={viewHref('message', event.messageId)}>查看原始消息 →</a>
        <a href={recordHref('routing-deliveries', event.deliveryId)}>查看投递记录 →</a>
      </div>
    </section>
  </>
}

function IssueCreatedDetails({ event }: { event: IssueCreated }) {
  return <section className="conversation-section">
    <h3>创建 Issue</h3><p>{event.title}</p>
    <dl>
      <Fact label="创建时间"><time dateTime={event.at}>{formatTime(event.at)}</time></Fact>
      <Fact label="Issue"><code>{event.issueId}</code></Fact>
    </dl>
    <a href={viewHref('issue', event.issueId)}>查看 Issue →</a>
  </section>
}

function DispatchDetails({ event, previewOptions }: { event: WorkerDispatch; previewOptions: boolean }) {
  const delivery = useRecord<WorkerDelivery>('internal-deliveries/' + encodeURIComponent(event.deliveryId))
  const content = delivery.data?.content
  const body = delivery.data?.body
  const refs = delivery.data?.refs
  const revision = refs?.find(ref => ref.kind === 'issue' && ref.id === event.issueId)?.goal_revision
  const materials = body?.materials || []
  const messageIds = new Set(refs?.filter(ref => ref.kind === 'trigger_message').map(ref => ref.id))
  for (const material of materials) if (material.messageId) messageIds.add(material.messageId)
  return <>
    {delivery.error && <ReadError error={delivery.error} stale={!!delivery.data}/>}
    <section className="conversation-section">
      <h3>{dispatchLabels[event.dispatchKind]}</h3>
      <p>{event.title}</p>
      {content ? <StructuredContent content={content} previewOptions={previewOptions}/>
        : (body?.instruction || event.summary) && <p className="conversation-original">{body?.instruction || event.summary}</p>}
    </section>
    <section className="conversation-section">
      <dl>
        <Fact label="登记时间"><time dateTime={event.at}>{formatTime(event.at)}</time></Fact>
        <Fact label="投递状态">{routingDeliveryLabel(delivery.data?.status || event.status)}</Fact>
        <Fact label="Execution"><code>{event.executionId}</code> · 第 {event.generation} 次执行</Fact>
        {revision && <Fact label="目标修订">{revision}</Fact>}
      </dl>
      <div className="conversation-links">
        <a href={viewHref('issue', event.issueId)}>查看 Issue →</a>
        <a href={recordHref('executions', event.executionId)}>查看 Execution →</a>
        <a href={recordHref('internal-deliveries', event.deliveryId)}>查看投递记录 →</a>
      </div>
    </section>
    {(materials.some(material => material.uri) || messageIds.size > 0) && <section className="conversation-section">
      <h3>投递材料</h3>
      <ul className="conversation-references">
        {materials.map((material, index) => material.uri && <li key={index}>{material.label && `${material.label} · `}<code>{material.uri}</code></li>)}
        {[...messageIds].map(id => <li key={id}>
          <a href={viewHref('message', id)}>{materials.find(material => material.messageId === id)?.label || id} →</a>
        </li>)}
      </ul>
    </section>}
  </>
}

function ReportDetails({ event }: { event: WorkerReport }) {
  const report = useRecord<Report>('reports/' + encodeURIComponent(event.reportId))
  const issue = useRecord<Issue>('issues/' + encodeURIComponent(event.issueId))
  const execution = useRecord<Execution>('executions/' + encodeURIComponent(event.executionId))
  const completion = issue.data?.status === 'completed' ? issue.data.completion : undefined
  return <>
    {[report, issue, execution].map((result, index) =>
      result.error && <ReadError key={index} error={result.error} stale={!!result.data}/>)}
    <section className="conversation-section">
      <h3>Worker Report · {reportLabels[event.reportKind]}</h3>
      <p className="conversation-original">{report.data?.summary || event.summary}</p>
      {report.data?.details && <p className="conversation-original">{report.data.details}</p>}
    </section>
    <section className="conversation-section">
      <dl>
        <Fact label="提交时间"><time dateTime={event.at}>{formatTime(event.at)}</time></Fact>
        <Fact label="Issue">{event.title}</Fact>
        <Fact label="Execution"><code>{event.executionId}</code> · 第 {event.generation} 次执行</Fact>
        {report.data?.goalRevision && <Fact label="目标修订">{report.data.goalRevision}</Fact>}
        {event.reportKind === 'ready' && issue.data && !completion && <Fact label="验收状态">待 Topic 验收</Fact>}
      </dl>
      <div className="conversation-links">
        <a href={viewHref('issue', event.issueId)}>查看 Issue →</a>
        <a href={recordHref('executions', event.executionId)}>查看 Execution →</a>
        <a href={recordHref('reports', event.reportId)}>查看 Report →</a>
        <a href={recordHref('internal-deliveries', event.deliveryId)}>查看通知投递 →</a>
        {execution.data?.startEventId && <a href={recordHref('internal-deliveries', execution.data.startEventId)}>查看启动投递 →</a>}
      </div>
    </section>
    <DetailList title="Report 证据" items={report.data?.evidence}/>
    <DetailList title="待确认问题" items={report.data?.questions}/>
    {completion && <section className="conversation-section">
      <h3>Issue 已验收</h3>
      <p className="conversation-original">{completion.reason}</p>
      <time dateTime={completion.completedAt}>{formatTime(completion.completedAt)}</time>
    </section>}
    <DetailList title="Issue 验收证据" items={completion?.evidence}/>
  </>
}

function DetailList({ title, items }: { title: string; items?: string[] }) {
  if (!items?.length) return null
  return <section className="conversation-section">
    <h3>{title}</h3>
    <ul className="conversation-references">{items.map((item, index) => <li key={index}>{item}</li>)}</ul>
  </section>
}

function GroupInfo({ topicId, avatars }: { topicId: string; avatars: boolean }) {
  const { online } = useRefresh()
  const params = new URLSearchParams({ topicId, preview: 'true', order: 'newest', limit: '10' })
  const subscriptions = usePages<Subscription>('subscriptions?' + params)
  const issues = usePages<Issue>('issues?' + params)
  return <>
    <section className="conversation-section">
      <h3>订阅的 Thread</h3>
      {subscriptions.error && <ReadError error={subscriptions.error} stale={!!subscriptions.data}/>}
      {subscriptions.data?.length === 0 && <p className="conversation-muted">暂无订阅</p>}
      <ul className="conversation-references">
        {subscriptions.data?.map(subscription => <li key={subscription.id}><ThreadLink threadKey={subscription.threadKey}/></li>)}
      </ul>
      {subscriptions.next && <Button size="sm" variant="ghost" disabled={!online || subscriptions.loading}
        onClick={() => void subscriptions.more()}>加载更多 Thread</Button>}
    </section>
    <section className="conversation-section">
      <h3>关联 Issue</h3>
      {issues.error && <ReadError error={issues.error} stale={!!issues.data}/>}
      {issues.data?.length === 0 && <p className="conversation-muted">暂无 Issue</p>}
      <ul className="conversation-references conversation-issues">
        {issues.data?.map(issue => <li key={issue.id} className={avatars ? 'prototype-group-issue' : undefined}>
          {avatars && <ObjectAvatar {...issueParticipant(issue.id, issue.title)}/>}
          <a href={viewHref('issue', issue.id)}><strong>{issue.title}</strong><small>{stateLabel(issue.status)}</small></a>
        </li>)}
      </ul>
      {issues.next && <Button size="sm" variant="ghost" disabled={!online || issues.loading}
        onClick={() => void issues.more()}>加载更多 Issue</Button>}
    </section>
    <section className="conversation-section"><a href={viewHref('topic', topicId)}>查看 Topic 记录 →</a></section>
  </>
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <div><dt>{label}</dt><dd>{children}</dd></div>
}

function ReadError({ error, stale }: { error: string; stale: boolean }) {
  return <p className="read-error" role="alert">{error}{stale && ' · 快照已过时'}</p>
}
