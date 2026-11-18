<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Drawer from 'primevue/drawer'
import Button from 'primevue/button'
import InputText from 'primevue/inputtext'
import Tag from 'primevue/tag'
import SessionPreview from './SessionPreview.vue'
import WorkbenchDetail from './WorkbenchDetail.vue'
import { formatTime, isWorkbench, messageTitle, relatedQueries, sourceLabel, stateLabel, workbenches } from './workbench'
import type { Page, RelatedPages, WorkbenchRow } from './workbench'

type Row = WorkbenchRow & {
 type?: string
 canonicalUrl?: string; lastMessageAt?: string; subscriptionId?: string; pullRequestId?: string
 ref?: { source: string; id: string }
 close?: { at: string; resourceVersion?: string; method: string; reusable: boolean }
 latestRequirementsNotification?: Row
 followup?: Row
 workObservation?: { state: string; observedAt: string; sessionId: string; sessionUid: string; generation: number; workId?: string; provider?: string; continuous: boolean; observationGap?: string; end?: { eventId: string; at: string; reason: string } }
 reminder?: Row
 evidence?: string[]; questions?: string[]
 preparation?: Row; startup?: Row
 messageId?: string; authorRole?: string; authorId?: string; instanceId?: string
 connection?: string; admission?: string; role?: string
 possibleDuplicate?: boolean; failureEventId?: string; errorCode?: string; retryAt?: string
 channel?: { type: string; conversationId: string; messageId: string; actorId?: string; address?: Record<string, unknown> }
 attempts?: {
  id: string; state: string; startedAt?: string; taskId?: string; taskAt?: string; errorCode?: string
  claims: { generation: number; mode: string; leaseExpiresAt: string; releasedAt?: string }[]
  results?: { outcome: string; at: string; generation: number; conversationId?: string; messageId?: string; errorCode?: string }[]
 }[]
 stop?: { status: string; workState: string; requestedAt: string; observedAt?: string; errorCode?: string; observationGap?: string; receipt?: { result: string; target?: string }; observation?: { eventId: string; state: string; at: string; stopReason?: string } }
 [key: string]: unknown
}
const deliveryKinds = ['deliveries', 'batches', 'attempts', 'recoveries'] as const
const workKinds = ['issues', 'executions', 'reports', 'timers', 'pull-requests'] as const
const kinds = ['messages', 'threads', 'outbox', 'topics', ...workKinds, 'decisions', 'subscriptions', 'relations', 'bindings', 'events', ...deliveryKinds, 'channel-health'] as const
type Kind = typeof kinds[number]
const collectionLabels: Record<Kind, string> = {
 messages: '消息', threads: '线程', outbox: '回执', topics: 'Topic', issues: 'Issue',
 executions: '执行记录', reports: '报告', timers: '定时器', 'pull-requests': '拉取请求',
 decisions: '路由决定', subscriptions: '订阅', relations: '路由关系', bindings: '绑定',
 events: '事件', deliveries: '投递记录', batches: '投递批次', attempts: '投递尝试',
 recoveries: '恢复记录', 'channel-health': '渠道健康状态',
}
const viewLabel = (view: Kind) => collectionLabels[view]
const reminderBasis: Record<string, string> = {
 execution_retired: '执行已停用', report_received: '已收到本次执行的报告',
 already_scheduled: '已安排一次提醒', awaiting_correlated_end: '等待关联的原生任务结束',
 end_unconfirmed: '原生任务结束状态未确认',
}
const initial = new URLSearchParams(location.search)
const kind = ref<Kind>(kinds.includes(initial.get('view') as Kind) ? initial.get('view') as Kind : 'topics')
const recordsMode = ref(initial.get('mode') === 'records')
const workbench = computed(() => !recordsMode.value && isWorkbench(kind.value) ? kind.value : undefined)
const diagnosticsOpen = ref(!workbench.value)
const selectedID = ref(initial.get('id') || '')
const threadKey = ref(initial.get('threadKey') || '')
const draftThread = ref(threadKey.value)
type FilterScope = 'topicId' | 'issueId' | 'messageId' | 'threadKey'
const parentScope = ref<FilterScope>(initial.has('issueId') ? 'issueId' : initial.has('messageId') ? 'messageId' : 'topicId')
const parentFilter = computed(() => {
 if (kind.value === 'outbox' || kind.value === 'decisions') return parentScope.value
 if (kind.value === 'events') return 'messageId'
 if (kind.value === 'pull-requests' || kind.value === 'relations') return parentScope.value
 if (['issues', 'timers', 'subscriptions'].includes(kind.value)) return 'topicId'
 if (kind.value === 'executions' || kind.value === 'reports') return 'issueId'
 return ''
})
const filtersThread = computed(() => kind.value === 'messages' || kind.value === 'subscriptions' || kind.value === 'outbox')
const parent = ref(initial.get(parentFilter.value) || '')
const rows = ref<Row[]>([])
const detail = ref<Row>()
const related = ref<RelatedPages>({})
const topicTitles = ref<Record<string, string>>({})
const displayedID = computed(() => selectedID.value || (workbench.value && rows.value[0] ? identity(rows.value[0]) : ''))
const detailsOpen = ref(false)
const next = ref('')
const stale = ref('')
const detailError = ref('')
const loading = ref(false)
const paused = ref(false)
type Counts = Record<string, number>
type Health = {
 ingress: string; router: string; channelStates: Counts; channelConnections: Counts; channelAdmissions: Counts; topicStates: Counts; issueStates: Counts
 deliveryStates: Counts; outboxStates: Counts; unboundEvents: number; unknownStops: number; unknownEchoes: number
}
const health = ref<Health>()
const count = (counts: Counts, keys: string[]) => keys.reduce((total, key) => total + (counts[key] || 0), 0)
const statusCounts = (counts: Counts) => Object.entries(counts).map(([state, amount]) => `${amount} ${stateLabel(state)}`).join(' · ')
const isDeliveryView = computed(() => deliveryKinds.some(view => view === kind.value))
const replyLabel = (status: string) => status === 'sent' ? '已发送' : stateLabel(status)
function followupLabel(followup?: Row) {
 if (!followup) return '—'
 if (!followup.deliveryId) return followup.invalidated ? '已替代' : '等待 Session'
 return stateLabel(followup.status)
}
let generation = 0
let pageCount = 1
let timer: ReturnType<typeof setInterval> | undefined
let controller: AbortController | undefined
let detailController: AbortController | undefined
const pageSize = 50
const identity = (row: Row) => String(row.id || row.key || '')
function clearDetail() {
 detailController?.abort()
 selectedID.value = ''
 detail.value = undefined
 related.value = {}
 topicTitles.value = {}
 detailError.value = ''
 detailsOpen.value = false
}
const open = computed({
 get: () => workbench.value ? detailsOpen.value : !!selectedID.value,
 set: (value: boolean) => {
  if (value) return
  if (workbench.value) detailsOpen.value = false
  else { clearDetail(); syncURL(false) }
 },
})
function syncURL(push = true) {
 const q = new URLSearchParams({ view: kind.value })
 if (selectedID.value) q.set('id', selectedID.value)
 if (threadKey.value && filtersThread.value) q.set('threadKey', threadKey.value)
 if (parent.value && parentFilter.value) q.set(parentFilter.value, parent.value)
 if (recordsMode.value && isWorkbench(kind.value)) q.set('mode', 'records')
 const url = `${location.pathname}?${q}`
 if (url === location.pathname + location.search) return
 if (push) history.pushState(null, '', url)
 else history.replaceState(null, '', url)
}
async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
 const response = await fetch(`/v1/observer/${path}`, { method: 'GET', signal, credentials: 'omit' }).catch(error => {
  if (signal?.aborted) throw error
  throw new Error('无法连接核心服务')
 })
 if (!response.ok) throw new Error(`核心服务查询失败 (${response.status})`)
 return response.json() as Promise<T>
}
async function load(append = false) {
 if (append && (!next.value || loading.value)) return
 controller?.abort()
 controller = new AbortController()
 const signal = controller.signal
 const current = ++generation
 loading.value = true
 try {
  const q = new URLSearchParams({ limit: String(pageSize), preview: 'true' })
  if (workbench.value) q.set('order', 'newest')
  if (threadKey.value && filtersThread.value) q.set('threadKey', threadKey.value)
  if (parent.value && parentFilter.value) q.set(parentFilter.value, parent.value)
  const view = kind.value
  let cursor = append ? next.value : ''
  const items: Row[] = []
  const status = await get<Health>('health', signal)
  for (let index = 0; index < (append ? 1 : pageCount); index++) {
   if (cursor) q.set('cursor', cursor)
   const page = await get<Page<Row>>(`${view}?${q}`, signal)
   items.push(...page.items)
   cursor = page.nextCursor || ''
   if (!cursor) break
  }
  if (current !== generation) return
  const previousID = displayedID.value
  rows.value = append ? [...rows.value, ...items] : items
  if (displayedID.value !== previousID) {
   detail.value = undefined
   related.value = {}
   topicTitles.value = {}
   detailError.value = ''
   void loadDetail()
  }
  if (append) pageCount++
  next.value = cursor
  health.value = status
  stale.value = ''
 } catch (error) { if (current === generation && !signal.aborted) stale.value = String(error) }
 finally { if (current === generation) loading.value = false }
}
async function loadDetail() {
 detailController?.abort()
 if (!displayedID.value) return
 detailController = new AbortController()
 const { signal } = detailController
 const id = displayedID.value, view = kind.value
 try {
  const result = await get<Row>(`${view}/${encodeURIComponent(id)}`, signal)
  if (!signal.aborted) { detail.value = result; detailError.value = '' }
 } catch (error) {
  if (!signal.aborted) detailError.value = String(error)
  return
 }
 if (signal.aborted || !workbench.value) return
 const queries = relatedQueries(workbench.value, id)
 const topicIDs = [...new Set([detail.value?.topicId, ...detail.value?.relations?.map(relation => relation.topicId) || []].filter((id): id is string => !!id))]
 void Promise.all(topicIDs.slice(0, 8).map(async topicID => {
  try {
   const topic = await get<Row>(`topics/${encodeURIComponent(topicID)}`, signal)
   if (!signal.aborted && topic.title) topicTitles.value[topicID] = topic.title
  } catch { /* The recorded Topic ID still identifies the link. */ }
 }))
 const pages = await Promise.all(queries.map(async ([collection, query]) => {
  try {
   return [collection, await get<Page>(`${collection}?${query}`, signal)] as const
  } catch (error) {
   return [collection, new Error(String(error))] as const
  }
 }))
 if (!signal.aborted) related.value = Object.fromEntries(pages)
}
function refresh() {
 void load()
 void loadDetail()
}
async function select(row: Row) {
 const alreadyDisplayed = identity(row) === displayedID.value && detail.value
 selectedID.value = identity(row)
 syncURL()
 if (alreadyDisplayed) return
 detail.value = undefined
 related.value = {}
 topicTitles.value = {}
 detailError.value = ''
 await loadDetail()
}
function openView(view: Kind, filterID: string, scope: FilterScope, id = '', records = false) {
 clearDetail()
 kind.value = view
 recordsMode.value = records
 diagnosticsOpen.value = !workbench.value
 selectedID.value = id
 parentScope.value = scope
 threadKey.value = scope === 'threadKey' ? filterID : ''
 draftThread.value = threadKey.value
 parent.value = scope === 'threadKey' ? '' : filterID
 rows.value = []
 next.value = ''
 pageCount = 1
}
function navigate(view: Kind, filterID = '', scope: FilterScope = 'topicId', id = '', records = false) {
 openView(view, filterID, scope, id, records)
 syncURL()
 refresh()
}
function showRecord(view: Kind, id: string) { navigate(view, '', 'topicId', id, !workbench.value) }
function showThread(key: string) { showRecord('threads', key) }
function filter() { navigate('messages', draftThread.value.trim(), 'threadKey', '', recordsMode.value) }
function clearFilter() { navigate(kind.value, '', 'topicId', '', recordsMode.value) }
function workbenchLink(view: string, id = '', filterID = '', scope: FilterScope = 'topicId') {
 const q = new URLSearchParams({ view })
 if (id) q.set('id', id)
 if (filterID) q.set(scope, filterID)
 return `?${q}`
}
function follow(event: MouseEvent, view: string, id = '', filterID = '', scope: FilterScope = 'topicId') {
 if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return
 if (!kinds.includes(view as Kind)) return
 event.preventDefault()
 navigate(view as Kind, filterID, scope, id)
}
function inspectRecord() {
 if (!selectedID.value && displayedID.value) {
  selectedID.value = displayedID.value
  syncURL()
 }
 detailsOpen.value = true
}
const links = { href: workbenchLink, follow }
function restoreLocation() {
 const q = new URLSearchParams(location.search)
 const view = kinds.includes(q.get('view') as Kind) ? q.get('view') as Kind : 'topics'
 const scope = (['threadKey', 'messageId', 'issueId', 'topicId'] as const).find(key => q.has(key)) || 'topicId'
 const parentKey = (['messageId', 'issueId', 'topicId'] as const).find(key => q.has(key))
 openView(view, q.get(scope) || '', parentKey || scope, q.get('id') || '', q.get('mode') === 'records')
 if (q.has('threadKey')) {
  threadKey.value = q.get('threadKey')!
  draftThread.value = threadKey.value
  parent.value = parentKey ? q.get(parentKey)! : ''
 }
 refresh()
}
function disconnected() {
 controller?.abort()
 detailController?.abort()
 stale.value = '浏览器已离线'
}
onMounted(() => {
 refresh()
 window.addEventListener('offline', disconnected)
 window.addEventListener('online', refresh)
 window.addEventListener('popstate', restoreLocation)
 timer = setInterval(() => { if (!document.hidden && navigator.onLine && !paused.value && !loading.value) refresh() }, 5000)
})
onUnmounted(() => {
 clearInterval(timer)
 controller?.abort()
 detailController?.abort()
 window.removeEventListener('offline', disconnected)
 window.removeEventListener('online', refresh)
 window.removeEventListener('popstate', restoreLocation)
})
</script>

