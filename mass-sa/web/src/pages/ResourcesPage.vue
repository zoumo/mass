<script setup lang="ts">
import '../resources.css'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Drawer from 'primevue/drawer'
import InputText from 'primevue/inputtext'
import Popover from 'primevue/popover'
import Select from 'primevue/select'
import Tab from 'primevue/tab'
import TabList from 'primevue/tablist'
import TabPanel from 'primevue/tabpanel'
import TabPanels from 'primevue/tabpanels'
import Tabs from 'primevue/tabs'
import Tag from 'primevue/tag'
import TimeValue from '../components/TimeValue.vue'
import { readAPI, type Page } from '../api'
import type { ResourceSession, Runtime, SessionEvent } from '../resource-api'
import { queryText, usePolling } from '../polling'
import { useSessionObservation } from '../session-observation'

const route = useRoute(), router = useRouter(), interval = usePolling()
const moreFilters = ref<InstanceType<typeof Popover>>()
const tab = computed(() => queryText(route.query.resourceTab) === 'runtimes' ? 'runtimes' : 'sessions')
const scope = computed(() => queryText(route.query.scope) || 'associated')
const params = computed(() => ({ project: queryText(route.query.project), scope: scope.value, namespace: queryText(route.query.namespace), runtime: queryText(route.query.runtime), state: queryText(route.query.state), type: queryText(route.query.type), cursor: queryText(route.query.cursor) }))
const listing = useQuery({ queryKey: computed(() => ['resources', tab.value, params.value]), queryFn: ({ signal }) => readAPI<Page<ResourceSession | Runtime>>(tab.value, params.value, signal), refetchInterval: interval, refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false) })
const selected = computed(() => queryText(route.query.selected))
const context = computed(() => ({ project: params.value.project, scope: scope.value }))
const detail = useQuery({ queryKey: computed(() => ['resource', tab.value, selected.value, context.value]), queryFn: ({ signal }) => readAPI<ResourceSession | Runtime>(`${tab.value}/${encodeURIComponent(selected.value)}`, context.value, signal), enabled: computed(() => !!selected.value), refetchInterval: interval, refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false) })
const session = computed(() => tab.value === 'sessions' ? detail.data.value as ResourceSession | undefined : undefined)
const runtime = computed(() => tab.value === 'runtimes' ? detail.data.value as Runtime | undefined : undefined)
const observationId = computed(() => tab.value === 'sessions' ? selected.value : '')
const eventParams = computed(() => ({ ...context.value, eventTypes: queryText(route.query.eventTypes), limit: '50' }))
const live = useSessionObservation(observationId, eventParams, () => ({ cursor: queryText(route.query.historyCursor), page: queryText(route.query.historyWindow) === 'page' }))
const detailTab = computed(() => {
  const value = queryText(route.query.detailTab)
  return value === 'config' || (tab.value === 'sessions' && value === 'history') ? value : 'overview'
})
const visible = computed({ get: () => !!selected.value, set: value => { if (!value) void closeDrawer() } })
const extraFilters = computed(() => [params.value.namespace, params.value.runtime, params.value.type].filter(Boolean).length)
const scopeOptions = [{ label: 'mass-sa 关联资源', value: 'associated' }, { label: '全部 massd 资源', value: 'all' }]
const stateOptions = computed(() => {
  const values = tab.value === 'sessions' ? ['idle', 'running', 'requires_action', 'offline', 'opening', 'closing', 'unknown'] : ['running', 'stopped', 'starting', 'stopping', 'unknown']
  if (params.value.state && !values.includes(params.value.state)) values.push(params.value.state)
  return [{ label: '全部状态', value: '' }, ...values.map(value => ({ label: stateLabel(value), value }))]
})
const eventOptions = [{ label: '全部持久类型', value: '' }, { label: '输入', value: 'user.message' }, { label: '消息与思考', value: 'agent.message,agent.thinking' }, { label: '工具', value: 'agent.tool_call,agent.tool_result' }]
const title = computed(() => detail.data.value?.name || selected.value)
watch(() => route.query.follow, value => { live.paused.value = value === 'paused' }, { immediate: true })
function toggleFollow() { void router.replace({ query: { ...route.query, follow: live.paused.value ? undefined : 'paused' } }) }
async function loadEarlier() {
  const cursor = await live.loadEarlier()
  if (cursor !== undefined) await router.replace({ query: { ...route.query, historyCursor: cursor || undefined, historyWindow: 'page' } })
}
function identity(row: ResourceSession | Runtime) { return 'sessionId' in row ? row.sessionId : row.name }
function stateLabel(value: string) { return ({ idle: '空闲', running: '运行中', requires_action: '需要处理', stopped: '已停止', starting: '启动中', stopping: '停止中', offline: '离线', opening: '连接中', closing: '关闭中', error: '错误', unknown: '未知' } as Record<string, string>)[value] || value }
function severity(value: string) { return value === 'running' ? 'success' : value === 'error' ? 'danger' : value === 'unknown' ? 'warn' : 'secondary' }
function associationSummary(row: ResourceSession | Runtime) {
  if (!('associations' in row)) return '持久 Runtime 配置'
  if (!row.associations.length) return '未关联工作'
  const current = row.associations.filter(a => a.current)
  const references = current.length ? current : row.associations
  const tickets = new Set(references.flatMap(a => a.ticketId ? [a.ticketId] : []))
  const routers = references.some(a => a.role === 'router')
  const summary = [tickets.size ? `${tickets.size} 项工作` : '', routers ? '路由器' : ''].filter(Boolean).join(' · ') || `${references.length} 项引用`
  return `${current.length ? '当前' : '历史'} · ${summary}`
}
function eventLabel(event: SessionEvent) {
  return ({ 'user.message': '用户输入', 'agent.message': 'Agent 消息', 'agent.message_chunk': '消息片段', 'agent.thinking': '思考', 'agent.thinking_chunk': '思考片段', 'agent.tool_call': '工具调用', 'agent.tool_result': '工具结果', 'session.error': '会话错误', 'session.state_update': '状态变化' } as Record<string, string>)[event.type] || event.type
}
function eventSummary(event: SessionEvent) {
  return event.text.trim().replace(/\s+/g, ' ').slice(0, 160) || (typeof event.payload.toolName === 'string' ? event.payload.toolName : eventLabel(event))
}
function set(key: string, value: string) {
  const resourceReset = key === 'resourceTab'
    ? { selected: undefined, detailTab: undefined, state: undefined, runtime: undefined, type: undefined, eventTypes: undefined, follow: undefined }
    : {}
  void router.push({ query: { ...route.query, [key]: value || undefined, cursor: undefined, historyCursor: undefined, historyWindow: undefined, ...resourceReset } })
}
function next() { void router.push({ query: { ...route.query, cursor: listing.data.value?.next } }) }
function choose(id: string) { return { query: { ...route.query, selected: id, detailTab: 'overview', historyCursor: undefined, historyWindow: undefined } } }
function setDetailTab(value: string | number) { void router.replace({ query: { ...route.query, detailTab: String(value) } }) }
let opener = ''
function rememberOpener(id: string) { opener = `open-resource-${id}` }
async function closeDrawer() {
  await router.replace({ query: { ...route.query, selected: undefined, detailTab: undefined, historyCursor: undefined, historyWindow: undefined } })
}
async function returnFocus() {
  await nextTick()
  const target = document.getElementById(opener)
  ;(target || document.getElementById('resource-list-heading'))?.focus({ preventScroll: true })
}
async function refresh() { if (route.query.cursor) await router.replace({ query: { ...route.query, cursor: undefined } }); await Promise.all([listing.refetch(), selected.value ? detail.refetch() : Promise.resolve()]) }

