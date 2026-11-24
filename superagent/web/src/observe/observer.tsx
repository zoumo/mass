import { createContext, useContext, useEffect, useRef, useState, useSyncExternalStore } from 'react'
import type { Context } from '@opentelemetry/api'
import type { WorkbenchRow, Page } from '../workbench'
import { measureView, observerRequest } from './telemetry'

export type Perspective = 'message' | 'router' | 'topic' | 'issue' | 'topic-conversation' | 'trace' | 'metrics' | 'watchdog'
export const defaultPerspective = 'topic-conversation'
export const watchdogPath = '/superagent/watchdog'
export type ViewProps = { query: URLSearchParams }
export type MessageRecord = WorkbenchRow & {
  id: string
  sourceMessageId?: string
  accountId?: string
  eventId?: string
}

export function viewHref(view: Perspective, id = '', filters: Record<string, string> = {}) {
  if (view === 'watchdog') return watchdogPath
  const query = new URLSearchParams({ perspective: view })
  if (id) query.set('id', id)
  for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value)
  return '/superagent/?' + query
}

export function navigate(href: string) {
  const current = location.pathname + location.search
  if (href === current) return
  const target = new URL(href, location.origin)
  const previous = new URL(current, location.origin)
  const workspace = (url: URL) => url.pathname === watchdogPath
    ? 'watchdog'
    : url.searchParams.get('perspective') || url.searchParams.get('view') || defaultPerspective
  history.pushState({ ...history.state, observerPrevious: current }, '', href)
  if (workspace(target) !== workspace(previous)) window.scrollTo(0, 0)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

const listenLocation = (onChange: () => void) => {
  window.addEventListener('popstate', onChange)
  return () => window.removeEventListener('popstate', onChange)
}
export function useObserverLocation() {
  return new URL(useSyncExternalStore(listenLocation, () => location.href))
}

export const RefreshContext = createContext({
  revision: 0,
  online: true,
  view: defaultPerspective as Perspective | 'diagnostics',
})
export const useRefresh = () => useContext(RefreshContext)

export async function readObserver<T>(path: string, signal: AbortSignal, parent?: Context): Promise<T> {
  return observerRequest(path, signal, response => {
    if (!response.ok) throw new Error(response.status === 404 ? '记录缺失（404）' : `读取失败（HTTP ${response.status}）`)
    return response.json() as Promise<T>
  }, parent)
}

function errorText(reason: unknown) {
  return reason instanceof TypeError ? '无法连接 Core' : reason instanceof Error ? reason.message : '读取失败'
}

// Each scope owns its in-flight read. A background refresh cannot interrupt a
// slow page or an explicit "load more", while navigation/offline cancels both.
function useReadScope(path: string, online: boolean) {
  const scope = useRef<{ signal: AbortSignal, busy: boolean } | null>(null)
  useEffect(() => {
    if (!path || !online) { scope.current = null; return }
    const controller = new AbortController()
    scope.current = { signal: controller.signal, busy: false }
    return () => controller.abort()
  }, [path, online])
  return scope
}

type ReadState<T> = { path: string, data?: T, loading: boolean, error?: string }
export function useRecord<T>(path: string) {
  const { revision, online, view } = useRefresh()
  const [state, set] = useState<ReadState<T>>({ path, loading: true })
  const scope = useReadScope(path, online)
  useEffect(() => {
    const request = scope.current
    if (!request || request.busy) return
    request.busy = true
    set(old => old.path === path ? { ...old, loading: true } : { path, loading: true })
    measureView(view, request.signal, parent => readObserver<T>(path, request.signal, parent)).then(data => {
      if (!request.signal.aborted) set({ path, data, loading: false })
    }).catch(reason => {
      if (!request.signal.aborted) set(old => ({ ...old, loading: false, error: errorText(reason) }))
    }).finally(() => { request.busy = false })
  }, [path, revision, online, scope, view])
  return state.path === path ? state : { path, loading: true, data: undefined, error: undefined }
}

type Pages<T> = ReadState<T[]> & { next?: string, count: number }
function savedCount(path: string): number {
  return history.state?.observerPages?.[path] || 1
}

export function usePages<T>(path: string) {
  const { revision, online, view } = useRefresh()
  const [state, set] = useState<Pages<T>>({ path, loading: true, count: 0 })
  const scope = useReadScope(path, online)
  function readPage(cursor: string, signal: AbortSignal, parent?: Context) {
    const url = new URL(path, location.origin + '/')
    if (cursor) url.searchParams.set('cursor', cursor)
    return readObserver<Page<T>>(url.pathname.slice(1) + url.search, signal, parent)
  }
  useEffect(() => {
    const request = scope.current
    if (!request || request.busy) return
    request.busy = true
    const { signal } = request
    set(old => old.path === path ? { ...old, loading: true } : { path, loading: true, count: 0 })
    measureView(view, signal, async parent => {
      const items: T[] = []
      let next = '', count = 0
      const desired = savedCount(path)
      do {
        const page = await readPage(next, signal, parent)
        items.push(...page.items)
        next = page.nextCursor || ''
        count++
      } while (next && count < desired)
      if (!signal.aborted) set({ path, data: items, next, count, loading: false })
    }).catch(reason => {
      if (!signal.aborted) set(old => ({ ...old, loading: false, error: errorText(reason) }))
    }).finally(() => { request.busy = false })
  }, [path, revision, online, scope, view])
  async function more() {
    const request = scope.current
    const cursor = state.next
    if (!online || !cursor || !request || request.signal.aborted || state.loading || request.busy) return
    request.busy = true
    const { signal } = request
    set(old => ({ ...old, loading: true }))
    try {
      const page = await measureView(view, signal, parent => readPage(cursor, signal, parent))
      if (signal.aborted) return
      const count = state.count + 1
      history.replaceState({ ...history.state, observerPages: { ...history.state?.observerPages, [path]: count } }, '')
      set({ path, data: [...state.data || [], ...page.items], next: page.nextCursor, count, loading: false })
    } catch (reason) {
      if (!signal.aborted) set(old => ({ ...old, loading: false, error: errorText(reason) }))
    } finally {
      request.busy = false
    }
  }
  const current = state.path === path ? state : { path, loading: true, count: 0, data: undefined, next: undefined, error: undefined }
  return { ...current, more }
}
