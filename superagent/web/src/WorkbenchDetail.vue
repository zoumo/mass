<script setup lang="ts">
import { computed } from 'vue'
import { formatTime, messageTitle, routingDeliveryLabel, sourceLabel, stateLabel, threadLabel } from './workbench'
import type { RecordLinks, RelatedKind, RelatedPages, Workbench, WorkbenchRow } from './workbench'

const props = defineProps<{
 view: Workbench
 record: WorkbenchRow
 related: RelatedPages
 topicTitles: Record<string, string>
 links: RecordLinks
}>()
defineEmits<{ inspect: [] }>()

const id = computed(() => props.record.id || '')
const items = (kind: RelatedKind) => {
 const page = props.related[kind]
 return page instanceof Error ? [] : page?.items || []
}
const missing = (kind: RelatedKind, empty: string) => {
 const page = props.related[kind]
 if (page instanceof Error) return '无法读取记录。'
 return page ? empty : '正在加载…'
}
const hasMore = (kind: RelatedKind) => {
 const page = props.related[kind]
 return page && !(page instanceof Error) && !!page.nextCursor
}
const topicName = (topicID: string) => props.topicTitles[topicID] || topicID
const sectionTitle = computed(() => props.view === 'messages' ? '关联工作' : '工作背景')
const recordTime = computed(() => props.view === 'messages' ? props.record.receivedAt : props.record.updatedAt || props.record.createdAt)
const recordStatus = computed(() => {
 const record = props.record
 return props.view === 'messages' ? record.routing?.state || record.echo?.status || record.acceptance : record.status
})
const topicIDs = computed(() => [...new Set(props.record.relations?.map(relation => relation.topicId) || [])])
type ActivityLink = { view: string; id: string; label: string }
type Activity = { key: string; at?: string; actor: string; title: string; reason?: string; links?: ActivityLink[] }
const activity = computed(() => {
 const { record, view } = props
 const entries: Activity[] = []
 const relations = view === 'topics' ? items('relations') : record.relations || []
 for (const relation of relations) {
  entries.push({
   key: relation.id || '', at: relation.createdAt,
   actor: relation.status ? `Topic · ${routingDeliveryLabel(relation.status)}` : `Topic · 第 ${relation.round} 轮`,
   title: relation.action === 'returned' ? 'Topic 将消息退回路由器' : relation.action === 'received' ? '消息路由至 Topic' : stateLabel(relation.action),
   reason: relation.reason,
   links: view === 'topics' && relation.messageId ? [{ view: 'messages', id: relation.messageId, label: '查看消息' }]
    : relation.topicId ? [{ view: 'topics', id: relation.topicId, label: topicName(relation.topicId) }] : [],
  })
 }
 for (const reply of items('outbox')) {
  entries.push({
   key: reply.id!, actor: '渠道回复',
   title: `渠道回执 · ${stateLabel(reply.status)}`, reason: reply.text,
   links: [{ view: 'outbox', id: reply.id!, label: '查看发送记录' }],
  })
 }
 if (view === 'messages') {
  if (record.routing?.state === 'ignored') entries.push({ key: 'ignored', actor: '路由器', title: '路由器忽略了消息' })
  entries.push({ key: 'intake', at: record.receivedAt, actor: '核心服务', title: record.acceptance === 'accepted' ? '核心服务接收了消息' : stateLabel(record.acceptance) })
  if (record.echo) {
   const echo = record.echo
   entries.push({
    key: 'echo', at: echo.resolvedAt || record.receivedAt, actor: '渠道 · 回声判定',
    title: stateLabel(echo.status), reason: echo.releaseReason,
    links: echo.match ? [{ view: 'outbox', id: echo.match.outboxId, label: '查看匹配的发送记录' }] : [],
   })
  }
 }
 if (view === 'topics') {
  for (const [event, change] of [['canceled', record.cancellation], ['reopened', record.reopening]] as const) {
   if (change) entries.push({ key: event, at: change.at, actor: 'Topic', title: `Topic ${stateLabel(event)}`, reason: change.reason })
  }
  if (record.completion) entries.push({ key: 'completed', at: record.completion.completedAt, actor: 'Topic', title: '已记录业务验收', reason: record.completion.reason })
 }
 entries.sort((a, b) => (b.at || '').localeCompare(a.at || ''))
 return view === 'topics' ? entries.slice(0, 12) : entries.reverse()
})
const activityError = computed(() => (props.view === 'topics' ? ['relations', 'outbox'] as const : ['outbox'] as const)
 .some(kind => props.related[kind] instanceof Error))
