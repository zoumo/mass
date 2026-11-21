import { useState } from 'react'
import { Button } from '../components/ui/button'
import { formatTime } from '../workbench'
import { countText, elapsed, rate } from './metricFormat'
import { navigate, useRecord, viewHref, type ViewProps } from './observer'
import PerformanceView, { performanceHref } from './PerformanceView'
import { TraceLink } from './TraceLinks'
import './observability.css'

const roles = ['router', 'topic', 'issue'] as const
type Role = typeof roles[number]
const dimensions = { all: '全部', message: 'Message', router: 'Router', topic: 'Topic', issue: 'Issue', 'session-binding': 'SessionBinding', tool: '工具' } as const
type Dimension = keyof typeof dimensions
const windows = { '1h': '1 小时', '24h': '24 小时', '7d': '7 天', '30d': '30 天' } as const
type Window = keyof typeof windows
type Filter = { scope: Dimension; id: string; window: Window; includeIssues: boolean }
type Timing = { count: number; failed: number; durationNs: number }
type Stats = Timing & { p95Ns: number; changed: number; replayed: number }
type Tool = Stats & { role: Role; tool: string }
type Bucket = Timing & { at: string; role: Role }
type Metrics = { from: string; until: string; bucketSeconds: number; total: Stats; tools: Tool[]; trend: Bucket[] }
type Call = { sequence: number; at: string; role: Role; callerId: string; tool: string; action?: string; outcome: string; durationNs: number; traceId?: string; spanId?: string; replayed: boolean }
type Calls = { items: Call[]; nextBefore?: number }

export function metricsHref(scope: Dimension, id = '', window: Window = '24h', includeIssues = false, before = '') {
  return viewHref('metrics', id, { scope, window, includeIssues: includeIssues ? 'true' : '', before })
}

export default function MetricsView({ query }: ViewProps) {
  const performance = query.get('tab') === 'performance'
  const scope = query.get('scope') || 'all'
  const window = query.get('window') || ''
  const filter: Filter = {
    scope: Object.hasOwn(dimensions, scope) ? scope as Dimension : 'all',
    window: Object.hasOwn(windows, window) ? window as Window : '24h',
    id: query.get('id') || '', includeIssues: query.get('includeIssues') === 'true',
  }
  const toolUsage = performance ? viewHref('metrics') : metricsHref(filter.scope, filter.id, filter.window, filter.includeIssues, query.get('before') || '')
  return <div className="observability">
    <nav className="panel metrics-scopes" aria-label="Metrics">
      <a href={toolUsage} aria-current={performance ? undefined : 'page'}>工具用量</a>
      <a href={performance ? viewHref('metrics', '', { tab: 'performance', window }) : performanceHref()} aria-current={performance ? 'page' : undefined}>系统性能</a>
    </nav>
    {performance
      ? <PerformanceView query={query}/>
      : <MetricsWorkspace key={JSON.stringify(filter)} filter={filter} before={query.get('before') || ''}/>}
  </div>
}

function metricsParams(filter: Filter) {
  const { scope, id, window, includeIssues } = filter
  const params = new URLSearchParams({ window })
  if (scope === 'message') params.set('messageId', id)
  if (scope === 'session-binding') params.set('sessionBindingId', id)
  if (scope === 'tool' && id) params.set('tool', id)
  if (scope === 'topic' && includeIssues) params.set('topicId', id)
  else if (scope === 'router' || scope === 'topic' || scope === 'issue') {
    params.set('role', scope)
    if (id) params.set('callerId', id)
  }
  return params
}

