import CopyLink from './CopyLink'
import PerspectiveFlow, { type FlowLane, type FlowNode } from './PerspectiveFlow'
import { useLayoutEffect, useRef, useState } from 'react'
import { ArrowUpRight, ChevronDown, ChevronRight, GitBranch, List, Undo2 } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, messageTitle, eventStatusLabel, sourceLabel, type Page } from '../workbench'
import { navigate, usePages, useRecord, viewHref, type ViewProps } from './observer'
import { TraceLinks, type TraceAssociation } from './TraceLinks'
import { metricsHref } from './MetricsView'
import './router.css'

type RouterMessage = {
  id: string; text: string; source: string; state: string
  activityAt: string; activityId: string; activityKind: string; activityReason?: string
  returnedEventId?: string; activityTopicId?: string; activityTopicTitle?: string
  targets: { id: string; topicId: string; title?: string; reason?: string }[]
}
type InternalEvent = {
  event_id: string; kind: string; status: string; createdAt: string; sentAt?: string
  sender: { kind: string }; recipient: { kind: string }; topicId?: string
  returnedEventId?: string; returned: boolean; body?: string; traceAssociations?: TraceAssociation[]
}
const outcomes: Record<string, string> = { pending: '待决策', routed: '已分发', ignored: '已忽略' }

function activity(kind: string, returned?: string, sender?: string) {
  if (returned) return '退回'
  if (kind === 'route_request') return '路由请求'
  return sender === 'core' ? '直投' : '分发'
}

function TopicLink({ id, title }: { id: string; title?: string }) {
  return <a className="router-topic" href={viewHref('topic', id)}>{title || id}<ArrowUpRight size={12}/></a>
}

function EventFacts({ row }: { row: InternalEvent }) {
  return <dl className="router-facts">
    <dt>事件 ID</dt><dd>{row.event_id}</dd>
    <dt>Trace</dt><dd><TraceLinks associations={row.traceAssociations} at={row.createdAt} label={false}/></dd>
    <dt>登记时间</dt><dd><time dateTime={row.createdAt}>{row.createdAt}</time></dd>
    {row.sentAt && <><dt>Session 受理时间</dt><dd><time dateTime={row.sentAt}>{row.sentAt}</time></dd></>}
    {(row.kind === 'routed_message' || row.returnedEventId) && <>
      <dt>{row.returnedEventId ? '退回理由' : row.sender.kind === 'core' ? '直投依据' : '分发理由'}</dt><dd className="router-full-reason">{row.body || '原因未记录'}</dd>
    </>}
  </dl>
}

function OriginalRoutingEvent({ id }: { id: string }) {
  const [open, setOpen] = useState(false)
  const record = useRecord<InternalEvent>(open ? 'routing-events/' + encodeURIComponent(id) + '?trace=true' : '')
  return <div className="router-original">
    <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}><Undo2 size={13}/>原事件 {id}{open ? <ChevronDown size={14}/> : <ChevronRight size={14}/>}</button>
    {open && <div>{record.error && <p role="alert" className="read-error">{record.error}{record.data && ' · 快照已过时'}</p>}
      {record.data ? <EventFacts row={record.data}/> : !record.error && <p>正在读取原事件…</p>}</div>}
  </div>
}

function RouterHistory({ message }: { message: RouterMessage }) {
  const list = usePages<InternalEvent>('routing-events?' + new URLSearchParams({ messageId: message.id, limit: '20', trace: 'true' }))
  return <section className="router-history" aria-label="路由事件记录">
    <div className="router-history-heading"><h3>路由事件记录</h3><a href={metricsHref('router')}>Router 用量</a><CopyLink href={location.pathname+location.search}/><a href={viewHref('message', message.id)}>查看消息路径<ArrowUpRight size={14}/></a></div>
    {list.error && <p role="alert" className="read-error">{list.error}{list.data && ' · 快照已过时'}</p>}
    {!list.data && !list.error && <p className="empty">正在读取事件…</p>}
    {list.data?.length === 0 && <p className="empty">事件记录缺失</p>}
    <ol>{list.data?.map(row => {
      const action = activity(row.kind, row.returnedEventId, row.sender.kind)
      const title = message.targets.find(t => t.topicId === row.topicId)?.title || (message.activityTopicId === row.topicId ? message.activityTopicTitle : undefined)
      const topic = row.topicId && <TopicLink id={row.topicId} title={title}/>
      const label = (kind: string) => kind === 'topic' ? topic || 'Topic' : kind === 'router' ? 'Router' : kind === 'core' ? 'Core' : kind
      return <li key={row.event_id}>
        <div className="router-event"><strong>{action}</strong><span>{label(row.sender.kind)} → {label(row.recipient.kind)}</span><span className="router-status">{eventStatusLabel(row.status)}{row.returned && ' · 已退回'}</span></div>
        <EventFacts row={row}/>
        {row.returnedEventId && <OriginalRoutingEvent id={row.returnedEventId}/>}
      </li>
    })}</ol>
    {list.next && <Button variant="ghost" size="sm" onClick={list.more} disabled={list.loading}>加载更多事件<ChevronDown/></Button>}
  </section>
}

