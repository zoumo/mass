export type SessionEvent = {
  eventId: string
  sessionId: string
  namespace: string
  sessionName: string
  type: string
  time: string
  itemId?: string
  replay?: { loadId: string }
  payload: Record<string, unknown>
}

export type Binding = {
  id: string
  role: string
  ownerId: string
  namespace: string
  name: string
  sessionId?: string
  sessionUid?: string
  state: string
}

export const eventLabels: Record<string, string> = {
  'user.message': '用户',
  'agent.message': 'Agent',
  'agent.message_chunk': '回复片段',
  'agent.thinking': '思考',
  'agent.thinking_chunk': '思考片段',
  'agent.tool_call': '工具调用',
  'agent.tool_call_chunk': '工具片段',
  'agent.tool_result': '工具结果',
  'session.state_update': '工作状态',
  'session.error': 'Session 错误',
  'session.unsupported_native_event': '未映射的原生事件',
  'session.info_update': 'Session 信息',
  'session.config_option_update': '配置快照',
  'session.available_commands_update': '可用命令',
  'session.usage_update': '用量',
  'session.history_replay': '历史回放',
  'session.interaction_requested': '交互请求',
  'session.interaction_closed': '交互结算',
}

export const workLabels: Record<string, string> = { idle: '空闲', running: '运行中', requires_action: '等待交互', offline: '离线', closing: '正在回收' }
export const text = (value: unknown) => typeof value === 'string' ? value : ''
export const record = (value: unknown) => value && typeof value === 'object' ? value as Record<string, unknown> : {}
export const list = (value: unknown) => Array.isArray(value) ? value.map(record) : []

export function mergeEvents(history: SessionEvent[], live: SessionEvent[]) {
  return [...new Map([...history, ...live].map(event => [event.eventId, event])).values()]
}

export function conversationRows(events: SessionEvent[]) {
  const rows: SessionEvent[][] = []
  const groups = new Map<string, SessionEvent[]>()
  for (const event of events) {
    const family = event.type.startsWith('agent.tool_') ? 'tool' : event.type.replace(/_chunk$/, '')
    const item = family === 'tool' ? text(event.payload.id) : event.itemId || text(event.payload.messageId)
    const key = item ? JSON.stringify([family, item]) : event.eventId
    const group = groups.get(key)
    if (group) group.push(event)
    else {
      const row = [event]
      groups.set(key, row)
      rows.push(row)
    }
  }
  return rows
}

type Frame = { kind: 'connected'; state: string } | { kind: 'event'; event: SessionEvent } | { kind: 'heartbeat' }

const rawJSON = (JSON as typeof JSON & { rawJSON?: (source: string) => object }).rawJSON

function readFrame(data: string): Frame {
  const frame = JSON.parse(data) as Frame
  if (frame.kind === 'event' && frame.event.type === 'session.unsupported_native_event' && rawJSON) {
    // Preserve numeric tokens across JSON.parse and the expanded JSON preview.
    return JSON.parse(data, (_key, value: unknown, context?: { source?: string }) =>
      typeof value === 'number' && context?.source ? rawJSON(context.source) : value)
  }
  return frame
}

async function* readFrames(body: NonNullable<Response['body']>) {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader()
  try {
    let partial = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) return
      const lines = (partial + value).split('\n')
      partial = lines.pop() || ''
      for (const line of lines) if (line) yield readFrame(line)
    }
  } finally {
    await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}

export function liveSession(binding: Binding, onEvent: (event: SessionEvent) => void, onEnd: (reason: string) => void) {
  const path = '/v1/core/session-bindings/' + encodeURIComponent(binding.id) + '/chat'
  const controller = new AbortController()
  const { signal } = controller
  const matches = (e: SessionEvent) => e.sessionId === binding.sessionId && e.namespace === binding.namespace && e.sessionName === binding.name
  async function request(suffix: string, command?: Record<string, unknown>) {
    const response = await fetch(path + suffix, {
      method: command ? 'POST' : 'GET', signal, credentials: 'omit', mode: 'same-origin', redirect: 'error',
      ...(command && { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(command) }),
    })
    if (!response.ok) {
      const error = await response.json().catch(() => ({}))
      throw new Error(error.error?.message || `Session 请求失败（HTTP ${response.status}）`)
    }
    return response
  }
  async function control(command: Record<string, unknown>) {
    const response = await request('', command)
    return response.json()
  }
  async function observe(frames: AsyncGenerator<Frame>) {
    let reason = '事件流已中断'
    try {
      for await (const frame of frames) {
        if (frame.kind !== 'event') continue
        if (!matches(frame.event)) throw new Error('收到其他 Session 的事件，已关闭连接')
        onEvent(frame.event)
      }
    } catch (error) {
      if (error instanceof Error) reason = error.message
    } finally {
      if (!signal.aborted) onEnd(reason)
    }
  }
  return {
    async connect() {
      const response = await request('/live')
      if (!response.body) throw new Error('实时事件流不可用')
      const frames = readFrames(response.body)
      const first = (await frames.next()).value
      if (!first || first.kind !== 'connected') {
        await frames.return(undefined)
        throw new Error('未收到 Session 订阅确认')
      }
      void observe(frames)
      return first.state
    },
    async history(after?: string) {
      const response = await request('/history' + (after ? '?' + new URLSearchParams({ cursor: after }) : ''))
      const page = await response.json() as { events: SessionEvent[]; next?: string }
      if (!page.events.every(matches)) throw new Error('历史包含其他 Session 的事件')
      return page
    },
    prompt: (value: string) => control({ action: 'prompt', text: value }),
    cancel: () => control({ action: 'cancel' }),
    answer: (interaction: string, answer: unknown) => control({ action: 'answer', interaction, answer }),
    close: () => controller.abort(),
  }
}
