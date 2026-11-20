import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { ArrowLeft, ChevronRight } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime, stateLabel } from '../workbench'
import { usePages, viewHref } from './observer'

type ObjectRow = { id: string; title: string; status: string; createdAt: string; updatedAt?: string }
type Props = {
  view: 'issue' | 'topic'
  query: URLSearchParams
  children: (id: string) => ReactNode
}
export default function ObjectWorkspace({ view, query, children }: Props) {
  const name = view === 'issue' ? 'Issue' : 'Topic'
  const scope = view === 'issue' ? query.get('topicId') || '' : ''
  const params = new URLSearchParams({ order: 'newest', limit: '50', preview: 'true' })
  if (scope) params.set('topicId', scope)
  const path = view + 's?' + params
  const list = usePages<ObjectRow>(path)
  const requested = query.get('id') || ''
  const [initial, setInitial] = useState({ path: '', id: '' })
  const first = list.data?.[0]?.id
  useEffect(() => {
    if (requested) setInitial({ path, id: requested })
    else if (first && initial.path !== path) setInitial({ path, id: first })
  }, [first, requested, path, initial.path])
  const id = requested || (initial.path === path ? initial.id : first)
  const filters: Record<string, string> = scope ? { topicId: scope } : {}
  const listHref = viewHref(view, '', scope ? filters : { list: '1' })
  const listOnly = !requested && (query.get('list') === '1' || Boolean(scope))
  const detailId = requested || (listOnly ? '' : id)
  const scroll = useRef<HTMLDivElement>(null)
  const details = useRef<HTMLElement>(null)
  const previous = useRef('')
  const [wide, setWide] = useState(() => matchMedia('(min-width: 761px)').matches)
  useEffect(() => {
    const media = matchMedia('(min-width: 761px)')
    const update = () => setWide(media.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  useLayoutEffect(() => {
    if (!wide && detailId && detailId !== previous.current) details.current?.focus({ preventScroll: true })
    if (!wide && !detailId && previous.current) {
      if (!list.data) return
      const row = Array.from(scroll.current?.querySelectorAll<HTMLAnchorElement>('.message-row') || [])
        .find(link => new URL(link.href).searchParams.get('id') === previous.current)
      row?.focus({ preventScroll: true })
    }
    previous.current = detailId || ''
  }, [detailId, wide, list.data])
  const restored = useRef('')
  useLayoutEffect(() => {
    if (!list.data || !scroll.current || restored.current === path) return
    scroll.current.scrollTop = history.state?.observerScroll?.[path] || 0
    restored.current = path
  }, [list.data, path])
  return <div className={'split-view message-layout object-workspace' + (detailId ? ' has-selection' : '')}>
    <aside className="panel object-list" aria-label={'最近的 ' + name}>
      <header className="panel-heading"><h2>最近的 {name}</h2>{scope && <a href={viewHref(view)}>清除 Topic 筛选</a>}</header>
      {list.error && <p className="read-error" role="alert">{list.error}{list.data && ' · 快照已过时'}</p>}
      <div className="object-scroll" ref={scroll} onScroll={e => {
        if (e.currentTarget.clientHeight) history.replaceState({ ...history.state, observerScroll: { ...history.state?.observerScroll, [path]: e.currentTarget.scrollTop } }, '')
      }}>
        {list.data?.map(row => <a key={row.id} className="message-row" href={viewHref(view, row.id, filters)} aria-current={row.id === id ? 'true' : undefined}>
          <div className="row-meta"><span>{stateLabel(row.status)}</span><time>{formatTime(row.updatedAt || row.createdAt)}</time></div>
          <div className="row-bottom"><strong className="row-title">{row.title || row.id}</strong><ChevronRight size={14}/></div>
        </a>)}
        {list.next && <Button className="load-more" variant="ghost" onClick={list.more} disabled={list.loading}>加载更多</Button>}
        {!list.data && !list.error && <p className="empty">正在读取…</p>}
        {list.data?.length === 0 && <p className="empty">暂无 {name} 记录</p>}
      </div>
    </aside>
    <section ref={details} tabIndex={-1} className="detail-column" aria-label={name + ' 详情'}>
      {detailId && <a className="mobile-back" href={listHref} onClick={event => {
        if (history.state?.observerPrevious === listHref && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
          event.preventDefault()
          history.back()
        }
      }}><ArrowLeft size={16}/>返回 {name} 列表</a>}
      {id && (wide || detailId) ? children(id) : <p className="panel empty">从列表选择一个 {name}</p>}
    </section>
  </div>
}