function routerLanes(row: RouterMessage, limit: string): FlowLane[] {
  const message: FlowNode = { kind: 'message', label: messageTitle({ text: row.text }), href: viewHref('message', row.id) }
  const router: FlowNode = { kind: 'router', label: 'Router', href: viewHref('router', row.id, { limit }) }
  const state = outcomes[row.state] || '状态未记录'
  const lanes: FlowLane[] = []
  function evidence(id: string, reason?: string) {
    return <div className="router-flow-evidence">
      {reason && <p>{reason}</p>}
      <a href={'/observer/?' + new URLSearchParams({ view: 'routing-events', id })}>事件记录 · {id}<ArrowUpRight size={13}/></a>
    </div>
  }
  function edge(action: string, reason?: string) {
    const text = reason || '原因未记录'
    const chars = Array.from(text)
    return action + ' · ' + (chars.length > 24 ? chars.slice(0, 24).join('') + '…' : text)
  }
  if (row.returnedEventId && row.activityTopicId) {
    const topic: FlowNode = { kind: 'topic', label: row.activityTopicTitle || row.activityTopicId, href: viewHref('topic', row.activityTopicId) }
    lanes.push({ id: row.activityId, nodes: [message, topic, router], edges: ['原事件目标', edge('退回', row.activityReason)],
      detail: <>{evidence(row.activityId, row.activityReason)}<OriginalRoutingEvent id={row.returnedEventId}/></> })
  }
  for (const target of row.targets) {
    lanes.push({ id: target.id, nodes: [message, router, { kind: 'topic', label: target.title || target.topicId, href: viewHref('topic', target.topicId) }],
      edges: ['进入路由', edge('分发', target.reason)], detail: evidence(target.id, target.reason) })
  }
  // A Message can be pending again while another branch still has an active target.
  if (!row.targets.length || row.state !== 'routed') {
    const outcome = row.state === 'ignored' ? '已忽略 · 原因未记录'
      : row.state === 'routed' && !row.targets.length ? '已分发 · 目标事件记录缺失' : state
    lanes.push({ id: row.id + '-outcome', nodes: [message, router, { kind: 'state', label: outcome }], edges: ['进入路由', '当前结果'],
      detail: <div className="router-flow-evidence"><p>最后可核实活动 · {activity(row.activityKind, row.returnedEventId)}登记 <time dateTime={row.activityAt}>{row.activityAt}</time></p>{evidence(row.activityId)}</div> })
  }
  return lanes
}

function RouterFlow({ messages, selected, limit }: { messages: RouterMessage[]; selected?: RouterMessage; limit: string }) {
  const shown = selected ? [selected] : messages
  const counts = Object.entries(outcomes).map(([state, label]) => {
    const count = messages.filter(row => row.state === state).length
    return count ? label + ' ' + count : ''
  }).filter(Boolean)
  const scope = selected ? '当前消息' : '按最近活动排序'
  return <div className="router-flow" data-testid="router-flow"><PerspectiveFlow title="Router 处理流程"
    description={['已载入 ' + messages.length + ' 条消息', ...counts, scope].join(' · ')}
    lanes={shown.flatMap(row => routerLanes(row, limit))}/></div>
}

