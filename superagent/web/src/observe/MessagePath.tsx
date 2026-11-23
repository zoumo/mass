import { useLayoutEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react'
import { ArrowDownRight, ArrowRight, CheckCheck, CircuitBoard, CornerDownRight, FileCheck2, GitBranch, Layers, Mail, Route, X } from 'lucide-react'
import { eventStatusLabel, messageBody, sourceLabel, stateLabel, type WorkbenchRow } from '../workbench'
import { usePages, useRecord, viewHref, type MessageRecord } from './observer'
import { TraceLinks, type TraceAssociation } from './TraceLinks'
import './message-path.css'

type Party = { kind: string, topic_id?: string }
type InternalEvent = {
  event_id: string, kind: string, createdAt: string, sentAt?: string, status: string,
  sender: Party, recipient: Party, topicId?: string, topicTitle?: string, returnedEventId?: string,
  traceAssociations?: TraceAssociation[]
  body?: string, refs?: { kind: string, id: string }[]
}
type Issue = WorkbenchRow & { id: string, topicId: string, topicTitle?: string }
type Reply = { traceAssociations?: TraceAssociation[]; createdAt?: string; id: string, status: string, preview: string, receipt?: { taskId?: string, errorCode?: string } }
type Selection = { kind: 'channel' | 'core' | 'router' | 'topic' | 'issue' | 'event', id: string }
type Toggle = (event: MouseEvent<HTMLButtonElement>, kind: Selection['kind'], id?: string) => void

function action(row: InternalEvent) {
  if (row.returnedEventId) return '退回'
  if (row.kind === 'route_request') return '请求路由'
  return row.sender.kind === 'core' ? '直投' : '分发'
}
function partyLabel(party: Party) {
  return ({ core: 'Core', router: 'Router', topic: 'Topic' }[party.kind] || party.kind) + (party.topic_id ? ' · ' + party.topic_id : '')
}
function Fields({ fields }: { fields: [string, ReactNode][] }) {
  return <dl className="path-fields">{fields.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value || '未记录'}</dd></div>)}</dl>
}
function PageStatus({ page, name }: { page: ReturnType<typeof usePages>, name: string }) {
  return <>{page.error && <p className="path-error" role="status">{name} · {page.error}{page.data && ' · 快照已过时'}</p>}
    {!page.data && !page.error && <p className="path-muted">正在读取{name}…</p>}
    {page.next && <button className="path-more" onClick={page.more} disabled={page.loading}>加载更多{name}</button>}</>
}
function Node({ kind, id = '', label, selected, toggle }: {
  kind: Exclude<Selection['kind'], 'event'>, id?: string, label: string, selected: Selection | null, toggle: Toggle
}) {
  const icons = { channel: Mail, core: CircuitBoard, router: Route, topic: Layers, issue: FileCheck2 }
  const Icon = icons[kind]
  return <button type="button" className={'path-node path-' + kind} onClick={e => toggle(e, kind, id)}
    aria-label={kind === 'topic' ? 'Topic ' + label : kind === 'issue' ? 'Issue ' + label : label}
    aria-expanded={selected?.kind === kind && selected.id === id} aria-controls="path-selection">
    <Icon size={16} aria-hidden="true"/><span>{(kind === 'topic' || kind === 'issue') && <small>{kind === 'topic' ? 'Topic ' : 'Issue '}</small>}{label}</span>
  </button>
}
function Edge({ row, selected, toggle }: { row: InternalEvent, selected: Selection | null, toggle: Toggle }) {
  return <div className="path-edge-trace"><button type="button" className="path-edge" data-event-id={row.event_id}
    aria-controls="path-selection" aria-expanded={selected?.kind === 'event' && selected.id === row.event_id}
    onClick={e => toggle(e, 'event', row.event_id)}>
    <span>{action(row)}<ArrowRight size={15}/></span><small>{eventStatusLabel(row.status)}</small>
  </button>{!!row.traceAssociations?.length && <TraceLinks associations={row.traceAssociations} at={row.createdAt}/>}</div>
}

