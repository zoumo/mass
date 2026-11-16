<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Drawer from 'primevue/drawer'
import Tag from 'primevue/tag'
import Tabs from 'primevue/tabs'
import TabList from 'primevue/tablist'
import Tab from 'primevue/tab'
import TabPanels from 'primevue/tabpanels'
import TabPanel from 'primevue/tabpanel'
import TimeValue from '../components/TimeValue.vue'
import { readAPI, type Page, type RouterRecord } from '../api'
import { queryText, usePolling } from '../polling'

const route = useRoute()
const router = useRouter()
const interval = usePolling()
const params = computed(() => ({ project: queryText(route.query.project), cursor: queryText(route.query.cursor) }))
const listing = useQuery({
  queryKey: computed(() => ['routers', params.value]),
  queryFn: ({ signal }) => readAPI<Page<RouterRecord>>('routers', params.value, signal),
  refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
const selected = computed(() => queryText(route.query.selected))
const detail = useQuery({
  queryKey: computed(() => ['router', selected.value]),
  queryFn: ({ signal }) => readAPI<RouterRecord>(`routers/${encodeURIComponent(selected.value)}`, {}, signal),
  enabled: computed(() => !!selected.value), refetchOnWindowFocus: computed(() => interval.value !== false), refetchOnReconnect: computed(() => interval.value !== false), refetchInterval: interval,
})
const current = computed(() => detail.data.value)
function label(row: RouterRecord) {
  if (row.observation.status === 'not_found' && !row.persisted) return '尚未建立 Session'
  if (row.observation.stale) return 'stale · 上次观察'
  return row.observation.session?.state ?? (row.observation.status === 'error' ? '观察错误' : 'Session 不可用')
}
const tab = computed(() => queryText(route.query.detailTab) || 'overview')
function changeTab(value: string | number) { void router.replace({ query: { ...route.query, detailTab: String(value) } }) }
function closeDetail() { void router.replace({ query: { ...route.query, selected: undefined, selectedProject: undefined } }) }
function choose(id: string) { return { query: { ...route.query, selected: id } } }
async function refresh() {
  if (route.query.cursor) await router.replace({ query: { ...route.query, cursor: undefined } })
  await Promise.all([listing.refetch(), selected.value ? detail.refetch() : Promise.resolve()])
}
function nextPage() { router.push({ query: { ...route.query, cursor: listing.data.value?.next } }) }
function firstPage() { router.push({ query: { ...route.query, cursor: undefined } }) }
</script>

<template>
  <div class="page-head"><div><h1>路由器</h1><p class="page-subtitle">查看项目的执行配置与当前观察。</p></div>
    <Button severity="secondary" outlined :disabled="listing.isFetching.value" :aria-busy="listing.isFetching.value" icon="pi pi-refresh" aria-label="刷新查询" @click="refresh">刷新</Button>
  </div>
  <p class="query-meta" aria-live="polite">配置来源：当前进程已加载配置 · 查询于 <TimeValue :value="listing.data.value?.observedAt" /></p>
  <div v-if="listing.isPending.value" class="state-display" role="status"><h2>正在读取 Routers</h2><p>正在核对已加载配置与 massd Session。</p><div class="skeleton-row"></div><div class="skeleton-row"></div></div>
  <div v-if="listing.isError.value" class="state-display error-message" role="alert"><h2>Router 查询失败{{ listing.data.value ? ' · stale' : '' }}</h2><p>{{ listing.error.value?.message }}。{{ listing.data.value ? '下方保留上次成功查询。' : '当前没有可展示的查询结果。' }}</p><p v-if="listing.data.value">最后成功：<TimeValue :value="listing.data.value.observedAt" /></p><Button severity="secondary" outlined @click="refresh">重试查询</Button></div>
  <section v-if="listing.data.value?.items.length === 0" class="state-display"><h2>当前筛选没有 Router</h2><p>已配置但尚无 Session 的 Project 仍会列出；当前结果只反映此筛选。</p><RouterLink class="touch-link" :to="{ query: { ...route.query, project: undefined, cursor: undefined } }">清除 Project 筛选</RouterLink></section>
  <section v-if="listing.data.value?.items.length" class="list-workspace" aria-label="Router 列表">
    <div class="table-scroll" tabindex="0" aria-label="可浏览的 Router 数据表"><table class="data-table"><colgroup><col style="width:35%" /><col style="width:35%" /><col style="width:30%" /></colgroup><thead><tr><th scope="col">Router / Project</th><th scope="col">Runtime / 请求配置</th><th scope="col">massd 当前观察</th></tr></thead><tbody>
      <tr v-for="row in listing.data.value.items" :key="row.project.id" :class="{ selected: row.project.id === selected }">
        <td data-label="Router / Project"><RouterLink class="open" :to="choose(row.project.id)" :aria-current="row.project.id === selected ? 'true' : 'false'">{{ row.project.name }} Router</RouterLink><span class="meta">{{ row.project.name }}</span></td>
        <td data-label="Runtime / 请求配置"><span class="mono">{{ row.runtimeRef }}</span><span class="meta">请求 {{ row.requested.model || 'Runtime 默认' }} / {{ row.requested.effort || '默认' }}</span></td>
        <td data-label="massd 当前观察"><Tag :value="label(row)" :severity="row.observation.status === 'observed' ? 'success' : 'warn'" /><span class="meta"><TimeValue :value="row.observation.lastSuccessAt" /></span></td>
      </tr>
    </tbody></table></div>
    <div class="pagination"><span>{{ listing.data.value.items.length }} 个配置 · 按 Project 身份排序</span><div class="head-actions"><Button v-if="params.cursor" severity="secondary" outlined @click="firstPage">回到第一页</Button><Button v-if="listing.data.value.next" severity="secondary" outlined @click="nextPage">下一页</Button></div></div>
  </section>
  <Drawer :visible="!!selected" position="right" class="observer-drawer" :style="{ width: 'min(720px, 100%)' }" @update:visible="value => { if (!value) closeDetail() }">
    <template #header><div class="drawer-header"><span class="drawer-icon pi pi-sitemap" aria-hidden="true"></span><div><strong>路由器详情</strong><small>{{ current?.project.name || selected }}</small></div></div></template>
    <section class="detail" aria-label="Router 配置">
  <div v-if="detail.isPending.value && selected" role="status" class="state-display">正在读取所选 Router…</div>
  <div v-if="detail.isError.value" class="state-display error-message" role="alert"><h2>所选 Router 读取失败{{ current ? ' · stale' : '' }}</h2><p>{{ detail.error.value?.message }}</p><Button severity="secondary" outlined @click="detail.refetch()">重试详情</Button></div>
      <article v-if="current" :key="current.project.id"><div class="drawer-intro"><Tag :value="label(current)" :severity="current.observation.status === 'observed' ? 'success' : 'warn'" /><h2>{{ current.project.name }} Router</h2><p class="muted">{{ current.runtimeRef }} · {{ current.requested.model || 'Runtime 默认' }}</p></div>
        <Tabs class="detail-tabs" :value="tab" @update:value="changeTab"><TabList aria-label="Router 详情页签"><Tab value="overview">概览</Tab><Tab value="config">处理配置</Tab></TabList><TabPanels><TabPanel value="overview">
<div><p class="muted">已加载请求配置；massd 的 initialConfig 是 Session 初始配置，不证明提供方实际选用了同名模型。</p>
      <dl class="facts record"><dt>Project</dt><dd class="mono">{{ current.project.id }}</dd><dt>Runtime</dt><dd class="mono">{{ current.runtimeRef }}</dd><dt>请求模型</dt><dd>{{ current.requested.model || 'Runtime 默认' }}</dd><dt>思考强度</dt><dd>{{ current.requested.effort || '默认' }}</dd><dt>工作目录</dt><dd class="mono">{{ current.cwd }}</dd></dl>
      <details><summary>Router 技术身份</summary><dl class="facts"><dt>名称</dt><dd class="mono">{{ current.name }}</dd><dt>Namespace</dt><dd class="mono">{{ current.namespace }}</dd><dt>配置来源</dt><dd>{{ current.configSource }}</dd></dl></details><h3>持久引用</h3><dl v-if="current.persisted" class="facts record"><dt>Session</dt><dd class="mono"><RouterLink class="touch-link" :to="{ name: 'resources', query: { project: current.project.id, selected: current.persisted.sessionId, resourceTab: 'sessions', scope: 'associated', refresh: route.query.refresh } }">{{ current.persisted.sessionId }}</RouterLink></dd><dt>持久状态</dt><dd>{{ current.persisted.observedState }}（记录值）</dd><dt>记录时间</dt><dd><TimeValue :value="current.persisted.updatedAt" /></dd></dl><p v-else class="record muted">尚无 mass-sa 持久 Session 引用。</p>
      <h3>massd 只读观察</h3><div class="record"><p class="badge" :class="{ warn: current.observation.status !== 'observed' }">{{ label(current) }}</p><p v-if="current.observation.error" class="error-message">{{ current.observation.error }}</p>
        <dl class="facts"><dt>尝试时间</dt><dd><TimeValue :value="current.observation.attemptedAt" /></dd><dt>最后成功</dt><dd><TimeValue :value="current.observation.lastSuccessAt" /></dd>
          <template v-if="current.observation.session"><dt>观察 Session</dt><dd class="mono">{{ current.observation.session.sessionId }}</dd><dt>观察 Runtime</dt><dd class="mono">{{ current.observation.session.runtimeRef }}</dd><dt>观察状态</dt><dd>{{ current.observation.session.state }}</dd><dt>提供方实际模型</dt><dd>未观测</dd><dt>初始配置</dt><dd>{{ current.observation.session.initialConfig?.model || '默认' }} / {{ current.observation.session.initialConfig?.effort || '默认' }}</dd></template>
        </dl>
      </div>
    </div>
        </TabPanel><TabPanel value="config">
    <div><h3>处理配置</h3><dl class="facts"><dt>合并窗口</dt><dd>{{ current.inbox.mergeWindowMs }} ms</dd><dt>批次上限</dt><dd>{{ current.inbox.maxBatchSize }} 个事件</dd><dt>输入上限</dt><dd>{{ current.inbox.maxPromptBytes }} bytes</dd><dt>Worker 默认</dt><dd>{{ current.worker.model || 'Runtime 默认' }} / {{ current.worker.effort || '默认' }}</dd><dt>允许 Runtime</dt><dd class="mono">{{ current.worker.runtimeRefs.join(', ') || '沿用 Project Runtime' }}</dd></dl>
      <details><summary>Router 任务指引</summary><pre>{{ current.instructions }}</pre></details>
      <details><summary>可见范围</summary><p>仅展示允许字段；认证配置、能力文件和 MCP 参数不进入页面。正文按文本显示，常见秘密形式会遮盖；任意文本仍须由操作员判断。</p></details>
    </div>
        </TabPanel></TabPanels></Tabs>
      </article>
    </section>
    <template #footer><div class="drawer-footer"><div class="head-actions"><Button icon="pi pi-refresh" aria-label="刷新查询" severity="secondary" text :disabled="listing.isFetching.value" @click="refresh" /><Button :icon="route.query.refresh === 'paused' ? 'pi pi-play' : 'pi pi-pause'" :aria-label="route.query.refresh === 'paused' ? '继续自动刷新' : '暂停自动刷新'" severity="secondary" text @click="router.replace({ query: { ...route.query, refresh: route.query.refresh === 'paused' ? undefined : 'paused' } })" /></div><Button label="关闭详情" severity="secondary" outlined @click="closeDetail" /></div></template>
  </Drawer>
</template>

<style scoped>
h1, h2 { overflow-wrap: break-word; min-width: 0; }
</style>
