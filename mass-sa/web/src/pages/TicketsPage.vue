<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Drawer from 'primevue/drawer'
import InputText from 'primevue/inputtext'
import Popover from 'primevue/popover'
import Select from 'primevue/select'
import Tag from 'primevue/tag'
import Tabs from 'primevue/tabs'
import TabList from 'primevue/tablist'
import Tab from 'primevue/tab'
import TabPanels from 'primevue/tabpanels'
import TabPanel from 'primevue/tabpanel'
import TimeValue from '../components/TimeValue.vue'
import OutputsPanel from '../components/OutputsPanel.vue'
import TicketProcess from '../components/TicketProcess.vue'
import { readAPI, type Page } from '../api'
import { queryText, usePolling } from '../polling'
import type { TicketDetail, TicketRecord } from '../ticket-types'

const route = useRoute()
const router = useRouter()
const queryClient = useQueryClient()
const interval = usePolling()
const moreFilters = ref<InstanceType<typeof Popover>>()
const filterKeys = ['search', 'state', 'parent', 'root', 'limit'] as const
const form = reactive<Record<string, string>>({})
watch(() => route.query, query => {
  for (const key of filterKeys) form[key] = queryText(query[key]) || (key === 'limit' ? '50' : '')
}, { immediate: true })
const params = computed(() => Object.fromEntries(['project', 'cursor', ...filterKeys].map(key => [key, queryText(route.query[key]) || (key === 'limit' ? '50' : '')])))
const listing = useQuery({
  queryKey: computed(() => ['tickets', params.value]),
  queryFn: ({ signal }) => readAPI<Page<TicketRecord>>('tickets', params.value, signal),
  refetchOnWindowFocus: computed(() => interval.value !== false),
  refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
const selected = computed(() => queryText(route.query.selected))
const selectedProject = computed(() => queryText(route.query.selectedProject) || queryText(route.query.project))
const detail = useQuery({
  queryKey: computed(() => ['ticket', selectedProject.value, selected.value]),
  queryFn: ({ signal }) => readAPI<TicketDetail>(`tickets/${encodeURIComponent(selected.value)}`, { project: selectedProject.value }, signal),
  enabled: computed(() => !!selected.value && !!selectedProject.value),
  refetchOnWindowFocus: computed(() => interval.value !== false),
  refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
// Cached details only belong to the exact Project + Ticket requested in the URL.
const current = computed(() => {
  const value = detail.data.value
  return value?.id === selected.value && value.projectId === selectedProject.value ? value : undefined
})
const currentAssignment = computed(() => current.value?.assignments.find(a => a.current && a.id === current.value?.currentAssignmentId))
const currentSessionName = computed(() => {
  const session = currentAssignment.value?.observation.session
  return session?.sessionId === current.value?.currentSessionId ? session?.name || '查看会话' : '查看会话'
})
const tab = computed(() => ['overview', 'execution', 'outputs'].includes(queryText(route.query.detailTab)) ? queryText(route.query.detailTab) : 'overview')
const drawerVisible = computed({ get: () => !!selected.value, set: value => { if (!value) closeDrawer() } })
const businessStates = [
  { label: '全部业务状态', value: '' }, { label: '待处理', value: 'open' },
  { label: '已阻塞', value: 'blocked' }, { label: '等待用户', value: 'waiting_user' },
  { label: '已完成', value: 'completed' }, { label: '失败', value: 'failed' },
  { label: '已取消', value: 'cancelled' }, { label: '已重新打开', value: 'reopened' },
]
const pageSizes = computed(() => Array.from(new Set([form.limit || '50', '10', '50', '100', '200'])).map(value => ({ label: `${value} 条`, value })))
const extraFilters = computed(() => ['parent', 'root'].filter(key => form[key]).length + (form.limit !== '50' ? 1 : 0))
function businessLabel(value: string) { if (!value) return '历史未记录'; return businessStates.find(state => state.value === value)?.label || value || '历史未记录' }
function businessSeverity(value: string) {
  if (value === 'completed') return 'success'
  if (value === 'failed') return 'danger'
  if (value === 'blocked' || value === 'waiting_user') return 'warn'
  if (value === 'cancelled') return 'secondary'
  return 'info'
}
const newCount = ref(0)
watch(() => listing.data.value, (page, old) => {
  if (old && page && !params.value.cursor) newCount.value = page.items.filter(row => !old.items.some(before => before.id === row.id && before.projectId === row.projectId)).length
})
let openerId = ''
function rememberOpener(row: TicketRecord) { openerId = `open-${row.projectId}-${row.id}` }
function choose(row: TicketRecord) { return { query: { ...route.query, selected: row.id, selectedProject: row.projectId, ticketOutputCursor: undefined } } }
function sessionLink(id: string, project: string) { return { name: 'resources', query: { project, selected: id, resourceTab: 'sessions', scope: 'associated', refresh: route.query.refresh } } }
function closeDrawer() {
  if (selected.value) openerId ||= `open-${selectedProject.value}-${selected.value}`
  saveReading()
  void router.push({ query: { ...route.query, selected: undefined, selectedProject: undefined, ticketOutputCursor: undefined } })
}
function returnFocus() {
  const target = document.getElementById(openerId) || document.getElementById('ticket-search')
  target?.focus({ preventScroll: true })
  openerId = ''
}
function setTab(value: string | number) { void router.push({ query: { ...route.query, detailTab: String(value) } }) }
function applyFilters() {
  moreFilters.value?.hide()
  newCount.value = 0
  void router.push({ query: { ...route.query, ...Object.fromEntries(filterKeys.map(key => [key, form[key] || undefined])), cursor: undefined, selected: undefined, selectedProject: undefined, ticketOutputCursor: undefined } })
}
function clearFilters() { void router.push({ query: { project: route.query.project, refresh: route.query.refresh } }) }
async function refresh() {
  if (route.query.cursor || route.query.ticketOutputCursor) await router.replace({ query: { ...route.query, cursor: undefined, ticketOutputCursor: undefined } })
  await Promise.all([listing.refetch(), selected.value && selectedProject.value ? detail.refetch() : Promise.resolve(), queryClient.refetchQueries({ queryKey: ['outputs'], type: 'active' })])
}
function pageTo(cursor?: string) { void router.push({ query: { ...route.query, cursor, selected: undefined, selectedProject: undefined, ticketOutputCursor: undefined } }) }

// Save local reading position only. Bodies and source text stay out of URLs/storage.
const storageKey = () => `mass-observer-ticket-view:${route.fullPath}`
let readingKey = storageKey()
let restored = false
function saveReading() {
  const content = document.querySelector<HTMLElement>('.ticket-drawer .p-drawer-content')
  const focus = document.activeElement as HTMLElement | null
  const open = Array.from(document.querySelectorAll<HTMLDetailsElement>('.ticket-drawer details[data-reading][open]')).map(element => element.dataset.reading)
  try { sessionStorage.setItem(readingKey, JSON.stringify({ y: window.scrollY, drawerY: content?.scrollTop || 0, focus: focus?.id || '', open })) } catch { /* Storage may be unavailable. */ }
}
async function restoreReading() {
  if (restored || listing.isPending.value || (selected.value && selectedProject.value && detail.isPending.value)) return
  await nextTick()
  const content = document.querySelector<HTMLElement>('.ticket-drawer .p-drawer-content')
  if (selected.value && !content) return
  restored = true
  try {
    const state = JSON.parse(sessionStorage.getItem(readingKey) || 'null')
    if (state) {
      document.querySelectorAll<HTMLDetailsElement>('.ticket-drawer details[data-reading]').forEach(element => { element.open = state.open?.includes(element.dataset.reading) || false })
      if (state.focus) document.getElementById(state.focus)?.focus({ preventScroll: true })
      if (content) content.scrollTop = state.drawerY || 0
      window.scrollTo(0, state.y || 0)
    }
  } catch { /* Ignore old local reading state. */ }
}
watch([() => listing.isPending.value, () => detail.isPending.value], restoreReading, { immediate: true })
watch(() => route.fullPath, () => { saveReading(); readingKey = storageKey(); restored = false; void restoreReading() })
window.addEventListener('pagehide', saveReading)
onBeforeUnmount(() => { saveReading(); window.removeEventListener('pagehide', saveReading) })
</script>

<template>
  <div class="page-head">
    <div><h1>工作项</h1><p class="page-subtitle">查看工作目标、当前责任与派工过程。</p></div>
    <Button label="刷新查询" icon="pi pi-refresh" severity="secondary" outlined :loading="listing.isFetching.value" @click="refresh" />
  </div>
  <section class="list-workspace ticket-list" aria-label="Ticket 列表">
    <form id="ticket-filters" class="list-toolbar ticket-filters" aria-label="Ticket 筛选" @submit.prevent="applyFilters">
      <div class="field search"><label class="ticket-sr-only" for="ticket-search">搜索工作</label><InputText id="ticket-search" v-model="form.search" type="search" placeholder="搜索工作标题或目标" /></div>
      <Select v-model="form.state" :options="businessStates" option-label="label" option-value="value" aria-label="业务状态" placeholder="全部业务状态" class="ticket-state" />
      <Button type="button" :label="extraFilters ? `更多筛选 (${extraFilters})` : '更多筛选'" icon="pi pi-sliders-h" severity="secondary" outlined @click="moreFilters?.toggle($event)" />
      <Button type="submit" label="应用筛选" severity="secondary" outlined />
      <Button type="button" label="清除筛选" severity="secondary" text @click="clearFilters" />
    </form>
    <Popover ref="moreFilters">
      <form class="ticket-advanced" aria-label="工作项高级筛选" @submit.prevent="applyFilters">
        <h3>更多筛选</h3>
        <label for="ticket-parent">父工作项</label><InputText id="ticket-parent" v-model="form.parent" />
        <label for="ticket-root">根工作项</label><InputText id="ticket-root" v-model="form.root" />
        <label for="ticket-limit">每页</label><Select input-id="ticket-limit" v-model="form.limit" :options="pageSizes" option-label="label" option-value="value" aria-label="每页" />
        <Button type="submit" label="应用高级筛选" />
      </form>
    </Popover>
    <section v-if="listing.isPending.value" class="state-display" role="status"><h2>正在读取工作项</h2><p>正在查询已有工作记录。</p></section>
    <section v-if="listing.isError.value" class="state-display error-message" role="alert">
      <h2>Ticket 查询失败{{ listing.data.value ? ' · stale' : '' }}</h2><p>{{ listing.error.value?.message }}。游标过期时请刷新第一页。</p>
      <p v-if="listing.data.value">最后成功：<TimeValue :value="listing.data.value.observedAt" /></p><Button label="重试查询" severity="secondary" outlined @click="refresh" />
    </section>
    <section v-if="listing.data.value?.items.length === 0" class="state-display"><h2>当前筛选没有 Ticket</h2><p>没有匹配的工作记录。</p><Button label="清除筛选" severity="secondary" outlined @click="clearFilters" /></section>
    <template v-if="listing.data.value?.items.length">
      <div class="table-scroll" tabindex="0" aria-label="可浏览的 Ticket 数据表">
        <table class="data-table"><thead><tr><th scope="col">工作</th><th scope="col">业务状态</th><th scope="col">当前执行会话</th><th scope="col">更新时间</th></tr></thead>
          <tbody><tr v-for="row in listing.data.value.items" :key="`${row.projectId}/${row.id}`" :class="{ selected: row.id === selected && row.projectId === selectedProject }">
            <td data-label="工作"><RouterLink :id="`open-${row.projectId}-${row.id}`" class="open ticket-title" :to="choose(row)" :aria-current="row.id === selected && row.projectId === selectedProject ? 'true' : undefined" aria-haspopup="dialog" @click="rememberOpener(row)"><i class="pi pi-briefcase" aria-hidden="true"></i><span>{{ row.title || '未命名工作项' }}</span></RouterLink></td>
            <td data-label="业务状态"><Tag :value="businessLabel(row.businessState)" :severity="businessSeverity(row.businessState)" /></td>
            <td data-label="当前执行会话"><RouterLink v-if="row.currentSessionId" class="ticket-session" :to="sessionLink(row.currentSessionId, row.projectId)"><i class="pi pi-desktop" aria-hidden="true"></i>查看会话</RouterLink><span v-else class="muted">{{ row.currentAssignmentId ? '会话引用不可用' : '尚未派工' }}</span></td>
            <td data-label="更新时间"><TimeValue :value="row.updatedAt" /></td>
          </tr></tbody>
        </table>
      </div>
      <div class="pagination"><span>{{ listing.data.value.items.length }} 条工作项</span><div class="ticket-actions"><Button v-if="params.cursor" label="回到第一页" severity="secondary" outlined @click="pageTo()" /><Button v-if="listing.data.value.next" label="下一页" severity="secondary" outlined @click="pageTo(listing.data.value.next)" /></div></div>
    </template>
  </section>
  <p class="query-meta ticket-query-meta" aria-live="polite">持久快照于 <TimeValue :value="listing.data.value?.observedAt" /><span v-if="newCount"> · 新增 {{ newCount }} 条，保留当前选中身份</span></p>

  <Drawer v-model:visible="drawerVisible" position="right" class="observer-drawer ticket-drawer" style="width:min(720px, 100%)" :block-scroll="true" :close-button-props="{ 'aria-label': '关闭工作项详情' }" aria-label="工作项详情" @show="restoreReading" @after-hide="returnFocus">
    <template #header><div class="drawer-header ticket-drawer-heading"><i class="pi pi-briefcase" aria-hidden="true"></i><strong>工作项详情</strong></div></template>
    <section aria-label="Ticket 责任详情">
      <p v-if="!selectedProject" role="alert">所选 Ticket 缺少 Project 身份，请从列表重新选择。</p>
      <p v-if="detail.isPending.value && selectedProject" role="status">正在读取所选 Ticket…</p>
      <div v-if="detail.isError.value" class="error-message" role="alert"><h2>所选 Ticket 读取失败{{ current ? ' · stale' : '' }}</h2><p>{{ detail.error.value?.message }}</p><Button label="重试详情" severity="secondary" outlined @click="detail.refetch()" /></div>
      <article v-if="current" :key="`${current.projectId}/${current.id}`">
        <div class="drawer-intro ticket-intro"><div class="ticket-labels"><Tag :value="businessLabel(current.businessState)" :severity="businessSeverity(current.businessState)" /><span class="muted">更新于 <TimeValue :value="current.updatedAt" /></span></div><h2>{{ current.title || '未命名工作项' }}</h2></div>
        <Tabs class="detail-tabs" :value="tab" @update:value="setTab">
          <TabList aria-label="Ticket 详情页签"><Tab value="overview">概览</Tab><Tab value="execution">派工与委派</Tab><Tab value="outputs">业务事实</Tab></TabList>
          <TabPanels>
            <TabPanel value="overview">
              <section class="detail-section ticket-section"><h3>目标与验收</h3>
                <details data-reading="objective"><summary id="ticket-objective">展开目标（文本）</summary><pre>{{ current.objective || '历史未记录' }}</pre></details>
                <details data-reading="acceptance"><summary id="ticket-acceptance">展开验收（文本）</summary><pre>{{ current.acceptance || '历史未记录' }}</pre></details>
              </section>
              <section class="detail-section ticket-section" aria-label="当前负责会话"><h3>当前负责会话</h3>
                <div class="ticket-current-session"><RouterLink v-if="current.currentSessionId" class="touch-link" :to="sessionLink(current.currentSessionId, current.projectId)"><i class="pi pi-desktop" aria-hidden="true"></i>{{ currentSessionName }}<i class="pi pi-arrow-up-right" aria-hidden="true"></i></RouterLink><span v-else>{{ current.currentAssignmentId ? '当前派工引用不可用' : '尚未派工 · 无 Session' }}</span>
                  <dl v-if="currentAssignment" class="facts"><dt>派工状态</dt><dd>{{ currentAssignment.status || '历史未记录' }}</dd><dt>SessionState</dt><dd>{{ currentAssignment.observation.session?.state || '未观测' }}</dd><dt>Session 观察</dt><dd>{{ currentAssignment.observation.status }}{{ currentAssignment.observation.stale ? ' · stale，保留上次值' : '' }}</dd><dt>观察时间</dt><dd><TimeValue :value="currentAssignment.observation.attemptedAt" /></dd><dt v-if="currentAssignment.observation.lastSuccessAt">最后成功</dt><dd v-if="currentAssignment.observation.lastSuccessAt"><TimeValue :value="currentAssignment.observation.lastSuccessAt" /></dd></dl>
                  <p v-if="currentAssignment?.observation.error" class="error-message">{{ currentAssignment.observation.error }}</p>
                  <p v-if="currentAssignment?.reconciliationRequired" class="error-message">需要对账：{{ currentAssignment.reconciliationReason }}</p>
                </div><p class="detail-note muted">按当前派工引用展示。Session 关闭不代表业务取消。</p>
              </section>
              <section class="detail-section ticket-section"><h3>业务状态</h3><dl class="facts"><dt>当前状态</dt><dd>{{ businessLabel(current.businessState) }} <span class="muted">{{ current.businessState || '历史未记录' }}</span></dd></dl></section>
              <details class="ticket-technical" data-reading="identity"><summary id="ticket-identity">技术身份与原始字段</summary><dl class="facts"><dt>Project</dt><dd class="mono">{{ current.projectId }}</dd><dt>Ticket</dt><dd class="mono">{{ current.id }}</dd><dt>当前 Assignment</dt><dd class="mono">{{ current.currentAssignmentId || '尚未派工' }}</dd><dt>Assignment 状态</dt><dd>{{ current.currentAssignmentStatus || '历史未记录' }}</dd><dt>当前 Session</dt><dd class="mono">{{ current.currentSessionId || '未记录' }}</dd><dt>根工作项</dt><dd class="mono">{{ current.rootTicketId || '未记录' }}</dd><dt>父工作项</dt><dd class="mono">{{ current.parentTicketId || '根工作' }}</dd><dt>委派深度</dt><dd>{{ current.depth }}</dd><dt>创建时间</dt><dd><TimeValue :value="current.createdAt" /></dd><dt>持久状态读取</dt><dd><TimeValue :value="current.observedAt" /></dd><dt>来源</dt><dd>{{ current.source }}</dd></dl></details>
            </TabPanel>
            <TabPanel value="execution"><TicketProcess :current="current" /></TabPanel>
            <TabPanel value="outputs"><OutputsPanel v-if="tab === 'outputs'" :key="`${current.projectId}/${current.id}`" :project="current.projectId" :ticket="current.id" category="fact" title="业务事实" empty-text="未记录业务事实。" cursor-key="ticketOutputCursor" /></TabPanel>
          </TabPanels>
        </Tabs>
      </article>
    </section>
  </Drawer>
</template>

<style scoped>
h1, h2 { overflow-wrap: anywhere; min-width: 0; }
.ticket-list { min-width: 0; }
.ticket-filters { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ticket-filters .search { flex: 1 1 260px; }
.ticket-filters .search input { width: 100%; }
.ticket-state { min-width: 160px; }
.ticket-advanced { width: min(300px, calc(100vw - 64px)); display: grid; gap: 10px; }
.ticket-advanced h3 { margin: 0 0 4px; }
.ticket-advanced label { color: var(--color-muted); font-size: 13px; }
.ticket-sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.data-table th:first-child { width: 38%; }
.ticket-title { display: inline-flex; align-items: center; gap: 12px; font-weight: 600; color: var(--color-text, var(--color-ink)); }
.ticket-title i { color: var(--color-muted); flex-shrink: 0; }
.ticket-title span { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.ticket-session, .touch-link { display: inline-flex; align-items: center; gap: 8px; }
.ticket-actions, .ticket-labels { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ticket-query-meta { margin-top: 16px; }
.ticket-drawer-heading { display: flex; align-items: center; gap: 12px; color: var(--color-text, var(--color-ink)); }
.ticket-drawer-heading i { padding: 10px; border-radius: 8px; background: var(--color-bg, var(--color-paper)); color: var(--p-primary-color); }

.ticket-intro h2 { margin: 16px 0 0; font-size: 20px; line-height: 1.6; }
.ticket-section { padding-block: 0 24px; margin-bottom: 24px; border-bottom: 1px solid var(--color-line, var(--color-rule)); }
.ticket-section h3 { margin: 0 0 12px; }
.ticket-current-session { padding: 16px; border: 1px solid var(--color-line, var(--color-rule)); border-radius: 8px; background: var(--color-surface, var(--color-paper-2)); }
.ticket-current-session .facts { margin-top: 12px; }
.ticket-technical { margin-top: 16px; }
.facts { grid-template-columns: 120px minmax(0, 1fr); }
.detail-note { margin-top: 12px; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; }
@media (max-width: 40rem) {
  .ticket-filters .search { flex-basis: 100%; }
  .ticket-state { flex: 1 1 100%; min-width: 0; }
  .ticket-filters :deep(.p-button) { flex-shrink: 0; }
  .facts { grid-template-columns: 96px minmax(0, 1fr); }
}
</style>