function MetricsWorkspace({ filter, before }: { filter: Filter; before: string }) {
  const { scope, id, window, includeIssues } = filter
  const [draft, setDraft] = useState(id)
  const requiresID = scope === 'message' || scope === 'session-binding' || (scope === 'topic' && includeIssues)
  const ready = !requiresID || Boolean(id)
  const params = metricsParams(filter)
  const metrics = useRecord<Metrics>(ready ? 'mcp-tool-usage/metrics?' + params : '')
  const callParams = new URLSearchParams(params)
  callParams.set('limit', '20')
  if (before) callParams.set('before', before)
  const calls = useRecord<Calls>(ready ? 'mcp-tool-usage/calls?' + callParams : '')
  const acceptsID = scope !== 'all' && scope !== 'router'
  const idLabel = scope === 'tool' ? '工具名称' : scope === 'message' || scope === 'session-binding' ? dimensions[scope] + ' ID' : '调用方 ID'
  return <div className="observability">
    <nav className="panel metrics-scopes" aria-label="Metrics 统计维度">
      {Object.entries(dimensions).map(([name, label]) => <a key={name} href={metricsHref(name as Dimension, name === scope ? id : '', window, name === scope && includeIssues)} aria-current={name === scope ? 'page' : undefined}>{label}</a>)}
    </nav>
    <form className="panel observable-query" onSubmit={event => { event.preventDefault(); navigate(metricsHref(scope, draft.trim(), window, includeIssues)) }}>
      {acceptsID && <label>{idLabel}<input required={requiresID} value={draft} onChange={event => setDraft(event.target.value)} placeholder={requiresID ? undefined : '全部'}/></label>}
      {scope === 'topic' && <label>Topic 用量范围<select aria-label="Topic 用量范围" value={String(includeIssues)} onChange={event => navigate(metricsHref(scope, draft.trim(), window, event.target.value === 'true'))}>
        <option value="false">Topic 自身</option><option value="true">包含下属 Issue</option>
      </select></label>}
      <label>时间范围<select value={window} onChange={event => navigate(metricsHref(scope, id, event.target.value as Window, includeIssues))}>
        {Object.entries(windows).map(([value, label]) => <option key={value} value={value}>最近 {label}</option>)}
      </select></label>
      {acceptsID && <Button type="submit">应用筛选</Button>}
      {id && (scope === 'message' || scope === 'topic' || scope === 'issue') && <a href={viewHref(scope, id)}>查看 {dimensions[scope]} →</a>}
      {id && scope === 'session-binding' && <a href={'/observer/?' + new URLSearchParams({ view: 'session-bindings', id })}>查看 SessionBinding →</a>}
    </form>
    {scope === 'message' && <p className="metric-note">按 Core 已记录的事件和 Trace 统计关联调用。跨消息共用的调用可能出现在多个 Message 视角。</p>}
    {!ready && <p className="empty">{scope === 'topic' ? '输入 Topic ID 查看包含下属 Issue 的用量。' : `输入 ${idLabel} 查看关联工具用量。`}</p>}
    {metrics.loading && ready && !metrics.data && !metrics.error && <p className="empty" role="status">正在读取 Metrics…</p>}
    {metrics.error && <p className="read-error" role="alert">{metrics.error}{metrics.data && ' · 上一次查询结果'}</p>}
    {metrics.data && <MetricCharts metrics={metrics.data} filter={filter}/>}
    {ready && <section className="panel metrics-detail" aria-labelledby="tool-call-records">
      <header><h2 id="tool-call-records">工具调用记录</h2>{before && <a href={metricsHref(scope, id, window, includeIssues)}>回到最新</a>}</header>
      {calls.error && <p className="read-error" role="alert">{calls.error}</p>}
      {calls.data?.items.length === 0 && <p className="empty">当前范围暂无工具调用。</p>}
      {!!calls.data?.items.length && <div className="metric-table-scroll"><table><thead><tr><th>时间</th><th>调用方</th><th>工具</th><th>结果</th><th>Core 耗时</th><th>Trace</th></tr></thead><tbody>
        {calls.data.items.map(call => <tr key={call.sequence}>
          <td><time dateTime={call.at} title={call.at}>{formatTime(call.at)}</time></td>
          <td><a href={metricsHref(call.role, call.callerId, window)}>{dimensions[call.role]}{call.callerId && ` · ${call.callerId}`} </a></td>
          <td><a href={metricsHref('tool', call.tool, window)}>{call.tool}</a>{call.action && ` · ${call.action}`}</td>
          <td>{call.outcome}{call.replayed && ' · 重放'}</td><td>{elapsed(call.durationNs)}</td>
          <td>{call.traceId ? <TraceLink id={call.traceId} at={call.at} span={call.spanId}/> : '—'}</td>
        </tr>)}
      </tbody></table></div>}
      {calls.data?.nextBefore && <a className="metrics-older" href={metricsHref(scope, id, window, includeIssues, String(calls.data.nextBefore))}>更早的记录 →</a>}
    </section>}
  </div>
}