</script>

<template>
 <article class="record-detail" :aria-label="view === 'messages' ? '当前消息' : view === 'topics' ? '当前 Topic' : '当前 Issue'">
  <header class="record-header">
   <div class="record-kicker"><span class="state-pill" :data-state="recordStatus">{{ stateLabel(recordStatus) }}</span>
    <span v-if="view === 'messages'">{{ sourceLabel(record.source) }}<template v-if="record.addressed === 'direct'"> · 单聊</template></span>
    <time :datetime="recordTime" :title="recordTime">{{ formatTime(recordTime) }}</time>
   </div>
   <h2>{{ view === 'messages' ? messageTitle(record) : record.title || id }}</h2>
   <p v-if="view === 'topics' && record.description" class="record-description">{{ record.description }}</p>
   <p v-if="view === 'messages' && record.actor" class="record-description muted">发送方 · {{ record.actor }}</p>
  </header>

  <template v-if="view === 'messages'">
   <section class="detail-section" aria-label="渠道原文">
    <h3>渠道原文</h3>
    <pre v-if="record.text" class="document-body message-original" data-testid="message-body" tabindex="0">{{ record.text }}</pre>
    <p v-else class="muted">渠道未提供正文。</p>
   </section>
   <div class="routing-state">
    <span>当前路由状态</span>
    <strong>{{ record.routing ? `第 ${record.routing.round} 轮 · ${stateLabel(record.routing.state)}` : record.echo?.status === 'outbound_echo' ? '自身回声 · 未投递给角色' : '无路由记录' }}</strong>
   </div>
  </template>

  <section v-if="view !== 'issues'" class="detail-section" aria-label="活动记录">
   <h3>{{ view === 'topics' ? '最近活动' : '处理记录' }}</h3>
   <p v-if="activityError" role="status" class="muted">部分记录读取失败。</p>
   <p v-if="!activity.length" class="muted">{{ missing('relations', '暂无活动记录。') }}</p>
   <ol v-else class="timeline">
    <li v-for="entry in activity" :key="entry.key">
     <div class="entry-meta"><span>{{ entry.actor }}</span><time v-if="entry.at" :datetime="entry.at" :title="entry.at">{{ formatTime(entry.at) }}</time></div>
     <h4>{{ entry.title }}</h4>
     <details v-if="entry.reason && entry.reason.length > 160 && view === 'topics'" class="entry-reason"><summary>查看原因</summary><p>{{ entry.reason }}</p></details>
     <p v-else-if="entry.reason">{{ entry.reason }}</p>
     <div v-if="entry.links?.length" class="entry-links">
      <a v-for="link in entry.links" :key="link.id" class="text-link" :href="links.href(link.view, link.id)" @click="links.follow($event, link.view, link.id)">{{ link.label }} <span aria-hidden="true">→</span></a>
     </div>
    </li>
   </ol>
   <div v-if="view === 'topics'" class="entry-links history-links">
    <a class="text-link" :href="links.href('relations', '', id)" @click="links.follow($event, 'relations', '', id)">路由历史 →</a>
    <a class="text-link" :href="links.href('routing-deliveries', '', id)" @click="links.follow($event, 'routing-deliveries', '', id)">路由投递 →</a>
    <a class="text-link" :href="links.href('outbox', '', id)" @click="links.follow($event, 'outbox', '', id)">关联回执 →</a>
   </div>
  </section>

  <template v-if="view === 'issues'">
   <section class="detail-section">
    <h3>目标 <span v-if="record.goalRevision" class="section-note">版本 {{ record.goalRevision }}</span></h3>
    <pre v-if="record.goal" class="document-body">{{ record.goal }}</pre>
    <p v-else class="muted">暂无要求。</p>
    <ul v-if="record.materials?.length" class="plain-list">
     <li v-for="(material, index) in record.materials" :key="index">
      <a v-if="material.messageId" class="text-link" :href="links.href('messages', material.messageId)" @click="links.follow($event, 'messages', material.messageId)">{{ material.label || '来源消息' }} →</a>
      <span v-else>{{ material.label ? material.label + ' · ' : '' }}{{ material.uri }}</span>
     </li>
    </ul>
    <div v-if="record.blockers?.length" class="blockers">
     <h4>受阻原因</h4>
     <ul class="plain-list"><li v-for="blocker in record.blockers" :key="blocker.id">
      {{ blocker.reason }}
      <a v-if="blocker.dependencyIssueId" class="text-link" :href="links.href('issues', blocker.dependencyIssueId)" @click="links.follow($event, 'issues', blocker.dependencyIssueId)">查看依赖的 Issue →</a>
     </li></ul>
    </div>
   </section>
   <section class="detail-section">
    <h3>执行记录</h3>
    <div class="routing-state"><span>当前执行器 Session</span><strong>{{ record.currentExecutionId ? stateLabel(record.workerSession?.state) : '当前没有执行' }}</strong></div>
    <p v-if="record.workerSession?.observationGap" class="muted">观测中断 · {{ record.workerSession.observationGap }}</p>
    <ul v-if="items('executions').length" class="compact-list">
     <li v-for="execution in items('executions')" :key="execution.id">
      <a class="text-link" :href="links.href('executions', execution.id)" @click="links.follow($event, 'executions', execution.id)">第 {{ execution.generation }} 代 <span aria-hidden="true">→</span></a>
      <small>{{ stateLabel(execution.authority) }} · 启动输入 {{ stateLabel(execution.startup?.status) }}</small>
     </li>
    </ul>
    <p v-else class="muted">{{ missing('executions', '暂无执行记录。') }}</p>
    <a v-if="hasMore('executions')" class="text-link" :href="links.href('executions', '', id, 'issueId')" @click="links.follow($event, 'executions', '', id, 'issueId')">全部执行记录 →</a>
   </section>
   <section class="detail-section">
    <h3>执行报告</h3>
    <ul v-if="items('reports').length" class="compact-list">
     <li v-for="report in items('reports')" :key="report.id">
      <div><span class="state-pill" :data-state="report.kind">{{ stateLabel(report.kind) }}</span><small v-if="report.historical"> · 历史执行</small></div>
      <a class="text-link" :href="links.href('reports', report.id)" @click="links.follow($event, 'reports', report.id)">{{ report.summary || '查看报告' }} <span aria-hidden="true">→</span></a>
     </li>
    </ul>
    <p v-else class="muted">{{ missing('reports', '暂无报告。') }}</p>
    <a v-if="hasMore('reports')" class="text-link" :href="links.href('reports', '', id, 'issueId')" @click="links.follow($event, 'reports', '', id, 'issueId')">全部报告 →</a>
   </section>
  </template>
  <section v-if="view === 'issues' || record.completion" class="detail-section acceptance-section">
   <h3>业务验收</h3>
   <template v-if="record.completion">
    <strong><i class="pi pi-check-circle" aria-hidden="true"></i> 已验收</strong>
    <p>{{ record.completion.reason }}</p>
    <ul v-if="record.completion.evidence?.length" class="plain-list"><li v-for="(evidence, index) in record.completion.evidence" :key="index">{{ evidence }}</li></ul>
    <time :datetime="record.completion.completedAt" :title="record.completion.completedAt">{{ formatTime(record.completion.completedAt) }}</time>
   </template>
   <p v-else class="muted">暂无明确的验收记录。</p>
  </section>
 </article>

 <aside class="record-context" :aria-label="sectionTitle">
  <h2>{{ sectionTitle }}</h2>
  <template v-if="view === 'topics'">
   <section class="context-group">
    <h3>来源</h3>
    <ul v-if="items('subscriptions').length" class="context-list">
     <li v-for="source in items('subscriptions')" :key="source.id">
      <a class="text-link" :title="source.threadKey" :href="links.href('messages', '', source.threadKey, 'threadKey')" @click="links.follow($event, 'messages', '', source.threadKey, 'threadKey')">{{ threadLabel(source.threadKey) }} →</a>
      <a v-if="source.messageId" class="sub-link" :href="links.href('messages', source.messageId, source.threadKey, 'threadKey')" @click="links.follow($event, 'messages', source.messageId, source.threadKey, 'threadKey')">查看来源消息</a>
     </li>
    </ul>
    <p v-else class="muted">{{ missing('subscriptions', '暂无来源线程记录。') }}</p>
    <a v-if="hasMore('subscriptions')" class="text-link" :href="links.href('subscriptions', '', id)" @click="links.follow($event, 'subscriptions', '', id)">全部来源 →</a>
   </section>
   <section class="context-group">
    <h3>关联 Issue</h3>
    <ul v-if="items('issues').length" class="context-list">
     <li v-for="issue in items('issues')" :key="issue.id">
      <a class="text-link" :href="links.href('issues', issue.id, id)" @click="links.follow($event, 'issues', issue.id, id)">{{ issue.title || issue.id }} →</a>
      <small>{{ stateLabel(issue.status) }}</small>
     </li>
    </ul>
    <p v-else class="muted">{{ missing('issues', '暂无关联 Issue。') }}</p>
    <a v-if="hasMore('issues')" class="text-link" :href="links.href('issues', '', id)" @click="links.follow($event, 'issues', '', id)">查看该 Topic 的全部 Issue →</a>
   </section>
  </template>
  <section v-if="view === 'issues'" class="context-group">
   <h3>Topic</h3>
   <a v-if="record.topicId" class="text-link" :href="links.href('topics', record.topicId)" @click="links.follow($event, 'topics', record.topicId)">{{ topicName(record.topicId) }} →</a>
   <p v-else class="muted">未记录所属 Topic。</p>
  </section>
  <section v-if="view !== 'messages' && record.pullRequests?.length" class="context-group">
   <h3>关注的拉取请求</h3>
   <ul class="context-list">
    <li v-for="pr in record.pullRequests" :key="pr.id">
     <a class="text-link" :href="links.href('pull-requests', pr.id)" @click="links.follow($event, 'pull-requests', pr.id)">{{ pr.canonicalUrl }}</a>
     <a class="sub-link" :href="links.href('subscriptions', pr.subscriptionId)" @click="links.follow($event, 'subscriptions', pr.subscriptionId)">查看订阅</a>
    </li>
   </ul>
  </section>
  <template v-if="view === 'messages'">
   <section class="context-group">
    <h3>来源线程</h3>
    <a v-if="record.threadKey" class="text-link" :title="record.threadKey" :href="links.href('messages', id, record.threadKey, 'threadKey')" @click="links.follow($event, 'messages', id, record.threadKey, 'threadKey')">{{ threadLabel(record.threadKey) }} →</a>
    <a v-if="record.threadKey" class="sub-link" :href="links.href('threads', record.threadKey)" @click="links.follow($event, 'threads', record.threadKey)">查看线程记录</a>
    <p v-else class="muted">未记录来源线程。</p>
   </section>
   <section class="context-group">
    <h3>关联 Topic</h3>
    <ul v-if="topicIDs.length" class="context-list">
     <li v-for="topicID in topicIDs" :key="topicID">
      <a class="text-link" :href="links.href('topics', topicID)" @click="links.follow($event, 'topics', topicID)">{{ topicName(topicID) }} →</a>
     </li>
    </ul>
    <p v-else class="muted">此消息暂无关联 Topic。</p>
   </section>
  </template>
  <section class="context-group evidence-links">
   <h3>记录与凭据</h3>
   <a v-if="record.bindingId" class="text-link" :href="links.href('bindings', record.bindingId)" @click="links.follow($event, 'bindings', record.bindingId)">查看 Session →</a>
   <a v-if="view === 'messages'" class="text-link" :href="links.href('routing-deliveries', '', id, 'messageId')" @click="links.follow($event, 'routing-deliveries', '', id, 'messageId')">路由投递 →</a>
   <a v-if="view === 'messages'" class="text-link" :href="links.href('outbox', '', id, 'messageId')" @click="links.follow($event, 'outbox', '', id, 'messageId')">消息回执 →</a>
   <button type="button" class="outline-button" @click="$emit('inspect')">查看记录详情</button>
  </section>
 </aside>
</template>
