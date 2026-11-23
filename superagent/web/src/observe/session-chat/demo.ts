import examples from '../../../../../harness/modules/runtime/event-examples.json'
import type { Binding, SessionEvent } from './protocol'

export const demoBindings: Binding[] = [
  { id: 'demo-topic', role: 'topic', ownerId: 'topic-demo', sessionId: 'topic-demo', sessionUid: 'demo-topic', namespace: 'demo', name: 'release-review', state: 'ready' },
  { id: 'demo-worker', role: 'issue', ownerId: 'issue-demo', sessionId: 'worker-demo', sessionUid: 'demo-worker', namespace: 'demo', name: 'check-readme', state: 'ready' },
]

export function demoSession(binding: Binding, emit: (event: SessionEvent) => void, end: (reason: string) => void) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let active = false
  let item = ''
  let interaction = ''
  let interactionKind = ''
  const event = (type: string, payload: SessionEvent['payload'], itemId?: string): SessionEvent => ({
    type, payload, itemId, eventId: crypto.randomUUID(), time: new Date().toISOString(),
    sessionId: binding.sessionId!, sessionName: binding.name, namespace: binding.namespace,
  })
  function finish(cancelled = false) {
    active = false
    clearTimeout(timer)
    if (interaction) {
      emit(event('session.interaction_closed', { interaction, outcome: 'cancelled' }))
      interaction = ''
    }
    emit(event('session.state_update', { previousState: 'running', state: 'idle', stopReason: cancelled ? 'cancelled' : 'end_turn' }))
  }
  function reply() {
    emit(event('agent.message_chunk', { messageId: item, content: [{ type: 'text', text: '已收到。' }] }, item))
    timer = setTimeout(() => {
      const message = binding.role === 'topic' ? '已收到。我会以 Topic Session 的上下文处理这条消息。' : '已收到。我会以当前 Issue Session 的上下文处理这条消息。'
      emit(event('agent.message', { messageId: item, content: [{ type: 'text', text: message }] }, item))
      finish()
    }, 2000)
  }
  function publish() {
    const run = crypto.randomUUID().slice(0, 8)
    for (const sample of examples.events) {
      const payload = { ...sample.payload }
      for (const key of ['id', 'messageId', 'interaction', 'loadId'] as const) {
        if (key in payload && typeof payload[key] === 'string') Object.assign(payload, { [key]: `${run}-${payload[key]}` })
      }
      const row = event(sample.type, payload, 'itemId' in sample ? `${run}-${sample.itemId}` : undefined)
      if (sample.type === 'session.history_replay') {
        row.replay = { loadId: `${run}-load-1` }
        row.payload = { ...row.payload, resumedSession: { ...sample.payload.resumedSession, sessionId: binding.sessionId } }
      }
      emit(row)
    }
  }
  return {
    async connect() { return 'idle' },
    async history() { return { events: [] as SessionEvent[], next: undefined } },
    async prompt(value: string) {
      if (active) return { result: 'not_applied', acceptance: {} }
      active = true
      item = crypto.randomUUID()
      const messageId = crypto.randomUUID()
      emit(event('user.message', { messageId, content: [{ type: 'text', text: value }] }, messageId))
      emit(event('session.state_update', { previousState: 'idle', state: 'running' }))
      timer = setTimeout(reply, 280)
      return { submission: item, result: 'applied_new_work', acceptance: { sent: true, nativeAccepted: true } }
    },
    async cancel() {
      if (!active) return { result: 'noActiveWork' }
      clearTimeout(timer)
      timer = setTimeout(() => finish(true), 400)
      return { result: 'controlAcknowledged', target: item }
    },
    async answer(target: string, answer: unknown) {
      if (!active || interaction !== target) throw new Error('交互已结束')
      const outcome = interactionKind === 'url' && (answer as { action?: string }).action === 'accept' ? 'completed' : 'answered'
      timer = setTimeout(() => {
        emit(event('session.interaction_closed', { interaction, outcome }))
        interaction = ''
        emit(event('session.state_update', { previousState: 'requires_action', state: 'running' }))
        reply()
      }, 450)
      return { interaction: target, evidence: { sent: true, nativeAccepted: true } }
    },
    request(kind: string) {
      if (active) return
      active = true
      item = crypto.randomUUID()
      interaction = crypto.randomUUID()
      interactionKind = kind
      const request = kind === 'permission' ? {
        kind, title: '允许读取 README？', options: [
          { optionId: 'allow-once', name: '允许一次', kind: 'allow', scope: 'once' },
          { optionId: 'reject-once', name: '拒绝', kind: 'reject', scope: 'once' },
        ],
      } : kind === 'form' ? {
        kind, message: '请选择检查策略。', requestedSchema: { type: 'object', properties: { strategy: { type: 'string' } } },
      } : { kind, message: '在外部页面查看授权说明。', url: 'https://example.com/' }
      emit(event('session.interaction_requested', { interaction, request, blocking: true }))
      emit(event('session.state_update', { previousState: 'idle', state: 'requires_action' }))
    },
    publish,
    close() { clearTimeout(timer) },
    disconnect() { clearTimeout(timer); end('演示事件流已中断') },
  }
}
