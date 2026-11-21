import { viewHref } from './observer'

export type TraceAssociation = { traceId: string; phase: string }

export function traceHref(id: string, at = '', span = '') {
  return viewHref('trace', id, { at, span })
}

export function TraceLink({ id, at, span, compact }: { id: string; at?: string; span?: string; compact?: boolean }) {
  return <a className="trace-link" href={traceHref(id, at, span)} aria-label={compact ? '查看 Trace' : `查看 Trace ${id}`}>
    {compact ? 'Trace' : <code>{id}</code>}
  </a>
}

export function TraceLinks({ associations, at, label = true }: { associations?: TraceAssociation[]; at?: string; label?: boolean }) {
  const ids = [...new Set(associations?.map(association => association.traceId))]
  return ids.length ? <span className="trace-links">{label && <span>Trace</span>}{ids.map(id => <TraceLink key={id} id={id} at={at}/>)}</span>
    : <span className="trace-unavailable">无关联 Trace</span>
}
