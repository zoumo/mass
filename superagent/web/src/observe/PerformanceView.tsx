import { useState } from 'react'
import { navigate, useRecord, viewHref, type ViewProps } from './observer'
import './performance.css'

const windows = { '15m': '15 分钟', '1h': '1 小时', '6h': '6 小时', '24h': '24 小时' } as const
type Window = keyof typeof windows

const definitions = {
  cpu: { title: 'CPU 使用量', unit: '核' },
  rss: { title: 'RSS 内存', unit: 'B' },
  goroutines: { title: 'Goroutine 数', unit: '个' },
  file_descriptors: { title: '打开的文件描述符', unit: '个' },
} as const
type MetricID = keyof typeof definitions
type Point = [unixSeconds: number, value: number]
type Series = { service: 'superagent-core' | 'massd'; instance: string; points: Point[] }
type Metric = { id: MetricID; unavailable?: boolean; series: Series[] }
type Performance = { from: number; until: number; stepSeconds: number; metrics: Metric[] }

export function performanceHref(window: Window = '1h') {
  return viewHref('metrics', '', { tab: 'performance', window })
}

export default function PerformanceView({ query }: ViewProps) {
  const requested = query.get('window')
  const range = requested && Object.hasOwn(windows, requested) ? requested as Window : '1h'
  const { data, loading, error } = useRecord<Performance>(`performance?window=${range}`)
  return <div className="observability">
    <div className="panel observable-query">
      <label>性能时间范围<select value={range} onChange={event => navigate(performanceHref(event.target.value as Window))}>
        {Object.entries(windows).map(([value, label]) => <option key={value} value={value}>最近 {label}</option>)}
      </select></label>
      {data && <p className="metric-note">本地时间 · 查询步长 {data.stepSeconds} 秒</p>}
    </div>
    {loading && !data && !error && <p className="empty" role="status">正在读取系统性能…</p>}
    {error && <p className="read-error" role="alert">{error}{data && ' · 上一次查询结果'}</p>}
    {data && <div className="performance-grid">
      {(Object.keys(definitions) as MetricID[]).map(id => <PerformanceChart key={`${range}/${id}`} id={id}
        metric={data.metrics.find(metric => metric.id === id)} from={data.from} until={data.until} step={data.stepSeconds}/>)}
    </div>}
  </div>
}

