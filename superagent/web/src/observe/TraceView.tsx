import { useLayoutEffect, useRef, useState } from 'react'
import { ChevronDown, ChevronRight, Search } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime } from '../workbench'
import CopyLink from './CopyLink'
import { traceHref } from './TraceLinks'
import { navigate, useRecord, useRefresh, type ViewProps } from './observer'
import { elapsed } from './metricFormat'
import './observability.css'

type Span = {
  id: string; parentId?: string; name: string; service: string; offsetUs: number; durationUs: number; status: number
  role?: string; tool?: string; action?: string; outcome?: string; callerId?: string; eventId?: string
}
type Trace = { traceId: string; startedAt: string; spans: Span[]; truncated: boolean }

function spanTree(spans: Span[], collapsed: Set<string>) {
  const ids = new Set(spans.map(span => span.id))
  const children = new Map<string, Span[]>()
  const roots: Span[] = []
  for (const span of spans) {
    if (!span.parentId || !ids.has(span.parentId) || span.parentId === span.id) roots.push(span)
    else children.set(span.parentId, [...children.get(span.parentId) || [], span])
  }
  const visited = new Set<string>()
  const rows: { span: Span; depth: number; hasChildren: boolean }[] = []
  function visit(span: Span, depth: number, visible: boolean) {
    if (visited.has(span.id)) return
    visited.add(span.id)
    const branches = children.get(span.id) || []
    if (visible) rows.push({ span, depth, hasChildren: branches.length > 0 })
    for (const child of branches) visit(child, depth + 1, visible && !collapsed.has(span.id))
  }
  for (const root of roots) visit(root, 0, true)
  for (const span of spans) visit(span, 0, true)
  return rows
}

export default function TraceView({ query }: ViewProps) {
  const traceId = query.get('id') || ''
  const anchor = query.get('at') || ''
  const selected = query.get('span') || ''
  return <TraceWorkspace key={`${traceId}/${anchor}/${selected}`} traceId={traceId} anchor={anchor} selected={selected}/>
}

function TraceWorkspace({ traceId, anchor, selected }: { traceId: string; anchor: string; selected: string }) {
  const [draft, setDraft] = useState(traceId)
  const valid = /^[a-f0-9]{32}$/.test(traceId) && !/^0+$/.test(traceId)
  const parameters = anchor ? '?' + new URLSearchParams({ at: anchor }) : ''
  const record = useRecord<Trace>(valid ? 'traces/' + traceId + parameters : '')
  const { online } = useRefresh()
  return <div className="observability">
    <form className="panel observable-query" onSubmit={event => { event.preventDefault(); navigate(traceHref(draft.trim().toLowerCase())) }}>
      <label>Trace ID<input value={draft} onChange={event => setDraft(event.target.value)} required pattern="[a-fA-F0-9]{32}" spellCheck={false} autoCapitalize="none"/></label>
      <Button type="submit"><Search/>查询 Trace</Button>
      {traceId && <CopyLink href={traceHref(traceId, anchor, selected)}/>}
    </form>
    {!traceId && <p className="empty">输入 Trace ID，或从事件打开 Trace。</p>}
    {traceId && !valid && <p className="read-error" role="alert">Trace ID 须为 32 位十六进制字符。</p>}
    {valid && online && record.loading && !record.data && <p className="empty" role="status">正在读取 Trace…</p>}
    {record.error && <p className="read-error" role="alert">{record.error.includes('404')
      ? '查询范围内暂无 Span。Trace 可能仍在写入，或已超过保留期。'
      : record.error.includes('409') ? 'Core 尚未配置 Trace 查询来源。' : record.error}</p>}
    {record.data && <Waterfall trace={record.data} requestedSpan={selected} anchor={anchor}/>}
  </div>
}

