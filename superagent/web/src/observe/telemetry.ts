import { ROOT_CONTEXT, SpanKind, SpanStatusCode, trace, type Context, type Span } from '@opentelemetry/api'
import { W3CTraceContextPropagator } from '@opentelemetry/core'
import { OTLPMetricExporter } from '@opentelemetry/exporter-metrics-otlp-proto'
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto'
import { resourceFromAttributes } from '@opentelemetry/resources'
import { MeterProvider, PeriodicExportingMetricReader } from '@opentelemetry/sdk-metrics'
import { BatchSpanProcessor, TracerProvider } from '@opentelemetry/sdk-trace'

const apiCollections = new Set([
  'session-bindings',
  'internal-events',
  'issues',
  'messages',
  'outbox',
  'reports',
  'threads',
  'topics',
  'watchdog',
])

function resourceRequest(path: string) {
  const [collection, id, action] = path.split('?', 1)[0].split('/')
  if (path === 'status') return { endpoint: '/status', api: 'status' }
  if (collection === 'performance' || collection === 'traces') {
    return { endpoint: '/apis/core/v1/telemetry/' + path, api: collection }
  }
  const resource = collection === 'messages' ? 'trigger-messages' : collection === 'outbox' ? 'outbox-messages' : collection
  const api = !apiCollections.has(collection) ? undefined : action && id ? `${resource}/${action}` : resource
  return { endpoint: '/apis/core/v1/' + resource + path.slice(collection.length), api }
}

type Outcome = 'ok' | 'error' | 'canceled'
const viewAttribute = 'superagent.observer.view'
const outcomeAttribute = 'superagent.outcome'

function instanceId() {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')
}

function createTelemetry() {
  const url = (signal: 'metrics' | 'traces') => new URL(`/apis/core/v1/telemetry/${signal}`, location.origin).href
  const resource = resourceFromAttributes({
    'service.name': 'superagent-web',
    'service.instance.id': instanceId(),
  })
  const traces = new TracerProvider({
    resource,
    spanProcessors: [
      new BatchSpanProcessor({
        exporter: new OTLPTraceExporter({ url: url('traces'), timeoutMillis: 3000, concurrencyLimit: 1 }),
        exportTimeoutMillis: 3000,
        scheduledDelayMillis: 2000,
        maxQueueSize: 128,
        maxExportBatchSize: 32,
      }),
    ],
  })
  const metrics = new MeterProvider({
    resource,
    readers: [
      new PeriodicExportingMetricReader({
        exporter: new OTLPMetricExporter({ url: url('metrics'), timeoutMillis: 3000, concurrencyLimit: 1 }),
        exportTimeoutMillis: 3000,
        exportIntervalMillis: 5000,
      }),
    ],
  })
  const meter = metrics.getMeter('superagent.observer')
  const duration = {
    unit: 's',
    advice: { explicitBucketBoundaries: [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10] },
  }
  // The browser's BatchSpanProcessor already flushes traces on hide.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) void metrics.forceFlush().catch(() => undefined)
  })
  return {
    tracer: traces.getTracer('superagent.observer'),
    propagator: new W3CTraceContextPropagator(),
    httpDuration: meter.createHistogram('http.client.request.duration', duration),
    viewDuration: meter.createHistogram('superagent.observer.view.duration', duration),
    liveActive: meter.createUpDownCounter('superagent.observer.live.active', { unit: '{connection}' }),
    liveEnded: meter.createCounter('superagent.observer.live.ended', { unit: '{connection}' }),
  }
}

let sdk: ReturnType<typeof createTelemetry> | undefined

export function startObserverTelemetry() {
  if (import.meta.env.VITE_SUPERAGENT_WEB_TELEMETRY !== 'on') return
  try {
    sdk = createTelemetry()
  } catch {
    return
  }
  const telemetry = sdk
  const navigation = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
  if (!navigation) return
  const record = () => {
    const span = telemetry.tracer.startSpan('observer.document', { startTime: performance.timeOrigin })
    span.end(performance.timeOrigin + (navigation.loadEventEnd || performance.now()))
  }
  if (document.readyState === 'complete') record()
  else window.addEventListener('load', () => window.setTimeout(record, 0), { once: true })
}

function finish(span: Span, outcome: Outcome) {
  span.setAttribute(outcomeAttribute, outcome)
  if (outcome === 'error') span.setStatus({ code: SpanStatusCode.ERROR })
  span.end()
}

export async function measureView<T>(
  view: string,
  signal: AbortSignal,
  load: (parent?: Context) => Promise<T>,
): Promise<T> {
  const telemetry = sdk
  if (!telemetry) return load()
  const start = performance.now()
  const attributes = { [viewAttribute]: view }
  const span = telemetry.tracer.startSpan('observer.view', { attributes })
  const context = trace.setSpan(ROOT_CONTEXT, span)
  let outcome: Outcome = 'error'
  try {
    const result = await load(context)
    outcome = 'ok'
    return result
  } finally {
    if (signal.aborted) outcome = 'canceled'
    finish(span, outcome)
    telemetry.viewDuration.record((performance.now() - start) / 1000, { ...attributes, [outcomeAttribute]: outcome }, context)
  }
}

export async function observerRequest<T>(
  path: string,
  signal: AbortSignal,
  consume: (response: Response) => T | Promise<T>,
  parent = ROOT_CONTEXT,
): Promise<T> {
  const { endpoint, api } = resourceRequest(path)
  const options: RequestInit = { method: 'GET', credentials: 'omit', signal, mode: 'same-origin', redirect: 'error' }
  const telemetry = sdk
  if (!telemetry || !api) return consume(await fetch(endpoint, options))

  const start = performance.now()
  const attributes = { 'http.request.method': 'GET', 'superagent.observer.api': api }
  const span = telemetry.tracer.startSpan('observer.http', { kind: SpanKind.CLIENT, attributes }, parent)
  const context = trace.setSpan(parent, span)
  const headers: Record<string, string> = {}
  telemetry.propagator.inject(context, headers, {
    set: (carrier, key, value) => { carrier[key] = value },
  })
  let outcome: Outcome = 'error'
  let status: number | undefined
  try {
    const response = await fetch(endpoint, { ...options, headers })
    status = response.status
    span.setAttribute('http.response.status_code', status)
    const result = await consume(response)
    if (response.ok) outcome = 'ok'
    return result
  } finally {
    if (signal.aborted) outcome = 'canceled'
    finish(span, outcome)
    telemetry.httpDuration.record((performance.now() - start) / 1000, {
      ...attributes,
      ...status && { 'http.response.status_code': status },
      [outcomeAttribute]: outcome,
    }, context)
  }
}

export function liveConnected() {
  sdk?.liveActive.add(1)
}

export function liveEnded(connected: boolean, outcome: Outcome) {
  if (connected) sdk?.liveActive.add(-1)
  sdk?.liveEnded.add(1, { [outcomeAttribute]: outcome })
}
