<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import Tag from 'primevue/tag'
import TimeValue from './TimeValue.vue'
import { outputKindLabels, type OutputRecord } from '../output-types'
defineProps<{ record: OutputRecord }>()
const route = useRoute()
</script>

<template>
  <article class="record output-record" :aria-label="`Output ${record.id}`">
    <h4>{{ outputKindLabels[record.kind] || (record.category === 'fact' ? `业务事实 · ${record.kind}` : record.kind) }}</h4>

    <p>记录于 <TimeValue :value="record.recordedAt" /></p>
    <pre class="output-content">{{ record.summary }}</pre>
    <details :data-reading="`output-record-${record.id}`"><summary>关联与目标详情</summary><p class="mono">{{ record.projectId }} / {{ record.id }} · 序号 {{ record.sequence }}</p><dl class="facts">
      <dt>来源 Trigger</dt><dd><RouterLink v-if="record.triggerId" class="touch-link mono" :to="{ name: 'triggers', query: { selected: record.triggerId, project: record.projectId, detailTab: 'outputs', refresh: route.query.refresh } }">{{ record.triggerId }}</RouterLink><span v-else>无来源 Trigger</span></dd>
      <dt>关联 Ticket</dt><dd><RouterLink v-if="record.ticketId" class="touch-link mono" :to="{ name: 'tickets', query: { selected: record.ticketId, project: record.projectId, refresh: route.query.refresh } }">{{ record.ticketId }}</RouterLink><span v-else>无关联 Ticket</span></dd>
      <dt v-if="record.factId">摘要依据</dt><dd v-if="record.factId" class="mono">{{ record.factId }}</dd>
      <dt>显式目标</dt><dd><span v-if="!record.targets.length">无外部目标</span><p v-for="target in record.targets" :key="`${target.source}/${target.threadId}`" class="mono">{{ target.source }} / {{ target.threadId }}</p></dd>
    </dl></details>
    <details v-if="record.evidence.length" :data-reading="`output-evidence-${record.id}`"><summary>事实证据</summary><p v-for="item in record.evidence" :key="item" class="mono">{{ item }}</p></details>
    <details :data-reading="`output-audit-${record.id}`"><summary>消费者读取与渠道确认 · {{ record.audit.length }} 条记录</summary>
    <p v-if="!record.audit.length" class="muted">未见消费者记录；这不证明渠道绝对未发送。</p>
    <dl v-for="audit in record.audit" :key="`${audit.consumer}/${audit.source}/${audit.threadId}`" class="facts output-audit">
      <dt>消费者</dt><dd class="mono">{{ audit.consumer }}</dd>
      <dt>该消费者的目标</dt><dd class="mono">{{ audit.source }} / {{ audit.threadId }}</dd>
      <dt>ReadAt</dt><dd><TimeValue :value="audit.readAt" /></dd>
      <dt>渠道结果</dt><dd><Tag :value="audit.outcome || '未记录渠道结果'" :severity="audit.outcome === 'delivered' ? 'success' : audit.outcome === 'failed' ? 'danger' : 'warn'" /></dd>
      <dt>结果确认时间</dt><dd><TimeValue :value="audit.confirmedAt" /></dd>
      <dt v-if="audit.reference">确认引用 / 原因</dt><dd v-if="audit.reference">{{ audit.reference }}</dd>
    </dl>
    <p v-if="record.audit.length" class="muted">每条结果只适用于列出的消费者与目的地；ReadAt 不证明渠道送达。</p>
    </details>
  </article>
</template>

<style scoped>
h4 { margin-block: var(--space-sm); font-weight: 500; color: var(--color-ink); overflow-wrap: anywhere; }
.output-content { font-family: var(--font-body); font-size: var(--text-md); }
.output-audit { padding-block: var(--space-md); border-bottom: 1px solid var(--color-rule); }
</style>