function RouterRow({ row, open, toggle }: { row: RouterMessage; open: boolean; toggle: () => void }) {
  const action = activity(row.activityKind, row.returnedEventId)
  return <article className={'router-row' + (open ? ' expanded' : '')} data-message-id={row.id}>
    <div className="router-grid">
      <button className="router-toggle" aria-expanded={open} onClick={toggle}>
        {open ? <ChevronDown size={16}/> : <ChevronRight size={16}/>}<span><small>{sourceLabel(row.source)}</small><strong>{messageTitle({ text: row.text })}</strong></span>
      </button>
      <div><span className={'router-outcome ' + row.state}>{outcomes[row.state] || '状态未记录'}</span></div>
      <div className="router-reasons">
        {row.state === 'ignored' && <span>原因未记录</span>}
        {row.returnedEventId && <div className="router-brief"><small>退回{row.activityTopicId && <> · <TopicLink id={row.activityTopicId} title={row.activityTopicTitle}/></>}</small><span>{row.activityReason || '原因未记录'}</span></div>}
        {row.targets.map(target => <div key={target.id} className="router-brief"><TopicLink id={target.topicId} title={target.title}/><span>{target.reason || '原因未记录'}</span></div>)}
        {!row.targets.length && row.state === 'routed' && <span className="router-muted">目标事件记录缺失</span>}
        {!row.targets.length && !row.returnedEventId && row.state === 'pending' && <span className="router-muted">等待 Router 选择目标</span>}
      </div>
      <div className="router-time"><small>{action}登记</small><time dateTime={row.activityAt} title={row.activityAt}>{formatTime(row.activityAt)}</time></div>
    </div>
    {open && <RouterHistory message={row}/>}
  </article>
}

export default function RouterView({ query }: ViewProps) {
  const value = query.get('limit') || ''
  const limit = ['50', '100'].includes(value) ? value : '20'
  const selected = query.get('id') || ''
  const path = 'router-messages?limit=' + limit
  const list = usePages<RouterMessage>(path)
  const onPage = list.data?.find(row => row.id === selected)
  const selectedRecord = useRecord<Page<RouterMessage>>(selected && (list.data || list.error) && !onPage ? 'router-messages?messageId=' + encodeURIComponent(selected) : '')
  const scroll = useRef<HTMLDivElement>(null)
  const restored = useRef('')
  useLayoutEffect(() => {
    if (!list.data || !scroll.current || restored.current === path) return
    scroll.current.scrollTop = history.state?.observerScroll?.[path] || 0
    restored.current = path
  }, [list.data, path])
  const lastSelected = useRef<RouterMessage | undefined>(undefined)
  const currentSelection = onPage || selectedRecord.data?.items[0]
  useLayoutEffect(() => {
    if (currentSelection) lastSelected.current = currentSelection
  }, [currentSelection])
  const pinned = !onPage && (currentSelection || (!selectedRecord.data && lastSelected.current?.id === selected ? lastSelected.current : undefined))
  const rows = pinned ? [pinned, ...list.data || []] : list.data || []
  function toggle(id: string) {
    navigate(viewHref('router', id === selected ? '' : id, { limit }))
  }
  const flowSelection = selected ? rows.find(row => row.id === selected) : undefined
  const showFlow = selected ? Boolean(flowSelection) : Boolean(list.data?.length)
  return <div className="router-workspace">
    {showFlow && <RouterFlow messages={list.data || []} selected={flowSelection} limit={limit}/>}
    <section className="panel router-panel" aria-label="最近路由消息">
    <header className="panel-heading"><div className="panel-title"><GitBranch size={17}/><h2>最近路由消息</h2></div>
      <label className="router-limit">每页<select value={limit} onChange={event => navigate(viewHref('router', selected, { limit: event.target.value }))}>
        {[20, 50, 100].map(n => <option value={n} key={n}>{n} 条</option>)}</select></label></header>
    <div className="router-columns router-grid" aria-hidden="true"><span>消息</span><span>当前结果</span><span>目标与理由</span><span>最近活动</span></div>
    {list.error && <p role="alert" className="read-error">{list.error}{list.data && ' · 快照已过时'}</p>}
    {selectedRecord.error && <p role="alert" className="read-error">{selectedRecord.error}{selectedRecord.data && ' · 快照已过时'}</p>}
    {selectedRecord.data?.items.length === 0 && <p className="empty">此消息的 Router 记录缺失</p>}
    <div className="object-scroll router-scroll" ref={scroll} onScroll={event => {
      history.replaceState({ ...history.state, observerScroll: { ...history.state?.observerScroll, [path]: event.currentTarget.scrollTop } }, '')
    }}>
      {!list.data && !list.error && <p className="empty">正在读取路由消息…</p>}
      {list.data?.length === 0 && <p className="empty">暂无交给 Router 的消息</p>}
      {rows.map(row => <RouterRow row={row} key={row.id} open={selected === row.id} toggle={() => toggle(row.id)}/>)}
      {list.next && <Button variant="ghost" className="load-more" disabled={list.loading} onClick={list.more}>加载更多消息<ChevronDown/></Button>}
    </div>
    {list.data && <footer className="list-footer"><List size={14}/>已显示 {list.data.length} 条消息</footer>}
  </section></div>
}
