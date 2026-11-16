<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import TimeValue from './TimeValue.vue'
import type { TicketDetail } from '../ticket-types'

defineProps<{ current: TicketDetail }>()
const route = useRoute()
function triggerLink(id: string, project: string) { return { name: 'triggers', query: { project, selected: id, refresh: route.query.refresh } } }
function ticketLink(id: string, project: string) { return { name: 'tickets', query: { project, selected: id, selectedProject: project, detailTab: 'execution', refresh: route.query.refresh } } }
function sessionLink(id: string, project: string) { return { name: 'resources', query: { project, selected: id, resourceTab: 'sessions', scope: 'associated', refresh: route.query.refresh } } }
</script>

<template>
  <div class="ticket-process">
    <section class="record" aria-label="Assignment 历史">
      <h3>当前与历史派工</h3>
      <p v-if="!current.assignments.length">{{ current.currentAssignmentId ? '当前派工引用 unavailable；未找到匹配的 Assignment 记录' : '尚未派工 · 无 Session' }}</p>
      <p class="muted">当前责任按工作项的当前引用标记。相同 Session 可有多次派工；动作未记录时不推断恢复、重试或替换。</p>
      <details v-for="a in current.assignments" :key="a.id" :data-reading="`assignment-${a.id}`">
        <summary :id="`assignment-${a.id}`">{{ a.current ? '当前派工' : '历史派工' }} · {{ a.workerName || '执行者未记录' }} · {{ a.status || '状态未记录' }}</summary>
        <dl class="facts">
          <dt>Assignment</dt><dd class="mono">{{ a.id }}</dd>
          <dt>Worker</dt><dd>{{ a.workerName || '未记录' }}</dd>
          <dt>固定 Session</dt><dd><template v-if="a.sessionId"><RouterLink class="touch-link" :to="sessionLink(a.sessionId, current.projectId)">{{ a.observation.session?.sessionId === a.sessionId ? a.observation.session.name || '查看会话' : '查看会话' }}</RouterLink><span class="meta mono">{{ a.sessionId }}</span></template><span v-else>没有持久身份</span></dd>
          <dt>请求 Runtime</dt><dd>{{ a.runtimeRef || '未记录' }}</dd>
          <dt>请求 model / effort</dt><dd>{{ a.requested.model || '默认' }} / {{ a.requested.effort || '默认' }}</dd>
          <dt>执行引用</dt><dd class="mono">{{ a.executionId || '未记录' }} · {{ a.executionState || '未记录' }}</dd>
          <dt>等待恢复后输入</dt><dd>{{ a.awaitingResumedInput ? '是（已有持久证据）' : '否' }}</dd>
          <dt>创建 / 更新</dt><dd><TimeValue :value="a.createdAt" /> / <TimeValue :value="a.updatedAt" /></dd>
          <dt>停止时间</dt><dd><TimeValue :value="a.stoppedAt" /></dd>
          <dt>Session 观察</dt><dd>{{ a.observation.status }}{{ a.observation.stale ? ' · stale，保留上次值' : '' }} · <TimeValue :value="a.observation.attemptedAt" /></dd>
          <dt>SessionState</dt><dd>{{ a.observation.session?.state || '未观测' }}</dd>
          <dt v-if="a.observation.lastSuccessAt">最后成功</dt><dd v-if="a.observation.lastSuccessAt"><TimeValue :value="a.observation.lastSuccessAt" /></dd>
        </dl>
        <p v-if="a.observation.error" class="error-message">{{ a.observation.error }}</p>
        <p v-if="a.reconciliationRequired" class="error-message">需要对账：{{ a.reconciliationReason }}</p>
      </details>
    </section>

    <section class="record" aria-label="父子委派">
      <h3>父子与后代委派</h3>
      <p v-if="!current.parentTicketId">这是根工作。</p>
      <p v-else>父工作 <RouterLink class="touch-link" :to="ticketLink(current.parentTicketId, current.projectId)">{{ current.parent?.title || '查看父工作' }}</RouterLink><span v-if="current.parentUnavailable" class="muted"> · 父记录 unavailable</span></p>
      <p v-if="current.parentTicketId && current.rootTicketId && current.rootTicketId !== current.parentTicketId"><RouterLink class="touch-link" :to="ticketLink(current.rootTicketId, current.projectId)">查看根工作</RouterLink></p>
      <p class="muted">独立委派关系不表示根工作的来源事件已投递给所有后代。直接关联事件在下方单独列出。</p>
      <p v-if="!current.descendants.length">没有后代工作</p>
      <ul class="ticket-relations">
        <li v-for="child in current.descendants" :key="child.id">
          <RouterLink class="touch-link" :to="ticketLink(child.id, current.projectId)">{{ child.title || '查看子工作' }}</RouterLink>
          <span class="meta">{{ child.parentTicketId === current.id ? '直属子工作' : '后代工作' }} · 业务 {{ child.businessState || '未记录' }}</span>
          <details :data-reading="`child-${child.id}`"><summary>委派身份</summary><dl class="facts"><dt>工作项</dt><dd class="mono">{{ child.id }}</dd><dt>父工作项</dt><dd class="mono">{{ child.parentTicketId }}</dd></dl></details>
        </li>
      </ul>
      <details v-if="current.createdByAssignment || current.createdBySessionId" data-reading="creator">
        <summary>创建来源</summary><dl class="facts"><dt>创建者 Assignment</dt><dd class="mono">{{ current.createdByAssignment || '未记录' }}</dd><dt>创建者 Session</dt><dd><RouterLink v-if="current.createdBySessionId" class="touch-link" :to="sessionLink(current.createdBySessionId, current.projectId)">查看会话</RouterLink><span class="meta mono">{{ current.createdBySessionId || '未记录' }}</span></dd></dl>
      </details>
    </section>

    <section class="record" aria-label="工作报告">
      <h3>报告记录</h3><p v-if="!current.reports.length">未记录报告</p>
      <p class="muted">本地保留报告正文、时间和来源。展开报告查看完整内容。</p>
      <details v-for="(report, i) in current.reports" :key="`${report.assignmentId}/${report.sourceEventId}/${i}`" :data-reading="`report-${i}`">
        <summary :id="`report-${i}`">报告 · <TimeValue :value="report.reportedAt" /></summary>
        <pre v-if="report.body" class="report-body">{{ report.body }}</pre>
        <dl class="facts">
          <dt>报告</dt><dd class="mono">{{ report.id || '未记录' }}</dd>
          <dt>Assignment</dt><dd class="mono">{{ report.assignmentId }}</dd>
          <dt>报告时间</dt><dd><TimeValue :value="report.reportedAt" /></dd>
          <dt>Session</dt><dd><template v-if="report.sessionId"><RouterLink class="touch-link" :to="sessionLink(report.sessionId, current.projectId)">查看会话</RouterLink><span class="meta mono">{{ report.sessionId }}</span></template><span v-else>未记录</span></dd>
          <dt>来源 Event / Item / Turn</dt><dd class="mono">{{ report.sourceEventId }} / {{ report.sourceItemId || '无' }} / {{ report.sourceTurnId || '无' }}</dd>
          <dt>结构化</dt><dd>{{ report.structured ? '是' : '否' }}</dd>
        </dl>
        <p v-if="report.parseError" class="error-message">{{ report.parseError }}</p>
      </details>
    </section>

    <section class="record" aria-label="直接关联 Triggers">
      <h3>直接关联事件</h3><p v-if="!current.directTriggers.length">没有直接关联的外部 Trigger</p>
      <article v-for="trigger in current.directTriggers" :key="trigger.triggerId" class="ticket-input">
        <RouterLink class="touch-link" :to="triggerLink(trigger.triggerId, current.projectId)">{{ trigger.summary || '查看事件' }}</RouterLink>
        <p v-if="!trigger.deliveries.length">已关联工作，未记录向此 Ticket 的投递</p>
        <details :data-reading="`trigger-${trigger.triggerId}`"><summary>事件来源身份</summary><dl class="facts"><dt>Trigger</dt><dd class="mono">{{ trigger.triggerId }}</dd><dt>来源</dt><dd>{{ trigger.source }}</dd><dt>Thread</dt><dd class="mono">{{ trigger.threadId }}</dd></dl></details>
        <div v-for="delivery in trigger.deliveries" :key="delivery.inboxId" class="ticket-delivery">
          <p>{{ delivery.state }} · {{ delivery.attempted ? '已尝试，固定历史目标' : delivery.targetResolution === 'blocked_before_attempt' ? '投递前阻塞，未尝试' : delivery.targetResolution === 'handled_without_attempt' ? '已处理，未尝试投递' : '待解析执行者 / 尚未尝试' }}</p>
          <RouterLink v-if="delivery.attempted && delivery.sessionId" class="touch-link" :to="sessionLink(delivery.sessionId, current.projectId)">查看当时的执行会话</RouterLink>
          <details :data-reading="`delivery-${delivery.inboxId}`"><summary>投递技术详情</summary>
            <p class="mono">Inbox {{ delivery.inboxId }}</p>
            <p v-if="delivery.attempted" class="mono">Assignment {{ delivery.assignmentId || '历史未记录' }} · Session {{ delivery.sessionId || '历史未记录' }} · Execution {{ delivery.executionId || '历史未记录' }}</p>
            <p v-for="batch in delivery.batches" :key="batch.id" class="mono">Batch {{ batch.id }} · {{ batch.state }} · {{ batch.attempted ? '固定目标' : '未尝试候选' }} {{ batch.assignmentId || '无' }} / {{ batch.sessionId || '无' }}</p>
          </details>
        </div>
      </article>
    </section>

    <section class="record" aria-label="内部子工作通知">
      <h3>内部子工作通知</h3><p class="muted">子工作生命周期事实，与外部事件分开记录。</p><p v-if="!current.internalInputs.length">没有内部子工作通知</p>
      <article v-for="input in current.internalInputs" :key="input.inboxId" class="ticket-input">
        <RouterLink v-if="input.childTicketId" class="touch-link" :to="ticketLink(input.childTicketId, current.projectId)">{{ current.descendants.find(child => child.id === input.childTicketId)?.title || '查看子工作' }}</RouterLink>
        <p>{{ input.childState || '历史状态未记录' }} · revision {{ input.revision }} · {{ input.summary }}</p>
        <p>{{ input.state }} · {{ input.targetResolution === 'fixed_evidence' ? '已尝试，固定历史目标' : input.targetResolution === 'blocked_before_attempt' ? '投递前阻塞，未尝试' : input.targetResolution === 'handled_without_attempt' ? '已处理，未尝试投递' : '待解析执行者 / 尚未尝试' }}</p>
        <details :data-reading="`input-${input.inboxId}`"><summary>通知技术详情</summary>
          <p class="mono">Inbox {{ input.inboxId }}</p><p class="mono">子工作 {{ input.childTicketId || '未记录' }}</p>
          <p v-if="input.assignmentId || input.sessionId" class="mono">Assignment {{ input.assignmentId }} · Session {{ input.sessionId }} · Execution {{ input.executionId || '未记录' }}</p>
          <p v-for="batch in input.batches" :key="batch.id" class="mono">Batch {{ batch.id }} · {{ batch.state }} · {{ batch.attempted ? '固定目标' : '未尝试候选' }} {{ batch.assignmentId || '无' }} / {{ batch.sessionId || '无' }}</p>
        </details>
      </article>
    </section>
  </div>
</template>

<style scoped>
.record { margin-block: 0; padding: 24px 0; border-radius: 0; border: 0; border-bottom: 1px solid var(--color-line, var(--color-rule)); }
.record:first-child { padding-top: 0; }
h3 { margin: 0 0 12px; }
.ticket-relations { list-style: none; padding: 0; margin: 0; }
.ticket-relations li, .ticket-input { padding-block: 12px; border-bottom: 1px solid var(--color-line, var(--color-rule)); }
.ticket-relations li:last-child, .ticket-input:last-child { border-bottom: 0; }
.facts { margin-block: 12px; }
.ticket-delivery { margin-top: 12px; }
pre, .mono, dd { overflow-wrap: anywhere; }
</style>
