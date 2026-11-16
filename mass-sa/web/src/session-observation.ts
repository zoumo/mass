import { computed, ref, watch, onScopeDispose, type ComputedRef } from 'vue'
import { readAPI } from './api'
import type { History, SessionEvent } from './resource-api'

// Keep live fragments separate from authoritative facts. A complete event closes
// only the corresponding preview kind; tool call and result remain distinct.
function itemKey(event: SessionEvent) { return `${event.itemId || event.eventId}:${event.type.replace(/_chunk$/, '')}` }
export function useSessionObservation(id: ComputedRef<string>, params: ComputedRef<Record<string, string>>, initialPosition: () => { cursor: string; page: boolean } = () => ({ cursor: '', page: false })) {
  const events = ref<SessionEvent[]>([]), state = ref('未连接'), disconnectedAt = ref(''), error = ref(''), lastSuccess = ref('')
  const paused = ref(false), pending = ref(0), earlier = ref<string[]>([]), loadingEarlier = ref(false), hasTrimmed = ref(false)
  let facts: SessionEvent[] = [], previews: SessionEvent[] = [], checkpoint = '', source: EventSource | undefined, timer: ReturnType<typeof setTimeout> | undefined
  let generation = 0, stopped = true, abort: AbortController | undefined, bookmarksInitialized = false, hasCheckpoint = false, fillingHistory = true
  function previewItems() {
    const groups = new Map<string, SessionEvent[]>()
    for (const event of previews) {
      const key = itemKey(event), group = groups.get(key) || []
      group.push(event); groups.set(key, group)
    }
    return [...groups.values()].map(group => {
      const complete = [...group].reverse().find(e => !e.type.endsWith('_chunk'))
      const last = complete || group[group.length - 1]!
      return { ...last, eventId: group[0]!.eventId, text: complete ? complete.text : group.map(e => e.text).join(''), payload: { ...last.payload, previewEventIds: group.map(e => e.eventId) } }
    })
  }
  function publish() {
    if (paused.value && !fillingHistory) { pending.value++; return }
    events.value = [...facts, ...previewItems()]; pending.value = 0
  }
  function mergeFacts(incoming: SessionEvent[], prepend = false) {
    if (!incoming.length) return
    const byId = new Map(facts.map(e => [e.eventId, e]))
    for (const event of incoming) byId.set(event.eventId, event)
    const order = prepend ? [...incoming, ...facts] : [...facts, ...incoming]
    const seen = new Set<string>()
    facts = order.filter(e => { if (seen.has(e.eventId)) return false; seen.add(e.eventId); return true }).map(e => byId.get(e.eventId)!)
    const completed = new Set(facts.map(itemKey))
    previews = previews.filter(e => !completed.has(itemKey(e)) && !facts.some(f => f.eventId === e.eventId))
    if (facts.length > 500) { facts = prepend ? facts.slice(0, 500) : facts.slice(-500); hasTrimmed.value = true }
    publish()
  }
  function preview(event: SessionEvent) {
    const filter = params.value.eventTypes?.split(',').filter(Boolean) || []
    if (filter.length && !filter.includes(event.type.replace(/_chunk$/, '')) && !event.type.startsWith('session.')) return
    if (facts.some(e => e.eventId === event.eventId || itemKey(e) === itemKey(event))) return
    // Dedup only an identical preview payload; a complete update with the same ID
    // must still replace a preview, and adjacent chunks retain their source order.
    const duplicate = previews.findIndex(e => e.eventId === event.eventId && e.type === event.type)
    if (duplicate >= 0) previews[duplicate] = event
    else previews.push(event)
    if (previews.length > 200) { previews = previews.slice(-200); hasTrimmed.value = true }
    publish()
  }
  function close() { stopped = true; source?.close(); source = undefined; if (timer) clearTimeout(timer); timer = undefined; abort?.abort() }
  function connect(run: number) {
    if (stopped || run !== generation || !id.value || !navigator.onLine) return
    state.value = disconnectedAt.value ? '重连并补读持久历史' : '建立观察'
    const query = new URLSearchParams({ ...params.value, cursor: checkpoint, window: hasCheckpoint ? 'page' : 'recent' })
    source?.close()
    const connection = new EventSource(`/observer/api/sessions/${encodeURIComponent(id.value)}/events?${query}`)
    source = connection
    connection.addEventListener('status', event => {
      if (run !== generation) return
      const value = JSON.parse((event as MessageEvent).data) as { state: string; at: string; reason?: string }
      state.value = value.state === 'disconnected' ? '观察已断开' : '已订阅，正在补读持久历史'
      if (value.state === 'disconnected') { disconnectedAt.value = value.at; error.value = value.reason || '观察中断' }
    })
    connection.addEventListener('history', event => {
      if (run !== generation) return
      const page = JSON.parse((event as MessageEvent).data) as History
      mergeFacts(page.events); lastSuccess.value = page.observedAt
      if (!bookmarksInitialized) { earlier.value = page.earlierCursors; bookmarksInitialized = true }
    })
    connection.addEventListener('preview', event => { if (run === generation) preview(JSON.parse((event as MessageEvent).data) as SessionEvent) })
    connection.addEventListener('checkpoint', event => {
      if (run !== generation) return
      checkpoint = (JSON.parse((event as MessageEvent).data) as { cursor: string }).cursor
      hasCheckpoint = true
      fillingHistory = false
      state.value = '实时观察 · 持久历史已补读'; error.value = ''
    })
    connection.onerror = () => {
      connection.close()
      if (source === connection) source = undefined
      if (stopped || run !== generation) return
      if (state.value.startsWith('实时观察')) disconnectedAt.value = new Date().toISOString()
      disconnectedAt.value ||= new Date().toISOString(); state.value = '观察已断开 · stale'
      error.value ||= '连接中断，2 秒后重连；保留已有记录'
      // History remains useful even if a closed/offline Session cannot subscribe.
      if (!facts.length) {
        abort = new AbortController()
        readAPI<History>(`sessions/${encodeURIComponent(id.value)}/history`, { ...params.value, cursor: checkpoint, window: hasCheckpoint ? 'page' : 'recent' }, abort.signal)
          .then(page => { if (run === generation) { mergeFacts(page.events); earlier.value = page.earlierCursors; lastSuccess.value = page.observedAt; checkpoint = page.cursor; hasCheckpoint = true; fillingHistory = false } })
          .catch(() => { if (run === generation) error.value = '实时连接与持久历史读取失败；保留旧记录' })
      }
      timer = setTimeout(() => connect(run), 2000)
    }
  }
  watch(() => JSON.stringify([id.value, params.value]), () => {
    close(); generation++; facts = []; previews = []; checkpoint = ''; events.value = []; earlier.value = []; pending.value = 0; bookmarksInitialized = false; hasCheckpoint = false
    const position = initialPosition()
    checkpoint = position.cursor; hasCheckpoint = position.page; fillingHistory = true
    error.value = ''; lastSuccess.value = ''; disconnectedAt.value = ''; hasTrimmed.value = false
    stopped = false; loadingEarlier.value = false; if (id.value) connect(generation)
  }, { immediate: true })
  watch(paused, value => { if (!value) publish() })
  async function loadEarlier() {
    if (!earlier.value.length || loadingEarlier.value) return
    const cursor = earlier.value[earlier.value.length - 1]!, run = generation
    loadingEarlier.value = true
    try {
      abort = new AbortController()
      const page = await readAPI<History>(`sessions/${encodeURIComponent(id.value)}/history`, { ...params.value, cursor, window: 'page' }, abort.signal)
      if (run === generation) {
        earlier.value.pop(); mergeFacts(page.events, true)
        if (paused.value) {
          const visibleIds = new Set(events.value.filter(e => !e.preview).map(e => e.eventId))
          events.value = [...[...page.events.filter(e => !visibleIds.has(e.eventId)), ...events.value.filter(e => !e.preview)].slice(0, 500), ...events.value.filter(e => e.preview)]
        }
        return cursor
      }
    } catch { if (run === generation) error.value = '更早历史读取失败；保留已有记录' }
    finally { if (run === generation) loadingEarlier.value = false }
  }
  function offline() {
    if (stopped || !id.value) return
    source?.close(); source = undefined; if (timer) clearTimeout(timer)
    disconnectedAt.value = new Date().toISOString(); state.value = '观察已断开 · stale'; error.value = '浏览器网络离线；保留已有记录，网络恢复后补读持久历史'
  }
  function online() { if (!stopped && id.value) { if (timer) clearTimeout(timer); connect(generation) } }
  window.addEventListener('offline', offline)
  window.addEventListener('online', online)
  onScopeDispose(() => { close(); window.removeEventListener('offline', offline); window.removeEventListener('online', online) })
  return { events, state, disconnectedAt, error, lastSuccess, paused, pending, earlier, loadEarlier, loadingEarlier, hasTrimmed, previewCount: computed(() => events.value.filter(e => e.preview).length) }
}
