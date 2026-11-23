import { useEffect, useRef, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { Button } from '../../components/ui/button'
import { stateLabel, eventStatusLabel } from '../../workbench'
import { navigate, usePages, useRecord, useRefresh, viewHref } from '../observer'
import RecordDetail from './RecordDetail'
import { TraceLinks } from '../TraceLinks'
import {
  followupLabel,
  identity,
  kinds,
  replyLabel,
  viewLabel,
  type DiagnosticRow,
  type Kind,
  type Filter,
  type OpenCollection,
} from './records'
import './diagnostics.css'

type Counts = Record<string, number>
type Health = {
  ingress: string
  router: string
  topicStates: Counts
  issueStates: Counts
  eventStates: Counts
  outboxStates: Counts
  unknownStops: number
}
const statusCounts = (counts: Counts) =>
  Object.entries(counts)
    .map(([k, v]) => v + ' ' + stateLabel(k))
    .join(' · ') || '无'
const workViews = { messages: 'message', issues: 'issue', topics: 'topic' } as const
function href(view: Kind, filterId = '', scope: Filter = 'topicId', id = '') {
  const q = new URLSearchParams({ view })
  if (id) q.set('id', id)
  if (filterId) q.set(scope, filterId)
  if (view in workViews) q.set('mode', 'records')
  return '/observer/?' + q
}
function nameOf(kind: Kind, row: DiagnosticRow) {
  if (kind === 'messages') return row.text || identity(row)
  if (kind === 'outbox') return row.preview || identity(row)
  return (
    row.canonicalUrl ||
    row.title ||
    row.summary ||
    (kind === 'timers' && typeof row.content === 'string' && row.content) ||
    (row.role || row.action || row.kind ? stateLabel(row.role || row.action || row.kind) : identity(row))
  )
}
type Column = [string, (row: DiagnosticRow) => ReactNode]
const internalEventTrace: Column = ['Trace ID', row => <TraceLinks associations={row.traceAssociations} at={row.createdAt} label={false}/>]
function columns(kind: Kind): Column[] {
  switch (kind) {
    case 'messages':
      return [['来源', (r) => r.source]]
    case 'topics':
    case 'issues':
      return [['业务状态', (r) => stateLabel(r.status)]]
    case 'outbox':
      return [['发送状态', (r) => replyLabel(r.status)]]
    case 'session-bindings':
      return [
        ['状态', (r) => stateLabel(r.state)],
        ['启动输入', (r) => r.startup && eventStatusLabel(r.startup.status)],
        ['原因', (r) => r.lastError],
        ['所属记录', (r) => r.ownerId],
      ]
    case 'timers':
      return [
        ['定时器状态', (r) => stateLabel(r.status)],
        ['计划触发时间 (UTC)', (r) => r.dueAt],
        ['版本', (r) => r.revision],
        ['跟进通知事件', (r) => followupLabel(r.followup)],
      ]
    case 'reports':
      return [
        ['报告类型', (r) => stateLabel(r.kind)],
        ['报告状态', (r) => (r.current ? '当前报告' : '历史报告')],
      ]
    case 'relations':
    case 'routing-events':
      return [
        ['Topic', (r) => r.topicId],
        ['Session 投递', (r) => r.status && eventStatusLabel(r.status)],
        ...(kind === 'routing-events' ? [internalEventTrace] : []),
      ]
    case 'subscriptions':
      return [['Topic', (r) => r.topicId]]
    case 'internal-events':
      return [['Session 投递', (r) => eventStatusLabel(r.status)], internalEventTrace]
    default:
      return []
  }
}
function Overview({ health }: { health: Health }) {
  return (
    <section className="diag-health" aria-label="运行概况">
      <dl>
        <dt>接收状态</dt>
        <dd>{health.ingress === 'ready' ? '核心服务已就绪' : stateLabel(health.ingress)}</dd>
        <dt>路由器准备状态</dt>
        <dd>{stateLabel(health.router)}</dd>
      </dl>
      <dl>
        <dt>Topic</dt>
        <dd>{statusCounts(health.topicStates)}</dd>
        <dt>Issue</dt>
        <dd>{statusCounts(health.issueStates)}</dd>
      </dl>
      <dl>
        <dt>待处理输入</dt>
        <dd>{(health.eventStates.pending || 0) + (health.eventStates.dispatching || 0)}</dd>
        <dt>Session 已受理</dt>
        <dd>{health.eventStates.sent || 0}</dd>
        <dt>回执</dt>
        <dd>{statusCounts(health.outboxStates)}</dd>
      </dl>
      <dl>
        <dt>原生停止未知</dt>
        <dd>{health.unknownStops}</dd>
      </dl>
    </section>
  )
}
export default function Diagnostics({ query }: { query: URLSearchParams }) {
  const { online } = useRefresh()
  const kind = kinds.find((k) => k === query.get('view')) || 'messages'
  const id = query.get('id') || ''
  const scope = (['threadKey', 'topicId', 'messageId', 'issueId'] as const).find((key) => query.has(key))
  const filter = scope ? query.get(scope) || '' : ''
  const params = new URLSearchParams({ limit: '50', preview: 'true' })
  if (scope && filter) params.set(scope, filter)
  if (kind === 'internal-events' || kind === 'routing-events') params.set('trace', 'true')
  const list = usePages<DiagnosticRow>(kind + '?' + params)
  const detail = useRecord<DiagnosticRow>(id ? kind + '/' + encodeURIComponent(id) + '?trace=true' : '')
  const health = useRecord<Health>('health')
  const [draft, setDraft] = useState(filter)
  useEffect(() => setDraft(scope === 'threadKey' ? filter : ''), [scope, filter])
  const rows = list.data || []
  const cols = columns(kind)
  const open: OpenCollection = (view, filterId, key, selected) => {
    if (!key && view === 'reports') key = 'issueId'
    navigate(href(view, filterId, key, selected))
  }
  const dialog = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    if (id && dialog.current) {
      if (!dialog.current.open) dialog.current.showModal()
      dialog.current.scrollTop = 0
      dialog.current.querySelector<HTMLButtonElement>('.diag-dialog-header button')?.focus({ preventScroll: true })
    }
    if (!id && dialog.current?.open) dialog.current.close()
  }, [kind, id])
  function close() {
    const previous = history.state?.observerPrevious
    const previousQuery = previous && new URL(previous,location.origin).searchParams
    const fromWorkspace = previousQuery && !previousQuery.has('mode') &&
      (!previousQuery.has('view') || Object.hasOwn(workViews, previousQuery.get('view') || ''))
    if (previous === href(kind, filter, scope) || fromWorkspace) {
      history.back()
      return
    }
    history.replaceState(history.state, '', href(kind, filter, scope))
    window.dispatchEvent(new PopStateEvent('popstate'))
  }
  return (
    <div className="diagnostic-workspace">
      {health.data && <Overview health={health.data} />}
      {health.error && (
        <p className="read-error" role="alert">
          {health.error}
        </p>
      )}
      <nav className="diag-collections" aria-label="诊断列表">
        {kinds.map((k) => (
          <Button
            key={k}
            variant={k === kind ? 'secondary' : 'ghost'}
            size="sm"
            onClick={() => open(k)}
            aria-current={k === kind ? 'page' : undefined}
          >
            {viewLabel(k)}
          </Button>
        ))}
      </nav>
      <section className="panel diag-table-panel">
        <div className="panel-heading">
          <h2>{viewLabel(kind)}</h2>
          {filter && <button onClick={() => open(kind)}>清除筛选</button>}
        </div>
        {kind === 'messages' && (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              open(kind, draft.trim(), 'threadKey')
            }}
          >
            <input
              aria-label="按线程键筛选"
              placeholder="输入完整线程键"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
            <Button variant="outline" size="sm">
              筛选
            </Button>
          </form>
        )}
        {list.error && (
          <p className="read-error" role="alert">
            {list.error}
            {list.data && ' · 快照已过时'}
          </p>
        )}
        <div className="diag-scroll">
          <table>
            <thead>
              <tr>
                <th>记录</th>
                {cols.map(([name]) => (
                  <th key={name}>{name}</th>
                ))}
                <th>记录时间</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={identity(row)}>
                  <td>
                    <button onClick={() => open(kind, filter, scope, identity(row))}>{nameOf(kind, row)}</button>
                  </td>
                  {cols.map(([name, value]) => (
                    <td key={name}>{value(row) ?? '—'}</td>
                  ))}
                  <td>{row.receivedAt || row.createdAt || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {list.data && rows.length === 0 && <p className="empty">暂无{viewLabel(kind)}记录。</p>}
        <footer>
          {list.data
            ? '显示 ' + rows.length + ' 条' + viewLabel(kind)
            : list.error
              ? '无法读取列表'
              : '正在读取列表…'}
          {list.next && (
            <Button variant="outline" onClick={list.more} disabled={list.loading}>
              加载更多
            </Button>
          )}
        </footer>
      </section>
      <dialog
        ref={dialog}
        className="diag-dialog"
        aria-label={viewLabel(kind) + '详情'}
        onCancel={(e) => {
          e.preventDefault()
          close()
        }}
      >
        <div className="diag-dialog-header">
          <h2>{viewLabel(kind)}详情</h2>
          <button type="button" aria-label="关闭" onClick={close}>
            <X size={20} />
          </button>
        </div>
        {!online && (
          <p className="read-error" role="alert">
            浏览器已离线 · 快照已过时
          </p>
        )}
        {detail.error && (
          <p role="alert">
            {detail.error}
            {detail.data && ' · 快照已过时'}
          </p>
        )}
        {detail.data ? (
          <div className="record-detail" key={kind + '/' + id}>
            {kind in workViews && (
              <a href={viewHref(workViews[kind as keyof typeof workViews], id)}>在{viewLabel(kind)}视角打开 →</a>
            )}
            <RecordDetail kind={kind} detail={detail.data} navigate={open} />
          </div>
        ) : (
          id && !detail.error && <p>正在读取记录…</p>
        )}
      </dialog>
    </div>
  )
}