function MetricCharts({ metrics, filter }: { metrics: Metrics; filter: Filter }) {
  const total = metrics.total
  const kpis = [
    { label: '工具调用次数', value: countText.format(total.count), note: `${total.replayed} 次幂等重放 · ${total.changed} 次业务变更` },
    { label: '失败率', value: rate(total.failed, total.count), note: `${total.failed} 次失败` },
    { label: 'Core P95 耗时', value: total.count ? elapsed(total.p95Ns) : '—', note: total.count ? `平均 ${elapsed(total.durationNs / total.count)}` : '无样本' },
    { label: 'Core 累计耗时', value: elapsed(total.durationNs), note: '逐次工具处理耗时之和' },
  ]
  const largest = Math.max(1, ...metrics.tools.map(tool => tool.count))
  const slowest = Math.max(1, ...metrics.tools.map(tool => tool.p95Ns))
  return <>
    <div className="metric-kpis">{kpis.map(kpi => <div className="panel metric-kpi" key={kpi.label}><span>{kpi.label}</span><strong>{kpi.value}</strong><small>{kpi.note}</small></div>)}</div>
    {total.count > 0 && <>
      <Trend metrics={metrics}/>
      <section className="panel metrics-detail">
        <header><h2>按角色和工具</h2><small>按调用次数排序</small></header>
        <div className="metric-table-scroll"><table><thead><tr><th>调用方角色</th><th>工具</th><th>调用次数</th><th>失败率</th><th>平均耗时</th><th>P95 耗时</th></tr></thead><tbody>
          {metrics.tools.map(tool => <tr key={`${tool.role}/${tool.tool}`}>
            <td><span className="metric-role" data-role={tool.role}>{dimensions[tool.role]}</span></td>
            <td><a href={metricsHref('tool', tool.tool, filter.window)}>{tool.tool}</a></td>
            <td><div className="metric-bar" data-role={tool.role}><span style={{ width: `${100 * tool.count / largest}%` }}/><strong>{countText.format(tool.count)}</strong></div></td>
            <td>{rate(tool.failed, tool.count)}</td><td>{elapsed(tool.durationNs / tool.count)}</td>
            <td><div className="metric-bar" data-role={tool.role}><span style={{ width: `${100 * tool.p95Ns / slowest}%` }}/><strong>{elapsed(tool.p95Ns)}</strong></div></td>
          </tr>)}
        </tbody></table></div>
      </section>
    </>}
  </>
}

function Trend({ metrics }: { metrics: Metrics }) {
  const [measure, setMeasure] = useState('count')
  const [selected, select] = useState<number>()
  const start = Date.parse(metrics.from)
  const byTime = new Map<number, Bucket[]>()
  for (const bucket of metrics.trend) {
    const at = Date.parse(bucket.at)
    byTime.set(at, [...byTime.get(at) || [], bucket])
  }
  const intervals = Array.from({ length: 24 }, (_, index) => {
    const at = start + index * metrics.bucketSeconds * 1000
    const buckets = byTime.get(at) || []
    const total = buckets.reduce((sum, bucket) => sum + bucket.count, 0)
    const failed = buckets.reduce((sum, bucket) => sum + bucket.failed, 0)
    const ns = buckets.reduce((sum, bucket) => sum + bucket.durationNs, 0)
    const value = measure === 'count' ? total : measure === 'failure' ? (total ? failed / total : 0) : (total ? ns / total : 0)
    const label = measure === 'count' ? `${total} 次` : measure === 'failure' ? rate(failed, total) : total ? elapsed(value) : '—'
    return { at, buckets, total, value, label }
  })
  const ceiling = measure === 'failure' ? 1 : Math.max(1, ...intervals.map(interval => interval.value))
  const axis = (number: number) => measure === 'count' ? countText.format(number) : measure === 'failure' ? rate(number, 1) : elapsed(number)
  return <section className="panel metrics-trend">
    <header><h2>工具调用趋势</h2><select aria-label="趋势指标" value={measure} onChange={event => setMeasure(event.target.value)}>
      <option value="count">调用次数</option><option value="failure">失败率</option><option value="latency">平均 Core 耗时</option>
    </select></header>
    <div className="trend-plot"><div className="trend-scale" aria-hidden="true"><span>{axis(ceiling)}</span><span>{axis(0)}</span></div>
    <div className="trend-chart" aria-label="按时间区间查看工具调用趋势">
      {intervals.map((interval, index) => <button type="button" key={interval.at} data-selected={index === selected}
        aria-label={`${formatTime(new Date(interval.at).toISOString())} · ${interval.label}`} onClick={() => select(index)} onFocus={() => select(index)} onMouseEnter={() => select(index)}>
        {measure === 'count' ? roles.map(role => {
          const count = interval.buckets.find(bucket => bucket.role === role)?.count || 0
          return count > 0 && <span key={role} data-role={role} style={{ height: `${100 * count / ceiling}%` }}/>
        }) : <span style={{ height: `${100 * interval.value / ceiling}%` }}/>}
      </button>)}
    </div>
    <div className="trend-axis"><span>{formatTime(metrics.from)}</span><span>{formatTime(metrics.until)}</span></div></div>
    <footer>{measure === 'count' && <div>{roles.map(role => <span className="metric-role" data-role={role} key={role}>{dimensions[role]}</span>)}</div>}
      {selected !== undefined && <output>{formatTime(new Date(intervals[selected].at).toISOString())} · {intervals[selected].label}
        {measure === 'count' && intervals[selected].buckets.map(bucket => ` · ${dimensions[bucket.role]} ${bucket.count}`)}</output>}</footer>
  </section>
}
