// Throwaway Web prototype. The transport speaks the existing MASS JSON-RPC v1 profile.
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
  'session.info_update': 'Session 信息',
  'session.config_option_update': '配置快照',
  'session.available_commands_update': '可用命令',
  'session.usage_update': '用量',
  'session.history_replay': '历史回放',
  'session.interaction_requested': '交互请求',
  'session.interaction_closed': '交互结算',
}

export const workLabels: Record<string, string> = { idle: '空闲', running: '运行中', requires_action: '等待交互' }
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

type Notice = { method: string; params: { subscriptionId: string; event?: SessionEvent; error?: { message: string } } }

class RpcSocket {
  readonly socket: WebSocket
  private pending = new Map<number, { resolve(value: unknown): void; reject(error: Error): void }>()
  private counter = 0

  constructor(path: string, notice: (message: Notice) => void, ended: () => void) {
    this.socket = new WebSocket(new URL(path, location.origin.replace(/^http/, 'ws')))
    this.socket.onmessage = ({ data }) => {
      const message = JSON.parse(data)
      if (message.method) { notice(message); return }
      const request = this.pending.get(message.id)
      if (!request) return
      this.pending.delete(message.id)
      if (message.error) request.reject(new Error(message.error.data?.message || message.error.message))
      else request.resolve(message.result)
    }
    this.socket.onclose = () => {
      for (const request of this.pending.values()) request.reject(new Error('连接中断，未收到回执'))
      this.pending.clear()
      ended()
    }
  }

  async initialize() {
    await new Promise<void>((resolve, reject) => {
      this.socket.addEventListener('open', () => resolve(), { once: true })
      this.socket.addEventListener('close', () => reject(new Error('massd WebSocket 无法连接')), { once: true })
    })
    const result = await this.call<{ protocolVersion: string }>('initialize', { protocolVersion: '1' })
    if (result.protocolVersion !== '1') throw new Error('massd 协议版本不匹配')
  }

  call<T>(method: string, params: Record<string, unknown>): Promise<T> {
    if (this.socket.readyState !== WebSocket.OPEN) return Promise.reject(new Error('massd 连接已关闭'))
    const id = ++this.counter
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: value => resolve(value as T), reject })
      this.socket.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
    })
  }
}

export function liveSession(binding: Binding, onEvent: (event: SessionEvent) => void, onEnd: (reason: string) => void) {
  const session = { sessionId: binding.sessionId }
  const params = { session }
  const path = '/__prototype/massd-chat'
  let subscriptionId = ''
  let stopped = false
  const close = () => {
    stopped = true
    control.socket.close()
    observation.socket.close()
  }
  const end = (reason: string) => {
    if (stopped) return
    close()
    onEnd(reason)
  }
  const matches = (e: SessionEvent) => e.sessionId === binding.sessionId && e.namespace === binding.namespace && e.sessionName === binding.name
  const control = new RpcSocket(path, () => {}, () => end('控制连接已断开'))
  const observation = new RpcSocket(path, ({ method, params }) => {
    if (params.subscriptionId !== subscriptionId) return
    if (method === 'subscription.closed') end(params.error?.message || '订阅已结束')
    if (method === 'session.event' && params.event) {
      if (matches(params.event)) onEvent(params.event)
      else end('收到其他 Session 的事件，已关闭连接')
    }
  }, () => end('事件流已中断'))
  async function verify() {
    const result = await control.call<{ metadata: { uid: string; namespace: string; name: string }; status: { sessionId: string; state: string } }>('session.get', params)
    const { metadata, status } = result
    if (metadata.uid !== binding.sessionUid || metadata.namespace !== binding.namespace || metadata.name !== binding.name || status.sessionId !== binding.sessionId) {
      throw new Error('Core 绑定与 massd Session 身份不一致')
    }
    return status.state
  }
  return {
    async connect() {
      await Promise.all([control.initialize(), observation.initialize()])
      await verify()
      const result = await observation.call<{ subscriptionId: string }>('session.subscribe', params)
      subscriptionId = result.subscriptionId
      return verify()
    },
    async history(after?: string) {
      const page = await control.call<{ events: SessionEvent[]; next?: string }>('session.history', { ...params, query: { limit: 100, ...(after ? { after } : {}) } })
      if (!page.events.every(matches)) throw new Error('历史包含其他 Session 的事件')
      return page
    },
    async prompt(value: string) {
      await verify()
      return control.call('session.prompt', { ...params, input: { content: [{ type: 'text', text: value }] } })
    },
    async cancel() {
      await verify()
      return control.call('session.cancel', params)
    },
    async answer(interaction: string, answer: unknown) {
      await verify()
      return control.call('session.answer', { ...params, interaction, answer })
    },
    close,
  }
}
