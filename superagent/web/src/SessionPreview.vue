<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import Button from 'primevue/button'
import Tag from 'primevue/tag'

type PreviewEvent = { id: string; type: string; at: string; text?: string; truncated?: boolean; replayed?: boolean }
type HistoryPage = { events: PreviewEvent[]; nextCursor?: string; observedAt: string }
type Frame = { kind: 'connected' | 'heartbeat' | 'history_changed' | 'preview'; event?: PreviewEvent }

const props = defineProps<{ bindingId: string }>()
const endpoint = `/v1/observer/session-preview/${encodeURIComponent(props.bindingId)}`
const history = ref<PreviewEvent[]>([])
const live = ref<PreviewEvent[]>([])
const observedAt = ref('')
const historyError = ref(false)
const busy = ref(false)
const next = ref('')
const connection = ref('正在连接')
const observationGap = ref(false)
let pageCount = 1
let retry: ReturnType<typeof setTimeout> | undefined
let scheduledHistory: ReturnType<typeof setTimeout> | undefined
let stream: AbortController | undefined
let historyRequest: AbortController | undefined

async function loadHistory(append = false) {
 if (append && (!next.value || busy.value)) return
 historyRequest?.abort()
 historyRequest = new AbortController()
 const { signal } = historyRequest
 busy.value = true
 try {
  const rows: PreviewEvent[] = []
  let cursor = append ? next.value : ''
  let at = ''
  for (let index = 0; index < (append ? 1 : pageCount); index++) {
   const q = new URLSearchParams()
   if (cursor) q.set('cursor', cursor)
   const response = await fetch(`${endpoint}/history?${q}`, { signal, credentials: 'omit' })
   if (!response.ok) throw new Error('无法读取 Session 历史')
   const page: HistoryPage = await response.json()
   rows.push(...page.events)
   cursor = page.nextCursor || ''
   at = page.observedAt
   if (!cursor) break
  }
  if (signal.aborted) return
  history.value = append ? [...history.value, ...rows] : rows
  if (append) pageCount++
  next.value = cursor
  observedAt.value = at
  historyError.value = false
 } catch {
  if (!signal.aborted) historyError.value = true
 } finally {
  if (!signal.aborted) busy.value = false
 }
}

function hintHistory() {
 if (!scheduledHistory) {
  scheduledHistory = setTimeout(() => {
   scheduledHistory = undefined
   void loadHistory()
  }, 300)
 }
}

function receive(frame: Frame) {
 if (frame.kind === 'connected') {
  connection.value = '实时连接'
  hintHistory()
 }
 if (frame.kind === 'history_changed') hintHistory()
 if (frame.kind === 'preview' && frame.event) live.value = [...live.value.slice(-19), frame.event]
}

async function connect() {
 clearTimeout(retry)
 stream?.abort()
 if (!navigator.onLine) {
  disconnect()
  return
 }
 stream = new AbortController()
 const { signal } = stream
 connection.value = observationGap.value ? '正在重新连接' : '正在连接'
 try {
  const response = await fetch(`${endpoint}/live`, { signal, credentials: 'omit' })
  if (!response.ok || !response.body) throw new Error('实时观测不可用')
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader()
  try {
   let pending = ''
   while (!signal.aborted) {
    const { done, value } = await reader.read()
    if (done || signal.aborted) break
    const lines = (pending + value).split('\n')
    pending = lines.pop() || ''
    for (const line of lines) if (line) receive(JSON.parse(line) as Frame)
   }
  } finally {
   await reader.cancel().catch(() => undefined)
   reader.releaseLock()
  }
 } finally {
  if (!signal.aborted) {
   disconnect()
   if (navigator.onLine) retry = setTimeout(() => void connect().catch(() => undefined), 2000)
  }
 }
}

function disconnect() {
 stream?.abort()
 clearTimeout(retry)
 connection.value = '连接已断开'
 observationGap.value = true
 live.value = []
}

function reconnect() {
 void connect().catch(() => undefined)
}

onMounted(() => {
 void loadHistory()
 reconnect()
 window.addEventListener('online', reconnect)
 window.addEventListener('offline', disconnect)
})
onUnmounted(() => {
 stream?.abort()
 historyRequest?.abort()
 clearTimeout(retry)
 clearTimeout(scheduledHistory)
 window.removeEventListener('online', reconnect)
 window.removeEventListener('offline', disconnect)
})
</script>

<template>
 <section aria-label="Session 历史" class="session-preview">
  <h2>Session 历史</h2>
  <p class="muted">MASS Session 的历史跨绑定代次保留。历史可能包含回放记录，也可能缺少实时事件或运行状态变化。</p>
  <p v-if="historyError" role="status">无法读取历史。当前显示上次成功读取的记录。</p>
  <p v-if="observedAt" class="muted">读取时间 {{ observedAt }}</p>
  <p v-if="observedAt && !history.length">此 Session 暂无已保存的事件。</p>
  <ol v-if="history.length" class="session-events">
   <li v-for="event in history" :key="event.id">
    <time :datetime="event.at">{{ event.at }}</time> · {{ event.type }}<span v-if="event.replayed"> · 回放</span>
    <pre v-if="event.text">{{ event.text }}</pre><span v-if="event.truncated">预览已截断</span>
   </li>
  </ol>
  <Button v-if="next" label="加载更多 Session 历史" outlined :disabled="busy" @click="loadHistory(true)" />
  <Button label="刷新 Session 历史" text :disabled="busy" @click="loadHistory()" />
 </section>
 <section aria-label="Session 实时预览" class="session-preview">
  <h2>Session 实时预览 <Tag :value="connection" :severity="connection === '实时连接' ? 'info' : 'secondary'" /></h2>
  <p class="muted">实时预览显示本次连接收到的事件。投递与完成状态以各自的记录为准。</p>
  <p v-if="observationGap" role="status">观测中断 · 断开期间的活动可能缺失。重新连接后会再次读取历史。</p>
  <ol v-if="live.length" class="session-events">
   <li v-for="event in live" :key="event.id">
    <time :datetime="event.at">{{ event.at }}</time> · {{ event.type }}
    <pre v-if="event.text">{{ event.text }}</pre><span v-if="event.truncated">预览已截断</span>
   </li>
  </ol>
 </section>
</template>