// Store identities and reading position only, without event text or payload.
const storageKey = () => `mass-observer-resource-view:${route.fullPath}`
let readingKey = storageKey(), restored = false
function saveReading() {
  const focus = document.activeElement as HTMLElement | null
  const drawer = document.querySelector<HTMLElement>('.resource-drawer .p-drawer-content')
  const open = Array.from(document.querySelectorAll<HTMLDetailsElement>('.resource-drawer details[data-reading][open]')).map(element => element.dataset.reading)
  sessionStorage.setItem(readingKey, JSON.stringify({ y: window.scrollY, drawerY: drawer?.scrollTop || 0, focus: focus?.id || '', open }))
}
async function restoreReading() {
  if (restored || listing.isPending.value || (selected.value && detail.isPending.value) || (observationId.value && !live.lastSuccess.value)) return
  await nextTick()
  try {
    const state = JSON.parse(sessionStorage.getItem(readingKey) || 'null')
    if (state) {
      const disclosures = document.querySelectorAll<HTMLDetailsElement>('.resource-drawer details[data-reading]')
      disclosures.forEach(element => { element.open = state.open?.includes(element.dataset.reading) || false })
      if (state.open?.some((id: string) => !Array.from(disclosures).some(element => element.dataset.reading === id))) return
      if (state.focus) document.getElementById(state.focus)?.focus({ preventScroll: true })
      const drawer = document.querySelector<HTMLElement>('.resource-drawer .p-drawer-content')
      if (drawer) drawer.scrollTop = state.drawerY || 0
      window.scrollTo(0, state.y || 0)
    }
    restored = true
  } catch { restored = true }
}
watch([() => listing.isPending.value, () => detail.isPending.value, () => live.events.value], restoreReading, { immediate: true })
watch(() => route.fullPath, () => { saveReading(); readingKey = storageKey(); restored = false; void restoreReading() })
window.addEventListener('pagehide', saveReading)
onBeforeUnmount(() => { saveReading(); window.removeEventListener('pagehide', saveReading) })
</script>

