<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Drawer from 'primevue/drawer'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Tag from 'primevue/tag'
import Popover from 'primevue/popover'
import Tabs from 'primevue/tabs'
import TabList from 'primevue/tablist'
import Tab from 'primevue/tab'
import TabPanels from 'primevue/tabpanels'
import TabPanel from 'primevue/tabpanel'
import TimeValue from '../components/TimeValue.vue'
import OutputsPanel from '../components/OutputsPanel.vue'
import { readAPI, type Page } from '../api'
import { queryText, usePolling } from '../polling'
import { reasonLabels, routeLabels, type TriggerDetail, type TriggerRecord } from '../trigger-types'

const route = useRoute()
const router = useRouter()
const queryClient = useQueryClient()
const interval = usePolling()
const filterKeys = ['search', 'source', 'thread', 'decision', 'route', 'from', 'to', 'limit'] as const
const form = reactive<Record<string, string>>({})
watch(() => route.query, query => { for (const key of filterKeys) form[key] = queryText(query[key]) || (key === 'limit' ? '50' : '') }, { immediate: true })
const params = computed(() => Object.fromEntries(['project', 'cursor', ...filterKeys].map(key => [key, queryText(route.query[key]) || (key === 'limit' ? '50' : '')])))
const listing = useQuery({
  queryKey: computed(() => ['triggers', params.value]),
  queryFn: ({ signal }) => readAPI<Page<TriggerRecord>>('triggers', params.value, signal),
  refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
const selected = computed(() => queryText(route.query.selected))
const selectedProject = computed(() => queryText(route.query.selectedProject) || queryText(route.query.project))
const detail = useQuery({
  queryKey: computed(() => ['trigger', selectedProject.value, selected.value]),
  queryFn: ({ signal }) => readAPI<TriggerDetail>(`triggers/${encodeURIComponent(selected.value)}`, { project: selectedProject.value }, signal),
  enabled: computed(() => !!selected.value && !!selectedProject.value),
  refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
const current = computed(() => detail.data.value)
const tab = computed(() => queryText(route.query.detailTab) || 'source')
const filters = ref<InstanceType<typeof Popover>>()
const decisionOptions = [{ label: '全部决定', value: '' }, ...['pending', 'ignored', 'associated', 'creating', 'created', 'unknown'].map(value => ({ label: value, value }))]
const routeOptions = [{ label: '全部处理路径', value: '' }, ...Object.entries(routeLabels).map(([value, label]) => ({ value, label }))]
const limitOptions = computed(() => Array.from(new Set([form.limit || '50', '10', '50', '100', '200'])))
const decisionLabels: Record<string, string> = { pending: '待判断', ignored: '已忽略', associated: '已关联工作', creating: '正在创建工作', created: '已创建工作', unknown: '结果未知' }
function sourceName(source: string) {
  const connector = source.split('/')[0]?.toLowerCase() || ''
  return ({ dingtalk: '钉钉', slack: 'Slack', feishu: '飞书', lark: '飞书', email: '邮件', github: 'GitHub' } as Record<string, string>)[connector] || '外部事件'
}
const message = computed(() => readableContent(current.value?.content || '') || current.value?.summary || '')
function readableContent(content: string): string {
  try {
    const value = JSON.parse(content)
    const text = (v: unknown): string => {
      if (typeof v === 'string') return v
      if (!v || typeof v !== 'object') return ''
      const data = v as Record<string, unknown>
      for (const key of ['content', 'text', 'message', 'body']) {
        if (typeof data[key] === 'string') return data[key] as string
        if (data[key] && typeof data[key] === 'object' && !Array.isArray(data[key])) {
          const nested = text(data[key]); if (nested) return nested
        }
      }
      return ''
    }
    return text(value)
  } catch { return content }
}
function closeDetail() {
  void router.replace({ query: { ...route.query, selected: undefined, selectedProject: undefined, replyCursor: undefined, factCursor: undefined } })
}
function changeTab(value: string | number) { void router.replace({ query: { ...route.query, detailTab: String(value) } }) }
const newCount = ref(0)
watch(() => listing.data.value, (page, old) => {
  if (old && page && !params.value.cursor) newCount.value = page.items.filter(row => !old.items.some(before => before.triggerId === row.triggerId && before.projectId === row.projectId)).length

})
function choose(row: TriggerRecord) { return { query: { ...route.query, selected: row.triggerId, selectedProject: row.projectId, replyCursor: undefined, factCursor: undefined } } }
function triggerLink(id: string, project: string) { return { name: 'triggers', query: { ...route.query, selected: id, selectedProject: project } } }
function ticketLink(id: string, project: string) { return { name: 'tickets', query: { project, selected: id, refresh: route.query.refresh } } }
function sessionLink(id: string, project: string) { return { name: 'resources', query: { project, selected: id, resourceTab: 'sessions', scope: 'associated', refresh: route.query.refresh } } }
function applyFilters() {
  void router.push({ query: { ...route.query, ...Object.fromEntries(filterKeys.map(key => [key, form[key] || undefined])), cursor: undefined, selected: undefined, selectedProject: undefined } })
}
function clearFilters() {
  void router.push({ query: { project: route.query.project, refresh: route.query.refresh } })
}
async function refresh() {
  if (route.query.cursor || route.query.replyCursor || route.query.factCursor) await router.replace({ query: { ...route.query, cursor: undefined, replyCursor: undefined, factCursor: undefined } })
  await Promise.all([listing.refetch(), selected.value ? detail.refetch() : Promise.resolve(), queryClient.refetchQueries({ queryKey: ['outputs'], type: 'active' })])
}
function pageTo(cursor?: string) { void router.push({ query: { ...route.query, cursor, selected: undefined, selectedProject: undefined } }) }

// Save only local reading state. Body/source text never enters URLs or storage.
const storageKey = () => `mass-observer-trigger-view:${route.fullPath}`
let readingKey = storageKey()
function saveReading() {
  const focus = document.activeElement as HTMLElement | null
  const open = Array.from(document.querySelectorAll<HTMLDetailsElement>('details[data-reading][open]')).map(element => element.dataset.reading)
  sessionStorage.setItem(readingKey, JSON.stringify({ drawerY: document.querySelector('.observer-drawer .p-drawer-content')?.scrollTop || 0, y: window.scrollY, focus: focus?.id || '', open }))
}
let restored = false
async function restoreReading() {
  if (restored || listing.isPending.value || (selected.value && detail.isPending.value)) return
  restored = true
  await nextTick()
  try {
    const state = JSON.parse(sessionStorage.getItem(readingKey) || 'null')
    if (state) {
      document.querySelectorAll<HTMLDetailsElement>('details[data-reading]').forEach(element => { element.open = state.open?.includes(element.dataset.reading) || false })
      if (state.focus) document.getElementById(state.focus)?.focus({ preventScroll: true })
      window.scrollTo(0, state.y || 0)
      const drawer = document.querySelector('.observer-drawer .p-drawer-content')
      if (drawer) drawer.scrollTop = state.drawerY || 0
    }
  } catch { /* Ignore old local reading state. */ }
}
watch([() => listing.isPending.value, () => detail.isPending.value], restoreReading)
watch(() => route.fullPath, () => { saveReading(); readingKey = storageKey(); restored = false; void restoreReading() })
window.addEventListener('pagehide', saveReading)
onBeforeUnmount(() => { saveReading(); window.removeEventListener('pagehide', saveReading) })
</script>

<template>
  <div class="page-head"><div><h1>事件</h1><p class="page-subtitle">从收到的消息，追踪到处理结果。</p></div><Button icon="pi pi-refresh" label="刷新" aria-label="刷新查询" severity="secondary" outlined :disabled="listing.isFetching.value" @click="refresh" /></div>
  <div class="list-workspace">
    <form class="list-toolbar" aria-label="Trigger 筛选" @submit.prevent="applyFilters">
      <div class="field search"><InputText id="trigger-search" v-model="form.search" aria-label="搜索身份或摘要" type="search" placeholder="搜索事件内容或关联工作" /></div>
      <div class="filter-actions"><Select input-id="trigger-route" aria-label="输入路径" placeholder="全部处理路径" v-model="form.route" :options="routeOptions" option-label="label" option-value="value" @update:model-value="applyFilters" /><Button icon="pi pi-sliders-h" label="更多筛选" severity="secondary" outlined @click="filters?.toggle($event)" /><Button type="submit" label="应用筛选" severity="secondary" outlined /></div>
    </form>
    <Popover ref="filters"><form class="advanced-filters" aria-label="更多事件筛选" @submit.prevent="applyFilters(); filters?.hide()">
      <h3>筛选事件</h3>
      <div class="field"><label for="trigger-source">来源</label><InputText id="trigger-source" v-model="form.source" placeholder="connector/account" /></div>
      <div class="field"><label for="trigger-thread">Thread</label><InputText id="trigger-thread" v-model="form.thread" /></div>
      <div class="field"><label for="trigger-decision">决定</label><Select input-id="trigger-decision" aria-label="决定" v-model="form.decision" :options="decisionOptions" option-label="label" option-value="value" /></div>
      <div class="field"><label for="trigger-from">接收时间起（ISO）</label><InputText id="trigger-from" v-model="form.from" placeholder="含时区的 RFC3339" /></div>
      <div class="field"><label for="trigger-to">接收时间止（ISO）</label><InputText id="trigger-to" v-model="form.to" placeholder="含时区的 RFC3339" /></div>
      <div class="field"><label for="trigger-limit">每页</label><Select input-id="trigger-limit" aria-label="每页" v-model="form.limit" :options="limitOptions" /></div>
      <div class="head-actions"><Button type="submit" label="应用筛选" /><Button label="清除筛选" text severity="secondary" @click="clearFilters(); filters?.hide()" /></div>
    </form></Popover>
    <section v-if="listing.isPending.value" class="state-display" role="status"><h2>正在读取 Triggers</h2><p>包含未关联工作、已忽略和待判断的事件。</p></section>
    <section v-if="listing.isError.value" class="state-display error-message" role="alert"><h2>Trigger 查询失败{{ listing.data.value ? ' · stale' : '' }}</h2><p>{{ listing.error.value?.message }}。游标过期时请刷新第一页。</p><p v-if="listing.data.value">最后成功：<TimeValue :value="listing.data.value.observedAt" /></p><Button severity="secondary" outlined @click="refresh">重试查询</Button></section>
    <section v-if="listing.data.value?.items.length === 0" class="state-display"><h2>当前筛选没有 Trigger</h2><p>该筛选没有匹配的外部事件。</p><Button severity="secondary" outlined @click="clearFilters">清除筛选</Button></section>
    <section v-if="listing.data.value?.items.length" aria-label="Trigger 列表">
      <div class="table-scroll" tabindex="0" aria-label="可浏览的 Trigger 数据表"><table class="data-table"><colgroup><col style="width:40%" /><col style="width:17%" /><col style="width:21%" /><col style="width:12%" /><col style="width:10%" /></colgroup><thead><tr><th>事件</th><th><span tabindex="0" title="路由入口、业务决定与固定投递分别记录；关联工作不代表已执行。">处理路径 <i class="pi pi-info-circle" aria-hidden="true"></i></span></th><th>关联工作</th><th>回复</th><th>接收时间</th></tr></thead><tbody>
        <tr v-for="row in listing.data.value.items" :key="`${row.projectId}/${row.triggerId}`" :class="{ selected: row.triggerId === selected && row.projectId === selectedProject }">
          <td><div class="event-cell"><i :class="['source-icon pi', row.decision === 'ignored' ? 'pi-minus-circle' : 'pi-comment']" aria-hidden="true"></i><div class="event-copy"><RouterLink :id="`open-${row.projectId}-${row.triggerId}`" class="open row-title" :to="choose(row)" :aria-current="row.triggerId === selected && row.projectId === selectedProject ? 'true' : undefined">{{ row.summary }}</RouterLink><span class="meta">{{ sourceName(row.source) }} · 收到的消息</span></div></div></td>
          <td>{{ row.routes.map(value => routeLabels[value] || value).join(' → ') }}<span v-if="row.routes.length !== 1 || row.routes[0] !== row.decision" class="meta">{{ decisionLabels[row.decision] || row.decision }}</span></td>
          <td><span v-if="!row.tickets.length" class="muted">未关联工作</span><RouterLink v-for="ticket in row.tickets" :key="ticket.id" class="touch-link" :to="ticketLink(ticket.id, row.projectId)">{{ ticket.title || ticket.id }}</RouterLink></td>
          <td><Tag v-if="row.replyCount > 0" value="有回复" severity="success" /><span v-else class="muted">无回复记录</span></td><td><TimeValue :value="row.acceptedAt" compact /></td>
        </tr>
      </tbody></table></div>
      <div class="pagination"><span>{{ listing.data.value.items.length }} 条 · 当前遍历快照</span><div class="head-actions"><Button v-if="params.cursor" severity="secondary" outlined @click="pageTo()">回到第一页</Button><Button v-if="listing.data.value.next" severity="secondary" outlined @click="pageTo(listing.data.value.next)">下一页</Button></div></div>
    </section>
  </div>
  <p class="query-meta" aria-live="polite">持久状态 · 遍历快照于 <TimeValue :value="listing.data.value?.observedAt" /><span v-if="newCount"> · 新增 {{ newCount }} 条，保留当前选中身份</span></p>
  <Drawer :visible="!!selected" position="right" class="observer-drawer" :style="{ width: 'min(720px, 100%)' }" @update:visible="value => { if (!value) closeDetail() }" @after-show="restored = false; restoreReading()">
    <template #header><div class="drawer-header"><span class="drawer-icon pi pi-inbox" aria-hidden="true"></span><div><strong>事件详情</strong><small>{{ current ? sourceName(current.source) + '消息' : '来源事件' }}</small></div></div></template>
    <section class="detail" aria-label="Trigger 追踪详情">
      <p v-if="!selectedProject" class="record" role="alert">所选 Trigger 缺少 Project 身份，请从列表重新选择。</p>
      <p v-if="detail.isPending.value && selectedProject" class="record" role="status">正在读取所选 Trigger…</p>
      <div v-if="detail.isError.value" class="record error-message" role="alert"><h2>所选 Trigger 读取失败{{ current ? ' · stale' : '' }}</h2><p>{{ detail.error.value?.message }}</p><Button severity="secondary" outlined @click="detail.refetch()">重试详情</Button></div>
      <article v-if="current" :key="`${current.projectId}/${current.triggerId}`">
        <div class="drawer-intro"><Tag :value="decisionLabels[current.decision] || current.decision" severity="info" /><span class="muted"> · <TimeValue :value="current.acceptedAt" /></span><h2>{{ current.summary }}</h2><p class="muted">{{ current.projectId }} · 持久状态读取于 <TimeValue :value="current.observedAt" /></p></div>
        <Tabs class="detail-tabs" :value="tab" @update:value="changeTab"><TabList aria-label="Trigger 详情页签"><Tab value="source">概览</Tab><Tab value="trace">处理过程</Tab><Tab value="outputs">回复与送达</Tab></TabList><TabPanels>
          <TabPanel value="source"><section class="detail-section"><h3>收到的消息</h3><div class="message-surface"><p class="message-body">{{ message }}</p></div></section>
            <section class="detail-section"><h3>处理结果</h3><p>{{ decisionLabels[current.decision] || current.decision }}</p><p class="detail-note">{{ reasonLabels[current.entryReason] || current.entryReason }}</p></section>
            <section class="detail-section"><h3>关联工作</h3><p v-if="!current.tickets.length" class="muted">未关联工作</p><div v-for="ticket in current.tickets" :key="ticket.id" class="record"><RouterLink class="touch-link" :to="ticketLink(ticket.id, current.projectId)">{{ ticket.title || ticket.id }}</RouterLink><dl class="facts"><dt>当前负责会话</dt><dd><RouterLink v-if="ticket.currentSessionId" class="touch-link mono" :to="sessionLink(ticket.currentSessionId, current.projectId)">{{ ticket.currentSessionId }}</RouterLink><span v-else>尚无执行会话</span></dd><dt>当前派工</dt><dd class="mono">{{ ticket.currentAssignmentId || '无' }}</dd></dl><p class="detail-note">这里展示当前负责会话；事件当时的固定投递目标在“处理过程”中。</p></div></section>
            <details data-reading="content"><summary id="trigger-content">技术详情与原始内容（文本）</summary><dl class="facts"><dt>Project / Trigger</dt><dd class="mono">{{ current.projectId }} / {{ current.triggerId }}</dd><dt>Source</dt><dd>{{ current.source }}</dd><dt>来源事件</dt><dd class="mono">{{ current.sourceEventId }}</dd><dt>Thread</dt><dd class="mono">{{ current.threadId }}</dd><dt>replyRequested</dt><dd>{{ current.replyRequested ? '是' : '否' }}</dd><dt v-if="current.echoOfOutputId">已证实回流</dt><dd v-if="current.echoOfOutputId" class="mono">{{ current.echoOfOutputId }}</dd></dl><pre>{{ current.content }}</pre></details>
          </TabPanel>
          <TabPanel value="trace">
          <section class="detail-section" aria-label="路由入口"><h3>1 <span class="meta-divider" aria-hidden="true"></span> 路由入口</h3><p>{{ reasonLabels[current.entryReason] || current.entryReason }}</p><template v-if="current.admission"><p>当时决定：{{ current.admission.decision }} <span class="meta-divider" aria-hidden="true"></span> <TimeValue :value="current.admission.at" /></p><p>当时匹配：<span v-if="!current.admission.matchedTicketIds.length">无 Ticket</span><RouterLink v-for="id in current.admission.matchedTicketIds" :key="id" class="touch-link" :to="ticketLink(id, current.projectId)">{{ id }}</RouterLink></p><p class="muted">actor：{{ current.admission.actor.kind }}；接收回执仅证明持久接纳。</p></template><p v-else class="muted">旧记录保留现有输入与批次事实；当前 Thread 关联不用于补全过去。</p></section>
          <section class="detail-section" aria-label="业务决定"><h3>2 <span class="meta-divider" aria-hidden="true"></span> Router / operator 业务决定</h3><p v-if="!current.decisions.length">{{ current.historyRecorded ? '尚无显式业务决定记录' : '历史决定依据未记录' }} <span class="meta-divider" aria-hidden="true"></span> 当前值 {{ current.decision }}</p><ol class="trigger-decisions"><li v-for="(evidence, i) in current.decisions" :key="i"><strong>{{ evidence.action }} → {{ evidence.decision }}</strong><p><TimeValue :value="evidence.at" /> <span class="meta-divider" aria-hidden="true"></span> {{ reasonLabels[evidence.reason] || evidence.reason }}</p><p>actor：{{ evidence.actor.kind }} <RouterLink v-if="evidence.actor.sessionId" class="touch-link mono" :to="sessionLink(evidence.actor.sessionId, current.projectId)">{{ evidence.actor.sessionId }}</RouterLink></p><p v-if="evidence.actor.kind === 'router_session'" class="muted">Router Session 绑定的能力调用；不证明模型或 provider 已执行。</p><p v-for="id in evidence.matchedTicketIds" :key="id"><RouterLink class="touch-link" :to="ticketLink(id, current.projectId)">Ticket {{ id }}</RouterLink></p></li></ol><p v-if="current.errorReason" class="error-message">{{ current.errorReason }}</p></section>
          <section class="detail-section" aria-label="具体投递"><h3>3 <span class="meta-divider" aria-hidden="true"></span> 具体投递</h3><p v-if="!current.deliveries.length">未记录投递输入{{ current.decision === 'ignored' ? ' · 已忽略' : '' }}</p><article v-for="delivery in current.deliveries" :key="delivery.inboxId" class="trigger-delivery"><h4>{{ routeLabels[delivery.route] || delivery.route }} <span class="meta-divider" aria-hidden="true"></span> {{ delivery.state }}</h4><dl class="facts"><dt>Inbox</dt><dd class="mono">{{ delivery.inboxId }}</dd><dt>排队 / 更新</dt><dd><TimeValue :value="delivery.acceptedAt" /> / <TimeValue :value="delivery.updatedAt" /></dd><dt v-if="delivery.ticketId">目标 Ticket</dt><dd v-if="delivery.ticketId"><RouterLink class="touch-link" :to="ticketLink(delivery.ticketId, current.projectId)">{{ delivery.ticketId }}</RouterLink></dd><dt>执行者依据</dt><dd>{{ delivery.attempted ? '已尝试，固定历史目标' : delivery.targetResolution === 'blocked_before_attempt' ? '投递前阻塞，未尝试' : delivery.targetResolution === 'handled_without_attempt' ? '未尝试，已由决定处理' : '待解析执行者' }}</dd><template v-if="delivery.attempted"><dt>Assignment</dt><dd class="mono">{{ delivery.assignmentId || '无（Router 或历史未记录）' }}</dd><dt>目标 Session</dt><dd><RouterLink v-if="delivery.sessionId" class="touch-link mono" :to="sessionLink(delivery.sessionId, current.projectId)">{{ delivery.sessionId }}</RouterLink><span v-else>历史未记录</span></dd><dt>Execution</dt><dd class="mono">{{ delivery.executionId || '历史未记录' }}</dd></template></dl><p v-if="delivery.errorReason" class="error-message">{{ delivery.errorReason }}</p><details v-for="batch in delivery.batches" :key="batch.id" :data-reading="batch.id"><summary :id="`batch-${batch.id}`">Batch {{ batch.id }} <span class="meta-divider" aria-hidden="true"></span> {{ batch.state }}</summary><p>{{ batch.attempted ? '已尝试，固定历史目标' : '投递前阻塞，未尝试' }}</p><p>{{ batch.attempted ? '固定' : '批次记录候选' }} Assignment {{ batch.assignmentId || '无（Router）' }} <span class="meta-divider" aria-hidden="true"></span> Execution {{ batch.executionId || '未记录' }}</p><RouterLink v-if="batch.sessionId" class="touch-link mono" :to="sessionLink(batch.sessionId, current.projectId)">{{ batch.attempted ? '目标 Session' : '批次候选 Session' }} {{ batch.sessionId }}</RouterLink><p>{{ batch.attempted ? '尝试' : '批次记录' }} <TimeValue :value="batch.createdAt" /> <span class="meta-divider" aria-hidden="true"></span> 更新 <TimeValue :value="batch.updatedAt" /></p><p v-if="batch.errorReason" class="error-message">{{ batch.errorReason }}</p><ul class="batch-inputs"><li v-for="input in batch.inputs" :key="input.inboxId"><RouterLink v-if="input.triggerId" class="touch-link" :to="triggerLink(input.triggerId, current.projectId)">Trigger {{ input.triggerId }}</RouterLink><span v-else>{{ input.kind === 'child_lifecycle' ? '内部子工作通知' : input.kind }}（非外部 Trigger）</span><span class="meta mono">{{ input.inboxId }}</span></li></ul></details></article><p class="muted">queued、尝试、已接受与 unknown 分别保留；输入投递不证明业务完成。</p></section>
          </TabPanel>
          <TabPanel value="outputs"><div v-if="tab === 'outputs'" :key="`outputs-${current.projectId}/${current.triggerId}`"><OutputsPanel :project="current.projectId" :trigger="current.triggerId" category="reply" title="显式回复" empty-text="此 Trigger 未记录显式回复；普通 final、thinking 和 tool result 不作为外发回复。" cursor-key="replyCursor" /><OutputsPanel :project="current.projectId" :related-trigger="current.triggerId" category="fact" title="关联工作结果" empty-text="未见匹配关联 Ticket 及该来源 Thread 目标的业务事实。" cursor-key="factCursor" /></div></TabPanel>
        </TabPanels></Tabs>
      </article>
    </section>
    <template #footer><div class="drawer-footer"><div class="head-actions"><Button icon="pi pi-refresh" aria-label="刷新查询" severity="secondary" text :disabled="listing.isFetching.value" @click="refresh" /><Button :icon="route.query.refresh === 'paused' ? 'pi pi-play' : 'pi pi-pause'" :aria-label="route.query.refresh === 'paused' ? '继续自动刷新' : '暂停自动刷新'" severity="secondary" text @click="router.replace({ query: { ...route.query, refresh: route.query.refresh === 'paused' ? undefined : 'paused' } })" /></div><Button label="关闭详情" severity="secondary" outlined @click="closeDetail" /></div></template>
  </Drawer>
</template>
<style scoped>
.trigger-delivery { padding-block:12px; border-top:1px solid var(--line); }
h4 { margin-block:12px; }
.trigger-decisions { padding-left:24px; }
.trigger-decisions li { padding-block:12px; }
.batch-inputs { padding-left:16px; }
</style>