export default function MessagePath({ message }: { message: MessageRecord }) {
  const query = '?messageId=' + encodeURIComponent(message.id) + '&preview=true&trace=true'
  const internalEvents = usePages<InternalEvent>('internal-events' + query + '&limit=20')
  const issues = usePages<Issue>('issues' + query + '&limit=6')
  const replies = usePages<Reply>('outbox' + query + '&limit=6')
  const [selected, select] = useState<Selection | null>(null)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const detail = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    if (!selected || !detail.current) return
    detail.current.focus({ preventScroll: true })
    detail.current.scrollIntoView({ block: 'center' })
  }, [selected])
  function close() { select(null); trigger.current?.focus() }
  const toggle: Toggle = (event, kind, id = '') => {
    if (selected?.kind === kind && selected.id === id) { close(); return }
    trigger.current = event.currentTarget
    select({ kind, id })
  }
  const node = (kind: Exclude<Selection['kind'], 'event'>, label: string, id = '') =>
    <Node kind={kind} id={id} label={label} selected={selected} toggle={toggle}/>
  const edge = (row: InternalEvent) => <Edge row={row} selected={selected} toggle={toggle}/>
  const rows = internalEvents.data || []
  const initial = rows.find(d => d.kind === 'route_request' && d.sender.kind === 'core')
  const targets = rows.filter(row => row.kind === 'routed_message')
  const batches: { sender: string, targets: InternalEvent[] }[] = []
  const latestTarget = new Map<string, string>()
  for (const target of targets) {
    const previous = batches[batches.length - 1]
    if (previous?.sender === target.sender.kind) previous.targets.push(target)
    else batches.push({ sender: target.sender.kind, targets: [target] })
    if (target.topicId) latestTarget.set(target.topicId, target.event_id)
  }
  const returnByOrigin = new Map(rows.filter(row => row.returnedEventId).map(row => [row.returnedEventId!, row]))
  const issuesByTopic = new Map<string, Issue[]>()
  for (const issue of issues.data || []) {
    const group = issuesByTopic.get(issue.topicId) || []
    group.push(issue)
    issuesByTopic.set(issue.topicId, group)
  }
  const evidencedTopics = new Set((message.relations || []).filter(rel => rel.action === 'received').map(rel => rel.topicId))
  for (const row of targets) if (row.topicId) evidencedTopics.add(row.topicId)
  const materialOnly = [...issuesByTopic].filter(([id]) => !evidencedTopics.has(id))
  const source = sourceLabel(message.source)
  function references(items: Issue[]) {
    return <ul className="path-references">{items.map(issue => <li key={issue.id}>
      <button className="reference-edge" onClick={e => toggle(e, 'issue', issue.id)} aria-controls="path-selection"
        aria-expanded={selected?.kind === 'issue' && selected.id === issue.id}><CornerDownRight size={14}/>引用本消息</button>
      {node('issue', issue.title || issue.id, issue.id)}</li>)}</ul>
  }
  function returned(row: InternalEvent) {
    return <div className="path-return" data-origin-id={row.returnedEventId}>
      <CornerDownRight size={17} aria-label="原事件退回"/>{edge(row)}{node('router', 'Router')}
    </div>
  }
  const inlet = <div className="path-trunk">{node('channel', source)}
    <button className="path-edge" onClick={e => toggle(e, 'core')} aria-controls="path-selection" aria-expanded={selected?.kind === 'core'}>
      <span>{stateLabel(message.acceptance)}<ArrowRight size={15}/></span></button>{node('core', 'Core')}
    {initial && <>{edge(initial)}{node('router', 'Router')}</>}
  </div>
  const inletSender = initial ? 'router' : 'core'
  const result = message.routing?.state
  return <section className="panel path-panel" aria-label="消息路径">
    <div className="panel-heading"><div className="panel-title"><GitBranch size={17}/><h2>消息路径</h2></div>
      {result && <span className={'path-result state-' + result}>{result === 'pending' ? '待决策' : result === 'ignored' ? '已忽略 · 原因未记录' : stateLabel(result)}</span>}</div>
    <div className="path-canvas">
      {batches[0]?.sender !== inletSender && inlet}
      {batches.map((batch, index) => <div className="path-batch" key={batch.targets[0].event_id}>
        <div className="path-sender">{index === 0 && batch.sender === inletSender ? inlet :
          batch.sender === 'router' ? node('router', 'Router') : node('core', 'Core')}</div>
        <ol className="path-branches">{batch.targets.map(row => {
          const topicID = row.recipient.topic_id
          const refs = topicID && latestTarget.get(topicID) === row.event_id && issuesByTopic.get(topicID)
          const back = returnByOrigin.get(row.event_id)
          return <li key={row.event_id} data-target-event={row.event_id}>
            <div className="path-leg">{edge(row)}{topicID && node('topic', row.topicTitle || topicID, topicID)}</div>
            {back && returned(back)}
            {refs && references(refs)}
          </li>
        })}</ol>
      </div>)}
      {rows.filter(row => row.returnedEventId && !targets.some(target => target.event_id === row.returnedEventId)).map(row =>
        <div className="path-leg" key={row.event_id}>{row.sender.topic_id && node('topic', row.topicTitle || row.sender.topic_id, row.sender.topic_id)}{returned(row)}</div>)}
      <PageStatus page={internalEvents} name="路由事件"/>
      {internalEvents.data?.length === 0 && !internalEvents.next && !internalEvents.error && <p className="path-muted">暂无路由事件记录</p>}
      {internalEvents.data && !internalEvents.next && !internalEvents.error && materialOnly.length > 0 && <section className="path-materials"><h3>材料引用 · 缺少对应 Topic 事件记录</h3>
        {materialOnly.map(([id, items]) => <div key={id}>{node('topic', items[0].topicTitle || id, id)}{references(items)}</div>)}</section>}
      <PageStatus page={issues} name="Issue 引用"/>
      {issues.data?.length === 0 && !issues.error && !issues.next && <p className="path-muted">尚无 Issue 材料引用本消息</p>}
    </div>
    <div className="path-tools"><button onClick={e => toggle(e, 'core')} aria-controls="path-selection" aria-expanded={selected?.kind === 'core'}><CheckCheck size={16}/>Core 接收记录</button></div>
    {selected && <section id="path-selection" className="path-selection" role="region" aria-label="路径详情" tabIndex={-1} ref={detail}
      onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); close() } }}>
      <div className="path-detail-title"><h3>{{ core: 'Core 接收记录', channel: '来源渠道', router: 'Router 处理记录', issue: 'Issue 材料引用', topic: '目标 Topic', event: '事件详情' }[selected.kind]}</h3>
        <button onClick={close} aria-label="收起路径详情"><X size={17}/></button></div>
      {selected.kind === 'event' && <InternalEventDetail id={selected.id}/>}
      {selected.kind === 'core' && <Fields fields={[['消息 ID', message.id], ['渠道消息 ID', message.sourceMessageId], ['接收状态', stateLabel(message.acceptance)], ['登记时间', message.receivedAt], ['Thread', message.threadId], ['Trace', <TraceLinks associations={message.traceAssociations} at={message.receivedAt}/>]]}/>}
      {selected.kind === 'channel' && <Fields fields={[['渠道', source], ['渠道消息 ID', message.sourceMessageId], ['原文', messageBody(message)]]}/>}
      {selected.kind === 'router' && <><Fields fields={[['本消息路由状态', result === 'pending' ? '待决策' : result === 'ignored' ? '已忽略 · 原因未记录' : stateLabel(result)], ['当前轮次', message.routing?.round]]}/>
        <a className="path-link" href={viewHref('router', message.id)}>查看 Router<ArrowRight size={14}/></a></>}
      {(selected.kind === 'topic' || selected.kind === 'issue') && <ObjectDetail kind={selected.kind} id={selected.id} messageID={message.id} references={issuesByTopic.get(selected.id)} incomplete={Boolean(issues.next || !issues.data || issues.error)}/>}
    </section>}
    {(replies.data?.length || replies.next || replies.error) ? <section className="path-outbox"><h3><ArrowDownRight size={16}/>渠道回执 · {source}</h3>
      {replies.data?.map(row => <details key={row.id}><summary><span>{row.preview}</span><b>{row.status === 'sent' ? '渠道已发送' : '渠道 · ' + stateLabel(row.status)}</b></summary>
        <Fields fields={[['Outbox ID', row.id], ['任务 ID', row.receipt?.taskId], ['错误码', row.receipt?.errorCode], ['Trace', <TraceLinks associations={row.traceAssociations} at={row.createdAt}/>]]}/>
        <a className="path-link" href={'/superagent/?view=outbox&id=' + encodeURIComponent(row.id)}>查看渠道回执<ArrowRight size={14}/></a>
      </details>)}<PageStatus page={replies} name="渠道回执"/></section> : null}
  </section>
}