const plotWidth = 1000
const plotHeight = 180
const bands = 4
const colors = { 'superagent-core': '#55dac2', massd: '#f7b97a' }
const dashes = ['', '6 3', '2 3', '8 3 2 3']
const axisNumber = new Intl.NumberFormat(undefined, { maximumSignificantDigits: 4 })
const cpuNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 })
const exactNumber = new Intl.NumberFormat(undefined, { maximumSignificantDigits: 21 })
const axisTime = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
const axisDateTime = new Intl.DateTimeFormat(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
const sampleTime = new Intl.DateTimeFormat(undefined, {
  year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit',
  hourCycle: 'h23', timeZoneName: 'shortOffset',
})

function axisScale(id: MetricID, peak: number) {
  const minimum = id === 'cpu' ? 0.005 : 1
  const step = Math.max(minimum, peak / bands)
  const power = 10 ** Math.floor(Math.log10(step))
  const multiplier = [1, 2, 2.5, 5, 10].find(value => value * power >= step) ?? 10
  const interval = id === 'goroutines' || id === 'file_descriptors' ? Math.ceil(multiplier * power) : multiplier * power
  const top = interval * bands
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const order = id === 'rss' ? Math.min(units.length - 1, Math.floor(Math.log2(top) / 10)) : 0
  return { top, unit: id === 'rss' ? units[order] : definitions[id].unit, labels: Array.from({ length: bands + 1 }, (_, index) =>
    axisNumber.format(interval * (bands - index) / 1024 ** order)) }
}

function plotPaths(points: Point[], step: number, x: (at: number) => number, y: (value: number) => number) {
  let line = ''
  let dots = ''
  for (const [index, [at, value]] of points.entries()) {
    const previous = index > 0 && at - points[index - 1][0] === step
    const next = index + 1 < points.length && points[index + 1][0] - at === step
    const position = `${x(at)} ${y(value)}`
    if (previous || next) line += `${previous ? 'L' : 'M'}${position}`
    else dots += `M${position}h0.01`
  }
  return { line, dots }
}

function PerformanceChart({ id, metric, from, until, step }: { id: MetricID; metric?: Metric; from: number; until: number; step: number }) {
  const [selectedTime, selectTime] = useState<number>()
  const definition = definitions[id]
  const heading = `performance-${id}`
  const serviceIndexes = { 'superagent-core': 0, massd: 0 }
  const series = (metric?.series || []).filter(item => item.points.length > 0)
    .sort((a, b) => a.service.localeCompare(b.service) || a.instance.localeCompare(b.instance))
    .map(item => ({
      ...item,
      points: [...item.points].sort(([a], [b]) => a - b),
      color: colors[item.service],
      dash: dashes[serviceIndexes[item.service]++ % dashes.length],
    }))
  const peak = series.reduce((max, item) => item.points.reduce((high, [, value]) => Math.max(high, value), max), 0)
  const scale = axisScale(id, peak)
  const x = (at: number) => (at - from) * plotWidth / Math.max(1, until - from)
  const y = (value: number) => plotHeight * (1 - value / scale.top)
  const formatTick = new Date(from * 1000).toDateString() === new Date(until * 1000).toDateString() ? axisTime : axisDateTime
  const timestamps = [...new Set(series.flatMap(item => item.points.map(([at]) => at)))].sort((a, b) => a - b)
  const selected = selectedTime !== undefined && timestamps.includes(selectedTime) ? selectedTime : timestamps.at(-1)
  const exactTime = selected === undefined ? '' : sampleTime.format(selected * 1000)
  const samples = series.flatMap(item => {
    const point = item.points.find(([at]) => at === selected)
    return point ? [{ ...item, value: point[1] }] : []
  })
  function pointAt(fraction: number) {
    const time = from + fraction * (until - from)
    const nearest = timestamps.reduce((closest, at) => Math.abs(at - time) < Math.abs(closest - time) ? at : closest, timestamps[0])
    selectTime(nearest)
  }
  return <section className="panel performance-chart" aria-labelledby={heading}>
    <header><h2 id={heading}>{definition.title}</h2>{series.length > 0 && <small>{scale.unit}</small>}</header>
    {metric?.unavailable && <p className="empty" role="status">{series.length ? '部分性能数据无法读取。' : '该指标暂不可用。'}</p>}
    {!series.length && !metric?.unavailable && <p className="empty">当前范围暂无样本。</p>}
    {series.length > 0 && <><div className="performance-plot" aria-hidden="true">
      <div className="performance-scale">{scale.labels.map((label, index) => <span key={index}>{label}</span>)}</div>
      <svg viewBox={`0 0 ${plotWidth} ${plotHeight}`} preserveAspectRatio="none"
        onPointerMove={event => {
          if (event.pointerType !== 'touch') {
            const bounds = event.currentTarget.getBoundingClientRect()
            pointAt((event.clientX - bounds.left) / bounds.width)
          }
        }}
        onClick={event => {
          const bounds = event.currentTarget.getBoundingClientRect()
          pointAt((event.clientX - bounds.left) / bounds.width)
        }}>
        {scale.labels.map((_, index) => <line className="performance-gridline" key={index} x1={0} x2={plotWidth}
          y1={index * plotHeight / bands} y2={index * plotHeight / bands} vectorEffect="non-scaling-stroke"/>)}
        {selected !== undefined && <line className="performance-cursor" x1={x(selected)} x2={x(selected)} y1={0} y2={plotHeight}
          vectorEffect="non-scaling-stroke"/>}
        {series.map(item => {
          const paths = plotPaths(item.points, step, x, y)
          return <g key={`${item.service}/${item.instance}`} data-instance={item.instance} stroke={item.color} fill="none">
            {paths.line && <path d={paths.line} strokeWidth={2} strokeDasharray={item.dash} vectorEffect="non-scaling-stroke"/>}
            {paths.dots && <path d={paths.dots} strokeWidth={4} strokeLinecap="round" vectorEffect="non-scaling-stroke"/>}
          </g>
        })}
        {selected !== undefined && samples.map(item => <path key={`${item.service}/${item.instance}`}
          d={`M${x(selected)} ${y(item.value)}h0.01`} className="performance-sample" stroke={item.color} strokeWidth={7}
          strokeLinecap="round" vectorEffect="non-scaling-stroke"/>)}
      </svg>
      <div className="performance-axis">{[from, (from + until) / 2, until].map(at => <span key={at}>{formatTick.format(at * 1000)}</span>)}</div>
    </div>
    <ul className="performance-legend" aria-label={`${definition.title}服务与实例`}>
      {series.map(item => <li key={`${item.service}/${item.instance}`}>
        <svg viewBox="0 0 24 8" aria-hidden="true"><line x1={0} x2={24} y1={4} y2={4} stroke={item.color} strokeWidth={2} strokeDasharray={item.dash}/></svg>
        <span>{item.service} <small>{item.instance}</small></span>
      </li>)}
    </ul>
    {selected !== undefined && <div className="performance-inspector">
      <label>采样点<input type="range" min={0} max={timestamps.length - 1} value={timestamps.indexOf(selected)}
        aria-label={`${definition.title}采样点`} aria-valuetext={exactTime} onChange={event => selectTime(timestamps[Number(event.target.value)])}/></label>
      <output aria-label={`${definition.title}样本`}><time dateTime={new Date(selected * 1000).toISOString()}>{exactTime}</time>
        {samples.map(item => {
          const exactValue = `${exactNumber.format(item.value)} ${definition.unit}`
          return <span key={`${item.service}/${item.instance}`}>
            <span>{item.service} <small title={item.instance}>
              <span aria-hidden="true">{item.instance.slice(0, 8)}</span>
              <span className="performance-sr-only">{item.instance}</span>
            </small></span>
            <strong title={exactValue}>
              <span aria-hidden="true">{id === 'cpu' ? `${cpuNumber.format(item.value)} ${definition.unit}` : exactValue}</span>
              <span className="performance-sr-only">{exactValue}</span>
            </strong>
          </span>
        })}
      </output>
    </div>}</>}
  </section>
}