function Waterfall({ trace, requestedSpan, anchor }: { trace: Trace; requestedSpan: string; anchor: string }) {
  const byID = new Map(trace.spans.map(span => [span.id, span]))
  const [selected, select] = useState(requestedSpan)
  const [toolsOnly, setToolsOnly] = useState(false)
  const [collapsed, setCollapsed] = useState(() => new Set<string>())
  const [zoom, setZoom] = useState(1)
  const viewport = useRef<HTMLDivElement>(null)
  const span = byID.get(selected) || trace.spans[0]
  const full = Math.max(1, ...trace.spans.map(span => span.offsetUs + span.durationUs))
  const width = full / zoom
  const start = Math.max(0, Math.min(full - width, span.offsetUs - width / 4))
  const end = start + width
  const rows = toolsOnly
    ? trace.spans.filter(span => span.tool).map(span => ({ span, depth: 0, hasChildren: false }))
    : spanTree(trace.spans, collapsed)
  useLayoutEffect(() => {
    const container = viewport.current
    const row = container?.querySelector<HTMLElement>('[data-selected=true]')
    if (!container || !row) return
    if (row.offsetTop < container.scrollTop) container.scrollTop = row.offsetTop
    if (row.offsetTop + row.offsetHeight > container.scrollTop + container.clientHeight) {
      container.scrollTop = row.offsetTop + row.offsetHeight - container.clientHeight
    }
  }, [span.id, toolsOnly, collapsed])
  function selectParent() {
    if (!span.parentId || !byID.has(span.parentId)) return
    setToolsOnly(false)
    setCollapsed(new Set())
    select(span.parentId)
  }
  return <>
    <div className="panel waterfall-summary">
      <div><strong>{trace.spans.length} spans</strong><span>时间跨度 {elapsed(full * 1000)} · {formatTime(trace.startedAt)}</span></div>
      <fieldset><legend className="sr-only">Span 筛选</legend>
        <Button variant={toolsOnly ? 'ghost' : 'outline'} size="sm" aria-pressed={!toolsOnly} onClick={() => setToolsOnly(false)}>全部 Span</Button>
        <Button variant={toolsOnly ? 'outline' : 'ghost'} size="sm" aria-pressed={toolsOnly} onClick={() => setToolsOnly(true)}>MCP 工具</Button>
      </fieldset>
      <label>时间轴缩放<select value={zoom} onChange={event => setZoom(Number(event.target.value))}>
        {[1, 2, 4, 8, 16, 32].map(factor => <option key={factor} value={factor}>{factor}×</option>)}
      </select></label>
    </div>
    {trace.truncated && <p className="read-error" role="status">查询只返回了部分 Span。</p>}
    <div className="waterfall-layout">
      <section className="panel waterfall" aria-label="Trace 瀑布图">
        <div className="waterfall-axis"><strong>操作</strong><div>{[0, .25, .5, .75, 1].map(tick => <span key={tick}>{elapsed((start + width * tick) * 1000)}</span>)}</div></div>
        <div className="waterfall-rows" ref={viewport}>
          {rows.length === 0 && <p className="empty">此 Trace 没有 MCP 工具 Span。</p>}
          {rows.map(({ span: row, depth, hasChildren }) => {
            const left = Math.max(start, row.offsetUs)
            const right = Math.min(end, row.offsetUs + row.durationUs)
            return <div className="waterfall-row" key={row.id} data-selected={row.id === span.id}>
              <div className="waterfall-operation" style={{ paddingInlineStart: 8 + Math.min(depth, 12) * 12 }}>
                {hasChildren ? <button type="button" className="span-expand" aria-expanded={!collapsed.has(row.id)} aria-label={`${collapsed.has(row.id) ? '展开' : '折叠'} ${row.name}`} onClick={() => setCollapsed(old => {
                  const next = new Set(old)
                  if (next.has(row.id)) next.delete(row.id)
                  else next.add(row.id)
                  return next
                })}>{collapsed.has(row.id) ? <ChevronRight size={13}/> : <ChevronDown size={13}/>}</button> : <span className="span-indent"/>}
                <button className="span-name" type="button" aria-pressed={row.id === span.id} onClick={() => select(row.id)} title={row.name}>
                  <strong>{row.tool || row.name}{row.action && ` · ${row.action}`}</strong><small>{row.role || row.service}</small>
                </button>
              </div>
              <button className="waterfall-timing" type="button" tabIndex={-1} onClick={() => select(row.id)} aria-label={elapsed(row.durationUs * 1000)} title={elapsed(row.durationUs * 1000)}>
                {right >= left && <span data-role={row.role} data-error={row.status === 2} style={{ left: `${100 * (left - start) / width}%`, width: `${100 * (right - left) / width}%` }}/>}
              </button>
            </div>
          })}
        </div>
      </section>
      <aside className="panel span-details" aria-label="Span 详情">
        <header><h2>Span 详情</h2><CopyLink href={traceHref(trace.traceId, anchor, span.id)}/></header>
        <strong>{span.name}</strong>
        <dl>{Object.entries({ 'Span ID': span.id, '服务': span.service, '开始偏移': elapsed(span.offsetUs * 1000), '耗时': elapsed(span.durationUs * 1000), '角色': span.role, '调用方': span.callerId, '工具': span.tool, '动作': span.action, '结果': span.outcome || (span.status === 2 ? '错误' : undefined), '事件 ID': span.eventId }).map(([label, value]) => value && <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
        {span.parentId && byID.has(span.parentId) && <Button variant="outline" size="sm" onClick={selectParent}>跳转父 Span</Button>}
        {span.parentId && !byID.has(span.parentId) && <p className="trace-unavailable">当前 Trace 查询未返回父 Span。</p>}
      </aside>
    </div>
  </>
}
