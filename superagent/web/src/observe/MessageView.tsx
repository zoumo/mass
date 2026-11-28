import CopyLink from './CopyLink'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowLeft, Check, ChevronDown, ChevronRight, Clock3, Inbox, ListFilter, Mail, Text } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, messageBody, messageTitle, sourceLabel, stateLabel } from '../workbench'
import { observerHome, navigate, usePages, useRecord, viewHref, type MessageRecord, type ViewProps } from './observer'
import MessagePath from './MessagePath'

export default function MessageView({ query }: ViewProps) {
  // A changed Thread starts its own list and selection.
  return <Messages key={query.get('threadId') || ''} query={query}/>
}

function Messages({ query }: ViewProps) {
  const threadId = query.get('threadId') || ''
  const requested = query.get('id') || ''
  const [draft, setDraft] = useState(threadId)
  const [filterOpen, setFilterOpen] = useState(Boolean(threadId))
  const [lastSelected, setLastSelected] = useState('')
  const params = new URLSearchParams({ limit: '50', preview: 'true', order: 'newest' })
  if (threadId) params.set('threadId', threadId)
  const path = 'messages?' + params
  const list = usePages<MessageRecord>(path)
  const listRef = useRef<HTMLDivElement>(null)
  const restored = useRef(false)
  const previousSelection = useRef(requested)
  useLayoutEffect(() => {
    if (restored.current || !list.data?.length || !listRef.current) return
    listRef.current.scrollTop = history.state?.observerScroll?.[path] || 0
    restored.current = true
  }, [path, list.data])
  useEffect(() => {
    if (requested) setLastSelected(requested)
    else if (!lastSelected && list.data?.length) setLastSelected(list.data[0].id)
  }, [requested, list.data, lastSelected])
  const id = requested || lastSelected
  const [wide, setWide] = useState(() => matchMedia('(min-width: 761px)').matches)
  useEffect(() => {
    const media = matchMedia('(min-width: 761px)')
    const onChange = () => setWide(media.matches)
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])
  useLayoutEffect(() => {
    if (!wide && !requested && previousSelection.current && listRef.current) {
      const target = Array.from(listRef.current.querySelectorAll<HTMLAnchorElement>('.message-row'))
        .find(link => new URL(link.href).searchParams.get('id') === previousSelection.current)
      target?.focus({ preventScroll: true })
    }
    previousSelection.current = requested
  }, [requested, wide])
  const displayed = requested || (wide ? id : '')
  const detail = useRecord<MessageRecord>(displayed ? 'messages/' + encodeURIComponent(displayed) + '?trace=true' : '')
  const listHref = viewHref('message', '', { threadId })
  return <div className={'split-view message-layout' + (requested ? ' has-selection' : '')}>
    <aside className="panel object-list" aria-label="最近消息">
      <div className="panel-heading"><div className="panel-title"><Inbox size={17}/><h2>最近消息</h2></div>
        <Button variant="ghost" size="icon-sm" title="按线程 ID 筛选" aria-label="按线程 ID 筛选" aria-expanded={filterOpen} onClick={() => setFilterOpen(!filterOpen)}><ListFilter/></Button>
      </div>
      {filterOpen && <form className="thread-form" onSubmit={event => { event.preventDefault(); navigate(viewHref('message', '', { threadId: draft.trim() })) }}>
        <label htmlFor="thread-id">Thread 筛选</label><div><input id="thread-id" aria-label="按线程 ID 筛选" value={draft} placeholder="输入完整线程 ID" onChange={e => setDraft(e.target.value)}/><Button variant="outline" size="sm">筛选</Button></div>
        {threadId && <a href={viewHref('message')}>清除筛选</a>}
      </form>}
      {list.error && <p role="alert" className="read-error">{list.error}{list.data && ' · 快照已过时'}</p>}
      <div className="object-scroll" ref={listRef} onScroll={event => {
        if (event.currentTarget.clientHeight) history.replaceState({ ...history.state, observerScroll: { ...history.state?.observerScroll, [path]: event.currentTarget.scrollTop } }, '')
      }}>
        {list.data?.map(row => <a href={viewHref('message', row.id, { threadId })} key={row.id} className="message-row" aria-current={id === row.id ? 'true' : undefined}>
          <span className="row-meta"><span><i/>{sourceLabel(row.source)}</span><time dateTime={row.receivedAt}>{formatTime(row.receivedAt)}</time></span>
          <span className="row-title">{messageTitle(row)}</span>
          <span className="row-bottom">{row.id}<ChevronRight size={14}/></span>
        </a>)}
        {!list.data && !list.error && <p className="empty">正在读取消息…</p>}
        {list.data?.length === 0 && <p className="empty">当前范围暂无消息</p>}
        {list.next && <Button className="load-more" variant="ghost" onClick={list.more} disabled={list.loading}>加载更多<ChevronDown/></Button>}
      </div>
      {list.data && <div className="list-footer">已显示 {list.data.length} 条消息</div>}
    </aside>
    <section className="detail-column" aria-label="消息详情">
      {requested && <a className="mobile-back" href={listHref} onClick={event => {
        const prev = history.state?.observerPrevious
        if (prev === listHref) { event.preventDefault(); history.back() }
      }}><ArrowLeft size={17}/>返回消息列表</a>}
      {detail.error && <p role="alert" className="read-error">{detail.error}{detail.data && ' · 快照已过时'}</p>}
      {detail.data ? <MessageDetail key={detail.data.id} row={detail.data} threadId={threadId}/> :
        <div className="empty panel">{displayed ? detail.error ? '无法显示此消息' : '正在读取消息详情…' : '从列表选择一条消息'}</div>}
    </section>
  </div>
}

function MessageDetail({ row, threadId }: { row: MessageRecord, threadId: string }) {
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    if (new URLSearchParams(location.search).get('id') && matchMedia('(max-width: 760px)').matches) {
      heading.current?.focus({ preventScroll: true })
    }
  }, [])
  return <article aria-label="当前消息" className="message-detail">
    <div className="panel original">
      <div className="panel-heading"><div className="panel-title"><Mail size={17}/><span>消息详情</span></div>
        <div className="observable-actions"><CopyLink href={viewHref('message', row.id, {threadId})}/></div></div>
      <div className="detail-body"><div className="source-line"><span className="source-tag">{sourceLabel(row.source)}</span><span className="acceptance"><Check size={13}/>{stateLabel(row.acceptance)}</span></div>
        <h2 ref={heading} tabIndex={-1}>{messageTitle(row)}</h2>
        <div className="message-meta"><span><Clock3/><time dateTime={row.receivedAt}>{formatTime(row.receivedAt)}</time></span></div>
        <details className="original-text"><summary><Text size={16}/><span>查看全文</span><ChevronDown size={15}/></summary><pre data-testid="message-body" tabIndex={0}>{messageBody(row) || '渠道未提供正文。'}</pre></details>
        <div className="topic-links"><a href={observerHome + '?' + new URLSearchParams({view:'messages',mode:'records',id:row.id})}>完整记录</a>
          {row.threadId && <a href={observerHome + '?' + new URLSearchParams({view:'threads',id:row.threadId})}>查看线程</a>}</div>
      </div>
    </div>
    <MessagePath message={row}/>
  </article>
}
