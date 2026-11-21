import CopyLink from './CopyLink'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ArrowLeft, Check, ChevronDown, ChevronRight, Clock3, Inbox, ListFilter, Mail, Text, UserRound } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, messageTitle, sourceLabel, stateLabel } from '../workbench'
import { navigate, usePages, useRecord, viewHref, type MessageRecord, type ViewProps } from './observer'
import MessagePath from './MessagePath'

export default function MessageView({ query }: ViewProps) {
  // A changed Thread starts its own list and selection.
  return <Messages key={query.get('threadKey') || ''} query={query}/>
}

function Messages({ query }: ViewProps) {
  const threadKey = query.get('threadKey') || ''
  const requested = query.get('id') || ''
  const [draft, setDraft] = useState(threadKey)
  const [filterOpen, setFilterOpen] = useState(Boolean(threadKey))
  const [lastSelected, setLastSelected] = useState('')
  const params = new URLSearchParams({ limit: '50', preview: 'true', order: 'newest' })
  if (threadKey) params.set('threadKey', threadKey)
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
  const listHref = viewHref('message', '', { threadKey })
  return <div className={'split-view message-layout' + (requested ? ' has-selection' : '')}>
    <aside className="panel object-list" aria-label="最近消息">
      <div className="panel-heading"><div className="panel-title"><Inbox size={17}/><h2>最近消息</h2></div>
        <Button variant="ghost" size="icon-sm" title="按线程键筛选" aria-label="按线程键筛选" aria-expanded={filterOpen} onClick={() => setFilterOpen(!filterOpen)}><ListFilter/></Button>
      </div>
      {filterOpen && <form className="thread-form" onSubmit={event => { event.preventDefault(); navigate(viewHref('message', '', { threadKey: draft.trim() })) }}>
        <label htmlFor="thread-key">Thread 筛选</label><div><input id="thread-key" aria-label="按线程键筛选" value={draft} placeholder="输入完整线程键" onChange={e => setDraft(e.target.value)}/><Button variant="outline" size="sm">筛选</Button></div>
        {threadKey && <a href={viewHref('message')}>清除筛选</a>}
      </form>}
      {list.error && <p role="alert" className="read-error">{list.error}{list.data && ' · 快照已过时'}</p>}
      <div className="object-scroll" ref={listRef} onScroll={event => {
        if (event.currentTarget.clientHeight) history.replaceState({ ...history.state, observerScroll: { ...history.state?.observerScroll, [path]: event.currentTarget.scrollTop } }, '')
      }}>
        {list.data?.map(row => <a href={viewHref('message', row.id, { threadKey })} key={row.id} className="message-row" aria-current={id === row.id ? 'true' : undefined}>
          <span className="row-meta"><span><i/>{sourceLabel(row.source)}</span><time dateTime={row.receivedAt}>{formatTime(row.receivedAt)}</time></span>
          <span className="row-title">{messageTitle(row)}</span>
          <span className="row-bottom">{row.actor || '发送者未记录'}<ChevronRight size={14}/></span>
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
      {detail.data ? <MessageDetail key={detail.data.id} row={detail.data} threadKey={threadKey}/> :
        <div className="empty panel">{displayed ? detail.error ? '无法显示此消息' : '正在读取消息详情…' : '从列表选择一条消息'}</div>}
    </section>
  </div>
}

function MessageDetail({ row, threadKey }: { row: MessageRecord, threadKey: string }) {
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    if (new URLSearchParams(location.search).get('id') && matchMedia('(max-width: 760px)').matches) {
      heading.current?.focus({ preventScroll: true })
    }
  }, [])
  return <article aria-label="当前消息" className="message-detail">
    <div className="panel original">
      <div className="panel-heading"><div className="panel-title"><Mail size={17}/><span>消息详情</span></div>
        <div className="observable-actions"><CopyLink href={viewHref('message', row.id, {threadKey})}/></div></div>
      <div className="detail-body"><div className="source-line"><span className="source-tag">{sourceLabel(row.source)}</span><span className="acceptance"><Check size={13}/>{stateLabel(row.acceptance)}</span></div>
        <h2 ref={heading} tabIndex={-1}>{messageTitle(row)}</h2>
        <div className="message-meta"><span><UserRound/>{row.actor || '发送者未记录'}</span><span><Clock3/><time dateTime={row.receivedAt}>{formatTime(row.receivedAt)}</time></span></div>
        <details className="original-text"><summary><Text size={16}/><span>查看全文</span><ChevronDown size={15}/></summary><pre data-testid="message-body" tabIndex={0}>{row.text || '渠道未提供正文。'}</pre></details>
        <div className="topic-links"><a href={'/observer/?' + new URLSearchParams({view:'messages',mode:'records',id:row.id})}>完整记录</a>
          {row.threadKey && <a href={'/observer/?' + new URLSearchParams({view:'threads',id:row.threadKey})}>查看线程</a>}</div>
      </div>
    </div>
    <MessagePath message={row}/>
  </article>
}
