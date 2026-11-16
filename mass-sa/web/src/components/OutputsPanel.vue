<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import InputText from 'primevue/inputtext'
import TimeValue from './TimeValue.vue'
import OutputRecordView from './OutputRecord.vue'
import { readAPI, type Page } from '../api'
import { queryText, usePolling } from '../polling'
import type { OutputRecord } from '../output-types'

// Ticket details can reuse this panel with project + ticket. Trigger facts use
// relatedTrigger so association alone never implies a channel subscription.
const props = withDefaults(defineProps<{
  project: string; trigger?: string; ticket?: string; relatedTrigger?: string;
  category?: string; title: string; emptyText: string; cursorKey?: string;
}>(), { cursorKey: 'outputCursor' })
const route = useRoute()
const router = useRouter()
const interval = usePolling()
const cursor = computed(() => queryText(route.query[props.cursorKey]))
const params = computed(() => ({
  project: props.project, trigger: props.trigger || '', ticket: props.ticket || '',
  relatedTrigger: props.relatedTrigger || '', category: props.category || '',
  source: queryText(route.query.outputSource), thread: queryText(route.query.outputThread),
  limit: '50', cursor: cursor.value,
}))
const listing = useQuery({
  queryKey: computed(() => ['outputs', params.value]),
  queryFn: ({ signal }) => readAPI<Page<OutputRecord>>('outputs', params.value, signal),
  networkMode: 'always',
  refetchInterval: computed(() => cursor.value ? false : interval.value),
  refetchOnWindowFocus: computed(() => !cursor.value && interval.value !== false),
  refetchOnReconnect: computed(() => !cursor.value && interval.value !== false),
})
function pageTo(value?: string) { return router.push({ query: { ...route.query, [props.cursorKey]: value } }) }
async function refresh() {
  if (cursor.value) await pageTo()
  await listing.refetch()
}
const target = reactive({ source: '', thread: '' })
watch(() => route.query, query => { target.source = queryText(query.outputSource); target.thread = queryText(query.outputThread) }, { immediate: true })
function filterTarget() {
  void router.push({ query: { ...route.query, outputSource: target.source || undefined, outputThread: target.thread || undefined, replyCursor: undefined, factCursor: undefined, [props.cursorKey]: undefined } })
}
</script>

<template>
  <section class="detail-section outputs-panel" :aria-label="title">
    <div class="section-line"><h3>{{ title }}</h3><Button severity="secondary" outlined :disabled="listing.isFetching.value" @click="refresh">刷新{{ title }}</Button></div>
    <p class="muted">只读查看 · 不登记消费者、不确认渠道结果、不推进 checkpoint。</p>
    <details :open="!!route.query.outputSource || !!route.query.outputThread"><summary>筛选输出目标</summary><form class="output-filters" :aria-label="`${title}目标筛选`" @submit.prevent="filterTarget">
      <label>目标来源<InputText name="source" v-model="target.source" /></label>
      <label>目标 Thread<InputText name="thread" v-model="target.thread" /></label>
      <Button type="submit" severity="secondary" outlined>筛选目标</Button>
    </form></details>
    <p v-if="listing.isPending.value" role="status">正在读取{{ title }}…</p>
    <div v-if="listing.isError.value" role="alert" class="error-message"><p>{{ title }}读取失败{{ listing.data.value ? ' · stale' : '' }}。{{ listing.error.value?.message }}</p><p v-if="listing.data.value">最后成功 <TimeValue :value="listing.data.value.observedAt" /></p><Button severity="secondary" outlined @click="refresh">重试{{ title }}</Button></div>
    <p v-if="listing.data.value?.items.length === 0">{{ emptyText }}</p>
    <template v-if="listing.data.value">
      <p class="query-meta">遍历快照于 <TimeValue :value="listing.data.value.observedAt" /></p>
      <OutputRecordView v-for="record in listing.data.value.items" :key="`${record.projectId}/${record.id}`" :record="record" />
      <div v-if="cursor || listing.data.value.next" class="pagination"><Button v-if="cursor" severity="secondary" outlined @click="pageTo()">回到首批输出</Button><Button v-if="listing.data.value.next" severity="secondary" outlined @click="pageTo(listing.data.value.next)">更多{{ title }}</Button></div>
    </template>
  </section>
</template>

<style scoped>
.section-line { justify-content: space-between; flex-wrap: wrap; }
.section-line h3 { margin: 0; }
.output-filters { display: flex; flex-wrap: wrap; gap: var(--space-sm); align-items: end; margin-block: var(--space-md); }
.output-filters label { flex: 1; min-width: 0; font-size: var(--text-sm); color: var(--color-muted); }
@media (max-width: 40rem) { .output-filters label { flex-basis: 100%; } }
</style>