function InternalEventDetail({ id }: { id: string }) {
  const read = useRecord<InternalEvent>('internal-events/' + encodeURIComponent(id) + '?trace=true')
  const row = read.data
  return <>{read.error && <p className="path-error">{read.error}{row && ' · 快照已过时'}</p>}
    {row ? <Fields fields={[['事件 ID', row.event_id], ['发送方', partyLabel(row.sender)], ['接收方', partyLabel(row.recipient)],
      ['Trace', <TraceLinks associations={row.traceAssociations} at={row.createdAt}/>], ['动作', action(row)], ['状态', eventStatusLabel(row.status)], ['登记时间', row.createdAt], ['受理时间', row.sentAt],
      [row.returnedEventId ? '退回理由' : row.kind === 'routed_message' ? '路由依据' : '内容', row.body],
      ['内容引用', row.refs?.map(ref => <p key={ref.kind + ref.id}>{ref.kind} · {ref.id}</p>)],
      ...(row.returnedEventId ? [['原事件 ID', row.returnedEventId] as [string, string]] : [])]}/> : !read.error && <p className="path-muted">正在读取事件…</p>}</>
}

function ObjectDetail({ kind, id, messageID, references = [], incomplete }: {
  kind: 'topic' | 'issue', id: string, messageID: string, references?: Issue[], incomplete: boolean
}) {
  const read = useRecord<WorkbenchRow>((kind === 'topic' ? 'topics/' : 'issues/') + encodeURIComponent(id))
  const row = read.data
  return <>{read.error && <p className="path-error">{read.error}{row && ' · 快照已过时'}</p>}
    {row ? <><Fields fields={[['名称', row.title], ['ID', id], ['状态', stateLabel(row.status)], ['目标', row.goal], ...(kind === 'issue'
      ? [['所属 Topic', row.topicId], ['材料', row.materials?.filter(m => m.messageId === messageID).map(m => [m.label, m.messageId].filter(Boolean).join(' · ')).join('\n')]] as [string, ReactNode][]
      : [['引用本消息的 Issue', references.length ? references.map(issue => <p key={issue.id}><a href={viewHref('issue', issue.id)}>{issue.title}</a></p>) : incomplete ? '正在核对当前范围' : '暂无引用']] as [string, ReactNode][])]}/>
      <a className="path-link" href={viewHref(kind, id)}>查看此 {kind === 'topic' ? 'Topic' : 'Issue'}<ArrowRight size={14}/></a></> : !read.error && <p className="path-muted">正在读取记录…</p>}</>
}