<template>
 <header class="masthead">
  <div class="shell masthead-inner">
   <div class="brand"><i class="pi pi-compass" aria-hidden="true"></i><span>Superagent</span><span class="product">观察台</span></div>
   <div class="connection"><span class="read-only">只读</span><span class="connection-state" :class="{ healthy: health?.ingress === 'ready' && !stale, warning: stale }">{{ stale ? '核心服务快照已过时' : health?.ingress === 'ready' ? '核心服务已就绪' : '正在连接' }}</span></div>
  </div>
  </header>
 <div class="navigation-bar">
  <nav class="shell workbench-nav" aria-label="工作台导航">
   <a v-for="(view, key) in workbenches" :key="key" :href="workbenchLink(key)" :aria-current="workbench === key ? 'page' : undefined" @click="follow($event, key)"><i :class="view.icon" aria-hidden="true"></i>{{ view.label }}</a>
  </nav>
 </div>
 <main class="shell">
  <div class="page-heading">
   <div>
    <h1>{{ workbench ? workbenches[workbench].title : viewLabel(kind) }}</h1>
    <p v-if="workbench">{{ workbenches[workbench].description }}</p>
   </div>
   <div class="refresh-controls">
    <button class="quiet-button" type="button" @click="paused = !paused">{{ paused ? '恢复自动刷新' : '暂停自动刷新' }}</button>
    <button class="outline-button" type="button" :disabled="loading" @click="refresh"><i class="pi pi-refresh" aria-hidden="true"></i> 刷新</button>
   </div>
  </div>
  <details class="diagnostics" :open="diagnosticsOpen" @toggle="diagnosticsOpen = ($event.target as HTMLDetailsElement).open">
   <summary>诊断</summary>
  <section v-if="health" class="overview" aria-label="运行概况">
   <dl aria-label="接收与路由">
    <dt>路由器准备状态</dt><dd>{{ stateLabel(health.router) }}</dd>
    <dt>渠道连接状态</dt><dd>{{ statusCounts(health.channelConnections) || '暂无有效状态上报' }}</dd>
    <dt>渠道接收状态</dt><dd>{{ statusCounts(health.channelAdmissions) || '暂无有效状态上报' }}</dd>
    <dt>已过期的心跳</dt><dd>{{ health.channelStates.stale ?? 0 }}</dd>
   </dl>
   <dl aria-label="业务状态">
    <dt>Topic</dt><dd>{{ statusCounts(health.topicStates) || '无' }}</dd>
    <dt>Issue</dt><dd>{{ statusCounts(health.issueStates) || '无' }}</dd>
    <dt>原生任务停止状态未知</dt><dd>{{ health.unknownStops }}</dd>
   </dl>
   <dl aria-label="输入与回执">
    <dt>待处理输入</dt><dd>{{ health.unboundEvents + count(health.deliveryStates, ['pending', 'dispatching', 'reconciling']) }}</dd>
    <dt>受阻输入</dt><dd>{{ health.deliveryStates.blocked ?? 0 }}</dd>
    <dt>状态未知的输入</dt><dd>{{ count(health.deliveryStates, ['blocked_unknown', 'transport_unknown']) }}</dd>
    <dt>需关注的输入</dt><dd>{{ health.deliveryStates.needsAttention ?? 0 }}</dd>
    <dt>回执</dt><dd>{{ statusCounts(health.outboxStates) || '无' }}</dd>
    <dt>状态未知的回声</dt><dd>{{ health.unknownEchoes }}</dd>
   </dl>
  </section>
   <nav class="collections" aria-label="诊断列表"><Button v-for="view in kinds" :key="view" :label="viewLabel(view)" :text="kind !== view || !!workbench" @click="navigate(view, '', 'topicId', '', true)" /></nav>
  </details>
  <p v-if="stale" role="alert" class="error">快照已过时 · {{ stale }}。当前显示上次成功读取的记录。</p>
  <div v-if="parent && parentFilter || filtersThread && threadKey" class="filter-scope">
   <span v-if="parent && parentFilter">{{ parentFilter === 'topicId' ? 'Topic' : parentFilter === 'messageId' ? '消息' : 'Issue' }} <strong :title="parent">{{ topicTitles[parent] || parent }}</strong></span>
   <span v-if="filtersThread && threadKey">线程 <strong>{{ threadKey }}</strong></span>
   <button class="quiet-button" type="button" @click="clearFilter">清除筛选</button>
  </div>
  <div v-if="workbench" class="workbench">
   <aside class="record-list" :aria-label="workbenches[workbench].list">
    <h2>{{ workbenches[workbench].list }}</h2>
    <form v-if="workbench === 'messages'" class="thread-filter" @submit.prevent="filter">
     <InputText v-model="draftThread" placeholder="输入完整线程键" aria-label="按线程键筛选" />
     <button type="submit" class="outline-button">筛选</button>
    </form>
    <ul v-if="rows.length">
     <li v-for="row in rows" :key="identity(row)">
      <button class="record-button" type="button" :aria-current="displayedID === identity(row) ? 'true' : undefined" @click="select(row)">
       <span class="record-title">{{ workbench === 'messages' ? messageTitle(row) : row.title || identity(row) }}</span>
       <span class="record-meta"><span>{{ workbench === 'messages' ? sourceLabel(row.source) + ' · ' + stateLabel(row.routing?.state || row.echo?.status || row.acceptance) : stateLabel(row.status) }}</span><time :datetime="row.updatedAt || row.receivedAt || row.createdAt">{{ formatTime(row.updatedAt || row.receivedAt || row.createdAt) }}</time></span>
      </button>
     </li>
    </ul>
    <footer v-if="rows.length"><span class="muted">显示 {{ rows.length }} 条{{ viewLabel(kind) }}</span><button v-if="next" class="outline-button" type="button" :disabled="loading" @click="load(true)">加载更多</button></footer>
   </aside>
   <WorkbenchDetail v-if="detail" :key="displayedID" :view="workbench" :record="detail" :related="related" :topic-titles="topicTitles" :links="links" @inspect="inspectRecord" />
   <div v-else class="workbench-empty">
    <i :class="workbenches[workbench].icon" aria-hidden="true"></i>
    <h2>{{ detailError ? '记录不可用' : loading || displayedID ? '正在加载记录…' : stale ? '工作台不可用' : workbench === 'issues' ? '暂无 Issue 记录' : workbench === 'topics' ? '暂无 Topic 记录' : '暂无消息记录' }}</h2>
    <p v-if="detailError" role="alert">{{ detailError }}</p>
    <p v-else-if="!loading && !displayedID && !stale">{{ parent || threadKey ? '没有符合筛选条件的记录。' : workbench === 'issues' ? 'Topic 创建 Issue 后，记录会显示在这里。' : workbench === 'topics' ? '路由器创建 Topic 后，记录会显示在这里。' : '核心服务收到渠道消息后，记录会显示在这里。' }}</p>
    <a v-if="!loading && !displayedID && !stale && workbench !== 'topics'" class="text-link" :href="workbenchLink('topics')" @click="follow($event, 'topics')">查看 Topic <span aria-hidden="true">→</span></a>
   </div>
   <p v-if="detail && detailError" class="error" role="alert">记录已过时 · {{ detailError }}</p>
  </div>
  <template v-else>
  <form v-if="kind === 'messages'" class="toolbar" @submit.prevent="filter"><InputText v-model="draftThread" placeholder="输入完整线程键" aria-label="按线程键筛选" /><Button label="筛选" type="submit" outlined /></form>
  <DataTable :value="rows" :loading="loading" :data-key="kind === 'threads' ? 'key' : 'id'" selection-mode="single" @row-select="select($event.data)" striped-rows table-style="min-width: 40rem">
   <Column v-if="kind === 'messages'" field="text" header="消息"><template #body="{ data }"><button class="row-link preview" @click="select(data)">{{ data.text }}</button></template></Column>
   <Column v-if="kind === 'messages' && rows.some(row => row.echo)" header="回声"><template #body="{ data }">{{ data.echo ? stateLabel(data.echo.status) : '—' }}</template></Column>
   <Column v-if="kind === 'messages'" field="actor" header="发送方" />
   <Column v-if="kind === 'threads'" field="key" header="线程"><template #body="{ data }"><button class="row-link" @click="select(data)">{{ data.key }}</button></template></Column>
   <Column v-if="!['messages', 'threads', 'events', 'channel-health', 'outbox'].includes(kind)" field="id" header="记录"><template #body="{ data }"><button class="row-link" :class="{ preview: kind === 'timers' || kind === 'pull-requests' }" @click="select(data)">{{ data.canonicalUrl || data.title || data.summary || data.content || ((data.role || data.action || data.kind) ? stateLabel(data.role || data.action || data.kind) : data.id) }}</button></template></Column>
   <Column v-if="kind === 'outbox'" field="text" header="回执"><template #body="{ data }"><button class="row-link preview" @click="select(data)">{{ data.text }}</button></template></Column>
   <Column v-if="kind === 'outbox'" field="status" header="发送状态"><template #body="{ data }">{{ replyLabel(data.status) }}</template></Column>
   <Column v-if="kind === 'outbox'" field="authorRole" header="发送角色"><template #body="{ data }">{{ stateLabel(data.authorRole) }}</template></Column>
   <Column v-if="kind === 'channel-health'" field="instanceId" header="实例"><template #body="{ data }"><button class="row-link" @click="select(data)">{{ data.instanceId }}</button></template></Column>
   <Column v-if="kind === 'channel-health'" field="source" header="来源" />
   <Column v-if="kind === 'channel-health'" field="status" header="状态"><template #body="{ data }">{{ stateLabel(data.status) }}</template></Column>
   <Column v-if="kind === 'channel-health'" field="connection" header="上报的连接状态"><template #body="{ data }">{{ stateLabel(data.connection) }}</template></Column>
   <Column v-if="kind === 'channel-health'" field="admission" header="接收状态"><template #body="{ data }">{{ stateLabel(data.admission) }}</template></Column>
   <Column v-if="kind === 'channel-health'" field="backlog" header="缓冲区"><template #body="{ data }">{{ data.backlog }} / {{ data.capacity }}</template></Column>
   <Column v-if="kind === 'bindings'" field="state" header="准备状态"><template #body="{ data }">{{ stateLabel(data.state) }}</template></Column>
   <Column v-if="kind === 'bindings'" field="lastError" header="原因" />
   <Column v-if="kind === 'topics' || kind === 'issues'" field="status" header="业务状态"><template #body="{ data }">{{ stateLabel(data.status) }}</template></Column>
   <Column v-if="kind === 'timers'" field="status" header="定时器状态"><template #body="{ data }">{{ stateLabel(data.status) }}</template></Column>
   <Column v-if="kind === 'timers'" field="dueAt" header="计划触发时间 (UTC)" />
   <Column v-if="kind === 'timers'" field="revision" header="版本" />
   <Column v-if="kind === 'timers'" header="跟进通知投递"><template #body="{ data }">{{ followupLabel(data.followup) }}</template></Column>
   <Column v-if="kind === 'executions'" field="authority" header="执行权"><template #body="{ data }">{{ stateLabel(data.authority) }}</template></Column>
   <Column v-if="kind === 'executions'" header="准备状态"><template #body="{ data }">{{ stateLabel(data.preparationAtRetirement || data.preparation?.state) }}</template></Column>
   <Column v-if="kind === 'executions'" field="startup.status" header="启动输入"><template #body="{ data }">{{ stateLabel(data.startup?.status) }}</template></Column>
   <Column v-if="kind === 'reports'" field="kind" header="报告类型"><template #body="{ data }">{{ stateLabel(data.kind) }}</template></Column>
   <Column v-if="kind === 'reports'" field="historical" header="执行来源"><template #body="{ data }">{{ data.historical ? '历史执行' : '提交时为当前执行' }}</template></Column>
   <Column v-if="kind === 'relations' || kind === 'subscriptions'" field="topicId" header="Topic" />
   <Column v-if="kind === 'bindings'" field="ownerId" header="所属记录" />
   <Column v-if="['relations', 'events'].includes(kind)" :field="kind === 'events' ? 'routeRound' : 'round'" header="轮次" />
   <Column v-if="kind === 'events'" field="type" header="事件"><template #body="{ data }"><button class="row-link" @click="select(data)">{{ data.type }}</button></template></Column>
   <Column v-if="kind === 'messages'" field="source" header="来源" />
   <Column v-if="kind === 'events' || isDeliveryView" field="status" header="状态"><template #body="{ data }">{{ stateLabel(data.status) }}</template></Column>
   <Column v-if="kind === 'deliveries'" field="attempts" header="尝试次数" />
   <Column v-if="kind === 'deliveries'" header="确认回执"><template #body="{ data }">{{ data.ackAt ? '已收到' : '—' }}</template></Column>
   <Column v-if="kind === 'batches'" field="method" header="输入" />
   <Column v-if="kind === 'attempts'" field="errorCode" header="错误" />
   <Column :field="kind === 'channel-health' ? 'acceptedAt' : kind === 'messages' ? 'receivedAt' : kind === 'events' ? 'at' : 'createdAt'" header="记录时间" />
   <template #empty>暂无{{ viewLabel(kind) }}记录。</template>
  </DataTable>
  <footer><span class="muted">显示 {{ rows.length }} 条{{ viewLabel(kind) }}</span><Button v-if="next" label="加载更多" outlined :disabled="loading" @click="load(true)" /></footer>
  </template>
  <Drawer v-model:visible="open" position="right" :header="kind === 'messages' ? '消息详情' : kind === 'threads' ? '线程详情' : viewLabel(kind) + '详情'" style="width: min(52rem, 95vw)">
   <p v-if="detailError" role="alert" class="error">{{ detail ? '记录已过时 · ' : '' }}{{ detailError }}</p><p v-else-if="!detail">正在读取记录…</p>
   <template v-if="detail">
    <Tag v-if="detail.acceptance || detail.status || detail.state" :value="kind === 'outbox' ? replyLabel(String(detail.status)) : stateLabel(String(detail.acceptance || detail.status || detail.state))" severity="info" />
    <h2 v-if="kind === 'events'">{{ detail.type }}</h2>
    <h2 v-if="detail.title">{{ detail.title }}</h2><p v-if="detail.reason">{{ detail.reason }}</p><p v-if="detail.lastError" role="status">{{ kind === 'channel-health' ? stateLabel(String(detail.lastError)) : detail.lastError }}</p>
    <h2 v-if="detail.actor">{{ detail.actor }}</h2><pre v-if="detail.text" class="body-text" data-testid="message-body">{{ detail.text }}</pre>
    <dl><dt>记录标识</dt><dd>{{ identity(detail) }}</dd><dt v-if="detail.source">来源</dt><dd v-if="detail.source">{{ detail.source }}</dd><dt v-if="detail.receivedAt">接收时间</dt><dd v-if="detail.receivedAt">{{ detail.receivedAt }}</dd></dl>
    <dl v-if="kind === 'messages'">
     <dt>渠道消息</dt><dd>{{ detail.sourceMessageId }}</dd>
     <dt>渠道时间</dt><dd>{{ detail.at || '未记录' }}</dd>
     <dt>线程键</dt><dd>{{ detail.threadKey }}</dd>
     <dt>路由状态</dt><dd>{{ detail.routing ? `第 ${detail.routing.round} 轮 · ${stateLabel(detail.routing.state)}` : '无路由记录' }}</dd>
    </dl>
    <dl v-if="kind === 'channel-health'">
     <dt>实例</dt><dd>{{ detail.instanceId }}</dd>
     <dt>连接状态</dt><dd>{{ stateLabel(detail.connection) }} · {{ detail.readyConsumers }} / {{ detail.consumers }} 已就绪</dd>
     <dt>接收状态</dt><dd>{{ stateLabel(detail.admission) }} · {{ detail.backlog }} / {{ detail.capacity }} 已缓冲</dd>
     <dt>最近接收</dt><dd>{{ detail.lastReceivedAt || '—' }}</dd>
     <dt>最近错误</dt><dd>{{ detail.lastErrorAt || '—' }}</dd>
     <dt v-if="detail.stopReason">停机原因</dt><dd v-if="detail.stopReason">{{ stateLabel(String(detail.stopReason)) }}</dd>
     <dt>接收时间</dt><dd>{{ detail.acceptedAt }}</dd>
     <dt>到期时间</dt><dd>{{ detail.expiresAt }}</dd>
    </dl>
    <dl v-if="kind === 'deliveries'">
     <dt>Session</dt><dd>{{ detail.sessionId || '等待投递' }}</dd>
     <dt>尝试次数</dt><dd>{{ detail.attempts || 0 }}</dd>
     <dt>下次尝试</dt><dd>{{ detail.nextAttemptAt || '—' }}</dd>
     <dt>确认回执</dt><dd>{{ detail.ackAt || '等待确认回执' }}</dd>
     <dt v-if="detail.needsAttention">关注状态</dt><dd v-if="detail.needsAttention">需关注</dd>
    </dl>
    <p v-if="detail.observationGap">观测中断 · {{ detail.observationGap }}</p>
    <p v-if="detail.possibleDuplicate">{{ kind === 'outbox' ? '发送结果未确认，恢复时可能重复发送。' : '之前的输入可能已经生效。' }}</p>
    <template v-if="detail.receipt && isDeliveryView">
     <h2>提交回执</h2><pre data-testid="submission-receipt">{{ JSON.stringify(detail.receipt, null, 2) }}</pre>
    </template>
    <Button v-if="detail.deliveryId" label="查看投递记录" text @click="showRecord('deliveries', String(detail.deliveryId))" />
    <Button v-if="detail.batchId" label="批次" text @click="showRecord('batches', String(detail.batchId))" />
    <Button v-if="detail.lastAttemptId" label="最近一次尝试" text @click="showRecord('attempts', String(detail.lastAttemptId))" />
    <Button v-if="detail.eventId && kind === 'deliveries'" label="事件" text @click="showRecord('events', String(detail.eventId))" />
    <Button v-if="kind === 'events' && detail.messageId" label="查看来源消息" text @click="showRecord('messages', detail.messageId!)" />
    <Button v-if="detail.threadKey" :label="`查看线程 ${detail.threadKey}`" text @click="showThread(String(detail.threadKey))" />
    <Button v-if="kind === 'messages'" label="查看消息回执" text @click="navigate('outbox', identity(detail), 'messageId')" />
    <Button v-if="kind === 'messages'" label="查看消息事件" text @click="navigate('events', identity(detail), 'messageId')" />
    <section v-if="kind === 'messages' && detail.relations?.length" aria-label="路由历史">
     <h2>路由历史</h2>
     <ul class="routing-history">
      <li v-for="relation in detail.relations" :key="relation.id">
       <span>第 {{ relation.round }} 轮 · {{ stateLabel(relation.action) }}</span>
       <p>{{ relation.reason }}</p>
       <Button :label="`查看 Topic ${relation.topicId}`" text @click="showRecord('topics', relation.topicId)" />
       <Button v-if="relation.decisionId" label="查看路由决定" text @click="showRecord('decisions', relation.decisionId!)" />
      </li>
     </ul>
    </section>
    <template v-if="kind === 'messages' && detail.echo">
     <h2>{{ stateLabel(detail.echo.status) }}</h2>
     <dl>
      <template v-if="detail.echo.deadline"><dt>等待截止时间</dt><dd>{{ detail.echo.deadline }}</dd></template>
      <template v-if="detail.echo.resolvedAt"><dt>确认时间</dt><dd>{{ detail.echo.resolvedAt }}</dd></template>
      <template v-if="detail.echo.releaseReason"><dt>人工放行原因</dt><dd>{{ detail.echo.releaseReason }}</dd></template>
      <template v-if="detail.echo.match"><dt>匹配的发送记录</dt><dd><Button :label="detail.echo.match.attemptId" text @click="showRecord('outbox', detail!.echo!.match!.outboxId)" /></dd></template>
      <template v-if="detail.echo.candidates?.length"><dt>接收时的关联发送记录</dt><dd v-for="candidate in detail.echo.candidates" :key="candidate.attemptId"><Button :label="candidate.attemptId" text @click="showRecord('outbox', candidate.outboxId)" /></dd></template>
     </dl>
     <Button v-if="detail.echo.routingEventId" label="路由事件" text @click="showRecord('events', detail!.echo!.routingEventId!)" />
    </template>
    <template v-if="kind === 'threads'">
     <dl>
      <dt>来源</dt><dd>{{ detail.ref?.source }}</dd>
      <dt>来源标识</dt><dd>{{ detail.ref?.id }}</dd>
      <dt>最近一条来源消息</dt><dd>{{ detail.lastMessageAt || '暂未收到来源消息' }}</dd>
     </dl>
     <Button label="查看线程消息" outlined @click="navigate('messages', identity(detail), 'threadKey', '', !workbench)" />
     <Button label="查看线程订阅" text @click="navigate('subscriptions', identity(detail), 'threadKey')" />
     <Button label="查看线程回执" text @click="navigate('outbox', identity(detail), 'threadKey')" />
    </template>
    <template v-if="kind === 'outbox'">
     <dl>
      <dt>发送角色</dt><dd>{{ stateLabel(detail.authorRole) }} {{ detail.authorId }}</dd>
      <dt>消费实例</dt><dd>{{ detail.instanceId }}</dd>
      <dt>目标会话</dt><dd>{{ detail.channel?.conversationId }}</dd>
      <dt>渠道目标消息</dt><dd>{{ detail.channel?.messageId }}</dd>
      <template v-if="detail.errorCode"><dt>原因</dt><dd>{{ detail.errorCode }}</dd></template>
      <template v-if="detail.retryAt"><dt>允许重试时间</dt><dd>{{ detail.retryAt }}</dd></template>
     </dl>
     <Button v-if="detail.messageId" label="查看目标消息" text @click="showRecord('messages', detail.messageId!)" />
     <Button v-if="detail.authorRole === 'topic' && detail.authorId" label="查看发送方 Topic" text @click="showRecord('topics', detail.authorId!)" />
     <Button v-if="detail.failureEventId" label="查看失败事件" text @click="showRecord('events', detail.failureEventId!)" />
     <h2>发送尝试</h2>
     <p v-if="!detail.attempts?.length">暂无发送尝试。</p>
     <section v-for="attempt in detail.attempts" :key="attempt.id" class="summary" aria-label="发送尝试">
      <dl>
       <dt>尝试</dt><dd>{{ attempt.id }} · {{ stateLabel(attempt.state) }}</dd>
       <template v-if="attempt.startedAt"><dt>确认时间</dt><dd>{{ attempt.startedAt }}</dd></template>
       <template v-if="attempt.taskId"><dt>渠道任务</dt><dd>{{ attempt.taskId }} · {{ attempt.taskAt }}</dd></template>
       <template v-if="attempt.errorCode"><dt>尝试结果说明</dt><dd>{{ attempt.errorCode }}</dd></template>
       <template v-for="claim in attempt.claims" :key="claim.generation">
        <dt>第 {{ claim.generation }} 次领取</dt><dd>{{ stateLabel(claim.mode) }} · 租约到期 {{ claim.leaseExpiresAt }}<span v-if="claim.releasedAt"> · 已释放 {{ claim.releasedAt }}</span></dd>
       </template>
       <template v-for="(result, index) in attempt.results" :key="index">
        <dt>结果 · 第 {{ result.generation }} 次领取</dt><dd>{{ stateLabel(result.outcome) }} · {{ result.at }}<span v-if="result.errorCode"> · {{ result.errorCode }}</span></dd>
        <template v-if="result.conversationId"><dt>发送到的会话</dt><dd>{{ result.conversationId }}</dd></template>
        <template v-if="result.messageId"><dt>渠道发送结果中的消息</dt><dd>{{ result.messageId }}</dd></template>
       </template>
      </dl>
     </section>
    </template>
    <template v-if="kind === 'pull-requests'">
     <dl><dt>规范拉取请求 URL</dt><dd>{{ detail.canonicalUrl }}</dd><dt>登记时间</dt><dd>{{ detail.createdAt }}</dd></dl>
     <Button v-if="detail.subscriptionId" label="查看订阅" text @click="showRecord('subscriptions', detail.subscriptionId!)" />
     <Button v-if="detail.executionId" label="查看执行记录" text @click="showRecord('executions', String(detail.executionId))" />
    </template>
    <Button v-if="detail.pullRequestId" label="查看登记记录" text @click="showRecord('pull-requests', detail.pullRequestId!)" />
    <Button v-if="detail.topicId" :label="`查看 Topic ${detail.topicId}`" text @click="showRecord('topics', String(detail.topicId))" />
    <Button v-if="detail.bindingId" label="Session 准备记录" text @click="showRecord('bindings', String(detail.bindingId))" />
    <template v-if="kind === 'topics'">
     <p v-if="detail.description">{{ detail.description }}</p>
     <dl><dt>工作目录</dt><dd>{{ detail.workspace }}</dd></dl>
     <Button label="查看 Topic 来源" text @click="navigate('subscriptions', identity(detail))" />
     <Button label="查看 Topic 路由历史" text @click="navigate('relations', identity(detail))" />
    </template>
    <Button v-if="['subscriptions', 'relations'].includes(kind) && detail.messageId" label="查看来源消息" text @click="showRecord('messages', detail.messageId!)" />
    <Button v-if="kind === 'relations' && detail.decisionId" label="查看路由决定" text @click="showRecord('decisions', String(detail.decisionId))" />
    <Button v-if="kind === 'topics'" label="查看 Topic 下的 Issue" text @click="navigate('issues', identity(detail), 'topicId', '', !workbench)" />
    <Button v-if="kind === 'topics'" label="查看 Topic 定时器" text @click="navigate('timers', identity(detail))" />
    <Button v-if="kind === 'topics'" label="查看 Topic 拉取请求" text @click="navigate('pull-requests', identity(detail))" />
    <Button v-if="kind === 'issues'" label="查看 Issue 拉取请求" text @click="navigate('pull-requests', identity(detail), 'issueId')" />
    <Button v-if="detail.issueId" label="查看 Issue" text @click="showRecord('issues', String(detail.issueId))" />
    <dl v-if="detail.cancellation || detail.reopening" data-testid="lifecycle-history">
     <template v-if="detail.cancellation"><dt>取消</dt><dd>{{ detail.cancellation.reason }} · {{ detail.cancellation.at }}</dd></template>
     <template v-if="detail.reopening"><dt>重开</dt><dd>{{ detail.reopening.reason }} · {{ detail.reopening.at }}</dd></template>
    </dl>
    <Button v-if="kind === 'topics' && detail.cancellation && detail.cancellation.bindingId !== detail.bindingId"
     label="已停用的 Topic Session" text @click="showRecord('bindings', detail.cancellation!.bindingId)" />
    <template v-if="kind === 'issues'">
     <h2>要求</h2><pre class="body-text">{{ detail.requirements }}</pre>
     <dl>
      <dt>工作目录</dt><dd>{{ detail.workspace }}</dd>
      <dt>Issue 版本</dt><dd>{{ detail.revision }}</dd>
      <dt>要求版本</dt><dd>{{ detail.requirementsRevision }}</dd>
      <dt>执行器 Session</dt><dd>{{ detail.currentExecutionId ? stateLabel(detail.workerSession?.state) : '当前没有执行' }}<span v-if="detail.workerSession?.observedAt"> · {{ detail.workerSession.observedAt }}</span><span v-if="detail.workerSession?.observationGap"> · {{ detail.workerSession.observationGap }}</span></dd>
      <dt>最近一次要求变更通知</dt><dd v-if="detail.latestRequirementsNotification">
       {{ stateLabel(detail.latestRequirementsNotification.status) }} · 要求版本 {{ detail.latestRequirementsNotification.revision }}
       <Button label="通知事件" text @click="showRecord('events', identity(detail.latestRequirementsNotification!))" />
      </dd><dd v-else>None</dd>
     </dl>
     <template v-if="detail.blockers?.length">
      <h2>受阻原因</h2>
      <ul><li v-for="blocker in detail.blockers" :key="blocker.id">
       <strong>{{ blocker.kind === 'human' ? '等待人工处理' : '依赖的 Issue' }}</strong> · {{ blocker.reason }}
       <Button v-if="blocker.dependencyIssueId" :label="`查看依赖的 Issue ${blocker.dependencyIssueId}`" text @click="showRecord('issues', blocker.dependencyIssueId)" />
      </li></ul>
     </template>
    </template>
    <template v-if="kind === 'timers'">
     <h2>跟进内容</h2><pre class="body-text">{{ detail.content }}</pre>
     <dl>
      <dt>定时器状态</dt><dd>{{ stateLabel(detail.status) }}</dd>
      <dt>版本</dt><dd>{{ detail.revision }}</dd>
      <dt>计划触发时间 (UTC)</dt><dd>{{ detail.dueAt }}</dd>
      <template v-if="detail.firedAt"><dt>实际触发时间 (UTC)</dt><dd>{{ detail.firedAt }}</dd></template>
      <template v-if="detail.followup">
       <dt>跟进通知投递</dt><dd>{{ followupLabel(detail.followup) }}</dd>
      </template>
     </dl>
     <Button v-if="detail.followupEventId" label="跟进事件" text @click="showRecord('events', String(detail.followupEventId))" />
    </template>
    <template v-if="(kind === 'issues' || kind === 'timers') && detail.materials?.length">
     <h2>材料</h2>
     <ul><li v-for="(material, index) in detail.materials" :key="index">
      <Button v-if="material.messageId" :label="material.label || material.messageId" text @click="showRecord('messages', material.messageId)" />
      <span v-else>{{ material.label ? material.label + ' · ' : '' }}{{ material.uri }}</span>
     </li></ul>
    </template>
    <template v-if="kind === 'issues'">
     <Button v-if="detail.currentExecutionId" label="当前执行" text @click="showRecord('executions', String(detail.currentExecutionId))" />
     <Button label="执行历史" text @click="navigate('executions', identity(detail))" />
     <Button label="报告历史" text @click="navigate('reports', identity(detail))" />
    </template>
    <dl v-if="kind === 'executions'">
     <dt>执行权</dt><dd>{{ stateLabel(detail.authority) }} · 第 {{ detail.generation }} 代</dd>
     <dt>启动时的要求版本</dt><dd>版本 {{ detail.requirementsRevision }}</dd>
     <dt>准备状态</dt><dd>{{ stateLabel(String(detail.preparationAtRetirement || detail.preparation?.state || '')) }}{{ detail.preparation?.sessionId ? ' · ' + detail.preparation.sessionId : '' }}</dd>
     <dt>启动输入</dt><dd>{{ stateLabel(detail.startup?.status) }}</dd>
     <dt v-if="detail.startup?.receipt">输入回执</dt><dd v-if="detail.startup?.receipt"><pre>{{ JSON.stringify(detail.startup.receipt, null, 2) }}</pre></dd>
     <dt v-if="detail.startup?.lastError">输入错误</dt><dd v-if="detail.startup?.lastError">{{ detail.startup.lastError }}</dd>
     <dt>报告提醒</dt><dd>{{ reminderBasis[String(detail.reminderEligibility)] || '未知' }}<span v-if="detail.reminder"> · {{ stateLabel(detail.reminder.status) }}</span></dd>
     <dt v-if="detail.workObservation">观测到的任务</dt>
     <dd v-if="detail.workObservation">{{ detail.workObservation.provider || '提供方未知' }} · {{ detail.workObservation.workId || '任务标识未确认' }} · {{ stateLabel(detail.workObservation.state) }}</dd>
     <dt v-if="detail.workObservation">观测到的 Session</dt>
     <dd v-if="detail.workObservation">{{ detail.workObservation.sessionId }} · {{ detail.workObservation.sessionUid }} · 第 {{ detail.workObservation.generation }} 代</dd>
     <dt v-if="detail.workObservation">实时观测</dt>
     <dd v-if="detail.workObservation">{{ detail.workObservation.continuous ? '连续' : '未确认' }} · {{ detail.workObservation.observedAt }}</dd>
     <dt v-if="detail.workObservation?.end">原生任务结束</dt>
     <dd v-if="detail.workObservation?.end">{{ detail.workObservation.end.reason }} · {{ detail.workObservation.end.at }} · {{ detail.workObservation.end.eventId }}</dd>
     <dt v-if="detail.workObservation?.observationGap">任务观测中断</dt>
     <dd v-if="detail.workObservation?.observationGap">{{ detail.workObservation.observationGap }}</dd>
    </dl>
    <dl v-if="detail.stop" data-testid="stop-evidence">
      <dt>停止请求</dt><dd>{{ detail.stop.requestedAt }} · {{ stateLabel(detail.stop.status) }}</dd>
      <dt>原生任务状态</dt><dd>{{ stateLabel(detail.stop.workState) }}</dd>
      <dt v-if="detail.stop.receipt">停止回执</dt><dd v-if="detail.stop.receipt">{{ stateLabel(detail.stop.receipt.result) }} {{ detail.stop.receipt.target }}</dd>
      <dt v-if="detail.stop.errorCode">停止错误</dt><dd v-if="detail.stop.errorCode">{{ detail.stop.errorCode }}</dd>
      <dt v-if="detail.stop.observation">Session 实时观测</dt><dd v-if="detail.stop.observation">{{ stateLabel(detail.stop.observation.state) }} · {{ detail.stop.observation.at }} {{ detail.stop.observation.stopReason }}</dd>
      <dt v-if="detail.stop.observationGap">观测中断</dt><dd v-if="detail.stop.observationGap">{{ detail.stop.observationGap }}</dd>
    </dl>
    <dl v-if="detail.close || detail.preparation?.close" data-testid="release-evidence">
     <dt>Session 已确认释放</dt>
     <dd>{{ (detail.close || detail.preparation?.close)?.method }} · {{ (detail.close || detail.preparation?.close)?.at }}</dd>
    </dl>
    <Button v-if="kind === 'executions' && detail.startEventId" label="启动事件" text @click="showRecord('events', String(detail.startEventId))" />
    <Button v-if="kind === 'executions' && detail.reminderEventId" label="提醒事件" text @click="showRecord('events', String(detail.reminderEventId))" />
    <Button v-if="kind === 'executions' && detail.issueId" label="此 Issue 的报告历史" text @click="navigate('reports', String(detail.issueId), 'issueId')" />
    <template v-if="kind === 'bindings'">
     <dl>
      <dt>角色</dt><dd>{{ stateLabel(detail.role) }} · 第 {{ detail.generation }} 代</dd>
      <dt>MASS Session</dt><dd>{{ detail.sessionId || '尚未准备' }}</dd>
      <dt>运行时</dt><dd>{{ detail.runtimeRef }}</dd>
      <dt>工作目录</dt><dd>{{ detail.workspace }}</dd>
     </dl>
     <SessionPreview v-if="detail.sessionId && detail.sessionUid" :key="identity(detail) + ':' + detail.sessionUid" :binding-id="identity(detail)" />
     <p v-else>核心服务记录 Session 标识后，即可查看历史。</p>
    </template>
    <template v-if="kind === 'reports'">
     <h2>{{ detail.summary }}</h2><p>{{ stateLabel(detail.kind) }} · {{ detail.createdAt }}<span v-if="detail.requirementsRevision"> · 要求版本 {{ detail.requirementsRevision }}</span><span v-if="detail.historical"> · 历史执行</span></p>
     <pre v-if="detail.details" class="body-text">{{ detail.details }}</pre>
     <h3 v-if="detail.evidence?.length">证据</h3><ul v-if="detail.evidence?.length"><li v-for="(ref, index) in detail.evidence" :key="index">{{ ref }}</li></ul>
     <h3 v-if="detail.questions?.length">待确认问题</h3><ul v-if="detail.questions?.length"><li v-for="(question, index) in detail.questions" :key="index">{{ question }}</li></ul>
     <Button label="查看执行记录" text @click="showRecord('executions', String(detail.executionId))" />
     <Button label="Topic 通知" text @click="showRecord('events', String(detail.eventId))" />
    </template>
    <template v-if="detail.completion">
     <h2>业务验收</h2>
     <p>{{ detail.completion.reason }}</p><p>{{ detail.completion.completedAt }}</p>
     <ul><li v-for="(ref, index) in detail.completion.evidence" :key="index">{{ ref }}</li></ul>
    </template>
    <pre v-if="['decisions', 'subscriptions', 'relations', 'events'].includes(kind)" data-testid="routing-record">{{ JSON.stringify(detail, null, 2) }}</pre>
    <details v-else><summary>原始字段和引用</summary><pre>{{ JSON.stringify(detail, null, 2) }}</pre></details>
   </template>
  </Drawer>
 </main>
</template>