<template>
  <div class="resource-page">
    <div class="page-head"><div><h1 id="resource-list-heading" tabindex="-1">执行会话</h1><p class="page-subtitle">查看 Agent 会话、关联工作与执行过程。</p></div><Button label="刷新查询" icon="pi pi-refresh" severity="secondary" outlined :loading="listing.isFetching.value" @click="refresh" /></div>
    <section class="list-workspace resource-workspace" aria-label="资源列表">
      <Tabs :value="tab" @update:value="value => set('resourceTab', String(value))"><TabList aria-label="资源类型"><Tab value="sessions">执行会话</Tab><Tab value="runtimes">Runtime</Tab></TabList></Tabs>
      <div class="list-toolbar resource-toolbar" aria-label="资源筛选">
        <Select :model-value="scope" :options="scopeOptions" option-label="label" option-value="value" aria-label="资源范围" @update:model-value="value => set('scope', value)" />
        <div class="resource-filter-actions"><Select :model-value="params.state" placeholder="全部状态" :options="stateOptions" option-label="label" option-value="value" aria-label="状态筛选" @update:model-value="value => set('state', value)" /><Button :label="extraFilters ? `更多筛选 (${extraFilters})` : '更多筛选'" icon="pi pi-sliders-h" severity="secondary" outlined @click="moreFilters?.toggle($event)" /></div>
      </div>
      <div v-if="listing.isPending.value" class="state-display" role="status">正在读取资源…</div>
      <div v-if="listing.isError.value" class="state-display error-message" role="alert"><h2>资源读取失败{{ listing.data.value ? ' · stale' : '' }}</h2><p>{{ listing.error.value?.message }}。保留上次成功数据；断线不表示没有资源。</p><Button label="重试查询" severity="secondary" outlined @click="refresh" /></div>
      <div v-if="listing.data.value?.items.length === 0" class="state-display"><h2>本页没有匹配资源</h2><p>可调整筛选，或继续浏览下一页。</p></div>
      <template v-if="listing.data.value">
        <div class="table-scroll" tabindex="0"><table class="data-table resource-table"><thead><tr><th scope="col">{{ tab === 'sessions' ? '会话' : 'Runtime' }}</th><th scope="col">{{ tab === 'sessions' ? 'Runtime' : '类型' }}</th><th scope="col"><span class="resource-column-heading">状态<i title="massd 观测状态，不代表工作项的业务结果。" class="pi pi-info-circle" tabindex="0" aria-label="状态含义"></i></span></th><th scope="col">{{ tab === 'sessions' ? '关联工作' : '配置来源' }}</th></tr></thead><tbody>
          <tr v-for="row in listing.data.value.items" :key="identity(row)" :class="{ selected: selected === identity(row) }">
            <td><div class="resource-name-cell"><span class="resource-icon"><i :class="'sessionId' in row ? 'pi pi-desktop' : 'pi pi-server'"></i></span><RouterLink :id="`open-resource-${identity(row)}`" class="resource-name" :to="choose(identity(row))" @click="rememberOpener(identity(row))">{{ row.name || identity(row) }}</RouterLink></div></td>
            <td>{{ 'runtimeRef' in row ? row.runtimeRef : row.type }}</td><td><Tag :value="stateLabel(row.state)" :severity="severity(row.state)" /></td>
            <td><span :class="{ muted: 'associations' in row && !row.associations.length }">{{ 'associations' in row ? associationSummary(row) : row.configSource }}</span></td>
          </tr>
        </tbody></table></div>
        <div class="pagination"><span>{{ listing.data.value.items.length }} 条本页记录</span><div class="head-actions"><Button v-if="params.cursor" label="第一页" severity="secondary" outlined @click="set('cursor', '')" /><Button v-if="listing.data.value.next" label="下一页" icon="pi pi-angle-right" icon-pos="right" severity="secondary" outlined @click="next" /></div></div>
      </template>
    </section>
    <p class="resource-query-meta muted">最后成功 <TimeValue :value="listing.data.value?.observedAt" /><i title="会话使用 MASS 原生游标；Runtime 使用 Observer 快照分页。关联仅按持久 Session ID，Namespace 只用于筛选。查看和关闭抽屉只影响观察连接。" class="pi pi-info-circle" tabindex="0" aria-label="资源查询说明"></i></p>
  </div>

  <Popover ref="moreFilters"><div class="resource-advanced-filters"><h3>更多筛选</h3><label>Namespace<InputText aria-label="Namespace" :model-value="params.namespace" @change="set('namespace', ($event.target as HTMLInputElement).value)" /></label><label v-if="tab === 'sessions'">Runtime<InputText aria-label="Runtime 筛选" :model-value="params.runtime" @change="set('runtime', ($event.target as HTMLInputElement).value)" /></label><label v-else>Runtime type<InputText aria-label="Runtime type" :model-value="params.type" @change="set('type', ($event.target as HTMLInputElement).value)" /></label><p class="muted">输入后按 Enter 或移开焦点应用筛选。</p></div></Popover>

  <Drawer v-model:visible="visible" position="right" class="observer-drawer resource-drawer" style="width: min(720px, 100%)" :block-scroll="true" :close-button-props="{ 'aria-label': '关闭资源详情' }" @after-hide="returnFocus" @after-show="restoreReading">
    <template #header><div class="resource-drawer-header"><span class="resource-icon"><i :class="tab === 'sessions' ? 'pi pi-desktop' : 'pi pi-server'"></i></span><div><strong>{{ tab === 'sessions' ? '执行会话详情' : 'Runtime 详情' }}</strong><span class="muted">只读观察</span></div></div></template>
    <div class="resource-intro"><Tag v-if="detail.data.value" :value="stateLabel(detail.data.value.state)" :severity="severity(detail.data.value.state)" /><h2 class="resource-heading">{{ title }}</h2><p v-if="session" class="muted">{{ session.runtimeRef }} · {{ associationSummary(session) }}</p><p v-else-if="runtime" class="muted">{{ runtime.type }}</p></div>
    <p v-if="detail.isPending.value && selected" class="resource-detail-loading" role="status">正在读取所选资源…</p>
    <div v-if="detail.isError.value && selected" class="state-display error-message" role="alert"><h2>所选资源 unavailable{{ detail.data.value ? ' · stale' : '' }}</h2><p>{{ detail.error.value?.message }}；保留原身份。</p></div>
    <Tabs :value="detailTab" class="resource-detail-tabs" @update:value="setDetailTab">
      <TabList><Tab value="overview">概览</Tab><Tab v-if="tab === 'sessions'" value="history">历史与实时</Tab><Tab value="config">配置与身份</Tab></TabList>
      <TabPanels>
        <TabPanel value="overview">
          <section v-if="session" class="detail-section"><h3>会话概况</h3><dl class="facts"><dt>Runtime</dt><dd>{{ session.runtimeRef }}</dd><dt>观测状态</dt><dd>{{ stateLabel(session.state) }}</dd><dt>关联工作</dt><dd>{{ associationSummary(session) }}</dd></dl></section>
          <section v-if="session" class="detail-section"><h3>关联工作与路由器<i title="反向关联来自持久 Session ID。当前与历史引用分别保留，不按 Namespace 推断归属。" class="pi pi-info-circle" tabindex="0" aria-label="持久关联说明"></i></h3><div v-for="a in session.associations" :key="a.projectId + a.role + a.assignmentId" class="record resource-association"><div class="resource-association-head"><Tag :value="a.current ? '当前引用' : '历史引用'" :severity="a.current ? 'info' : 'secondary'" /><span class="muted">{{ a.projectId }}</span></div><RouterLink v-if="a.ticketId" class="touch-link" :to="{ name: 'tickets', query: { project: a.projectId, selected: a.ticketId, refresh: route.query.refresh } }"><i class="pi pi-briefcase" aria-hidden="true"></i>{{ a.ticketId }}</RouterLink><RouterLink v-if="a.role === 'router'" class="touch-link" :to="{ name: 'routers', query: { selected: a.projectId, project: a.projectId, refresh: route.query.refresh } }"><i class="pi pi-sitemap" aria-hidden="true"></i>查看路由器</RouterLink><span v-if="!a.ticketId && a.role !== 'router'" class="muted">{{ a.role }}</span></div><p v-if="!session.associations.length" class="detail-note">未见持久身份关联。</p></section>
          <section v-if="runtime" class="detail-section"><h3>Runtime 概况</h3><dl class="facts"><dt>类型</dt><dd>{{ runtime.type }}</dd><dt>观测状态</dt><dd>{{ stateLabel(runtime.state) }}</dd><dt>配置来源</dt><dd>{{ runtime.configSource }}</dd></dl></section>
        </TabPanel>
        <TabPanel v-if="tab === 'sessions'" value="history">
          <section class="detail-section resource-history" aria-label="历史与实时观察">
            <div class="resource-history-toolbar"><Select :model-value="queryText(route.query.eventTypes)" placeholder="全部持久类型" :options="eventOptions" option-label="label" option-value="value" aria-label="持久事件类型" @update:model-value="value => set('eventTypes', value)" /><Button :label="live.paused.value ? '继续跟随' : '暂停跟随'" :icon="live.paused.value ? 'pi pi-play' : 'pi pi-pause'" severity="secondary" outlined @click="toggleFollow" /></div>
            <p class="resource-live-state" role="status" aria-live="polite"><span>{{ live.state.value }}</span><span v-if="live.paused.value" class="muted">已暂停显示更新，阅读位置保持</span><Tag v-if="live.pending.value" value="有新增观察" severity="info" /><i title="完整事实按持久追加顺序显示；实时预览独立标注。错误和状态更新仅来自实时观察。仅持久历史可补读，实时预览可能缺失。" class="pi pi-info-circle" tabindex="0" aria-label="历史与实时说明"></i></p>
            <p v-if="live.disconnectedAt.value" class="detail-note">最近断开时间：<TimeValue :value="live.disconnectedAt.value" /></p><p class="detail-note">历史最后成功：<TimeValue :value="live.lastSuccess.value" /></p>
            <p v-if="live.error.value" class="error-message" role="alert">{{ live.error.value }}</p><p v-if="live.hasTrimmed.value" class="detail-note">显示缓存已达上限（500 条持久事实、200 条预览）；更早历史仍可按原生游标重新查询。</p>
            <Button v-if="live.earlier.value.length" class="resource-earlier" label="加载更早记录" icon="pi pi-history" severity="secondary" outlined :loading="live.loadingEarlier.value" :disabled="live.loadingEarlier.value" @click="loadEarlier" />
            <p v-if="!live.events.value.length && live.lastSuccess.value" class="detail-note">没有匹配的持久历史；继续观察新增事件。</p>
            <ol class="session-events"><li v-for="(event, i) in live.events.value" :key="event.eventId + event.type + event.preview"><details :data-reading="`${event.eventId}/${event.type}/${event.preview}`"><summary :id="`event-${event.eventId}-${event.type}-${event.preview}`"><span class="resource-event-meta"><Tag :value="event.preview ? '实时预览，未确认持久' : '持久事实'" :severity="event.preview ? 'warn' : 'secondary'" /><span>{{ eventLabel(event) }}</span><TimeValue :value="event.time" /></span><span class="resource-event-summary">{{ eventSummary(event) }}</span></summary><div class="resource-event-content"><pre class="resource-event-text">{{ event.text || '无文本内容' }}</pre><details class="resource-raw-event" :data-reading="`raw/${event.eventId}/${event.type}/${event.preview}`"><summary :id="`raw-${event.eventId}-${event.type}-${event.preview}`">事件身份与原始数据</summary><dl class="facts"><dt>追加位置</dt><dd>{{ i + 1 }}</dd><dt>eventId</dt><dd class="mono">{{ event.eventId }}</dd><dt>itemId</dt><dd class="mono">{{ event.itemId || '未提供' }}</dd><dt>事件类型</dt><dd class="mono">{{ event.type }}</dd></dl><pre>{{ JSON.stringify(event.payload, null, 2) }}</pre></details></div></details></li></ol>
          </section>
        </TabPanel>
        <TabPanel value="config">
          <section v-if="session" class="detail-section"><h3>持久初始配置</h3><dl class="facts"><dt>massd 初始配置</dt><dd>{{ session.initialConfig?.model || '默认' }} / {{ session.initialConfig?.effort || '默认' }}</dd><dt>provider 实际模型</dt><dd>{{ session.providerModel || session.providerModelEvidence || '未观测' }}</dd></dl></section>
          <section v-if="session?.associations.length" class="detail-section"><h3>派工请求与身份</h3><div v-for="a in session.associations" :key="a.projectId + a.role + a.assignmentId" class="record"><dl class="facts"><dt>Project / 角色</dt><dd>{{ a.projectId }} / {{ a.role }} · {{ a.current ? '当前引用' : '历史引用' }}</dd><dt>Assignment</dt><dd class="mono">{{ a.assignmentId || '无' }}</dd><dt>请求模型</dt><dd>{{ a.requested?.model || '未记录' }} / {{ a.requested?.effort || '默认' }}</dd></dl></div></section>
          <section v-if="runtime" class="detail-section"><h3>持久 Runtime 配置</h3><dl class="facts"><dt>模型 / effort</dt><dd>{{ runtime.config.model || '未提供模型' }} / {{ runtime.config.effort || '未提供 effort' }}</dd><dt>来源</dt><dd>{{ runtime.configSource }}</dd></dl></section>
          <section v-if="detail.data.value" class="detail-section"><h3>资源身份</h3><dl class="facts"><dt>{{ session ? 'Session ID' : 'Runtime name' }}</dt><dd class="mono">{{ selected }}</dd><dt>Namespace</dt><dd class="mono">{{ detail.data.value.namespace || '全实例' }}</dd></dl></section>
        </TabPanel>
      </TabPanels>
    </Tabs>
    <template #footer><div class="resource-drawer-footer"><span class="muted"><i class="pi pi-eye"></i>只读观察</span><Button label="关闭详情" severity="secondary" outlined @click="closeDrawer" /></div></template>
  </Drawer>
</template>
