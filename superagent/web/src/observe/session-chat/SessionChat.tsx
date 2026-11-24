import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowLeft, ArrowUp, ChevronDown, FlaskConical, Hash, Info, List, MessageSquare, Square, Terminal } from 'lucide-react'
import { Button } from '../../components/ui/button'
import { sessionChatDemoHref, useRecord, viewHref, type ViewProps } from '../observer'
import { demoBindings, demoSession } from './demo'
import { Conversation, EventRows, json } from './EventRows'
import { eventLabels, liveSession, mergeEvents, record, text, workLabels, type Binding, type SessionEvent } from './protocol'
import './session-chat.css'

const targetLabel = (role: string) => role === 'topic' ? 'Topic Session' : 'Worker Session'

export default function SessionChat({ query }: ViewProps) {
  const demo = import.meta.env.DEV && !query.has('chat')
  const role = query.get('chat') === 'issue' ? 'issue' : 'topic'
  const ownerID = query.get('id') || ''
  const owner = useRecord<{ title: string; sessionBindingId?: string; currentSessionBindingId?: string }>(
    !demo && ownerID ? `${role}s/${encodeURIComponent(ownerID)}` : '',
  )
  const bindingID = role === 'issue' ? owner.data?.currentSessionBindingId : owner.data?.sessionBindingId
  const binding = useRecord<Binding>(bindingID ? 'session-bindings/' + encodeURIComponent(bindingID) : '')
  const [demoIndex, setDemoIndex] = useState(0)
  const target = demo ? demoBindings[demoIndex] : binding.data
  const back = demo ? viewHref('topic-conversation') : viewHref(role === 'topic' ? 'topic-conversation' : 'issue', ownerID)
  const title = demo ? demoIndex === 0 ? '发布评审' : '检查 README' : owner.data?.title || target?.name
  const ready = target?.sessionId && target.sessionUid && target.state === 'ready' &&
    (demo || (target.ownerId === ownerID && target.role === role))
  const unavailable = owner.error || binding.error || (ownerID && (owner.loading || (bindingID && binding.loading))
    ? '正在读取 Session 绑定…' : '当前没有可用的 Session')
  return <div className="observer sc-observer">
    <header className="topbar">
      <div className="shell top-inner">
        <a href={back} className="brand"><ArrowLeft size={17}/> <strong>Superagent</strong><span className="brand-divider"/>Web</a>
        {demo ? <nav className="sc-selector" aria-label="演示目标">
          {demoBindings.map((entry, index) => <Button key={entry.id} size="sm" variant="ghost"
            aria-label={targetLabel(entry.role)} aria-pressed={demoIndex === index}
            onClick={() => setDemoIndex(index)}>{entry.role === 'topic' ? 'Topic' : 'Worker'}</Button>)}
        </nav> : import.meta.env.DEV && <div className="secondary"><a href={sessionChatDemoHref}><FlaskConical size={14}/> 交互演示</a></div>}
      </div>
    </header>
    <main className="shell sc-main">
      {target && ready ? <Chat key={`${target.id}:${target.sessionUid}`} binding={target} title={title || target.name} demo={demo}/>
        : <p className="panel sc-unavailable" role="status">{unavailable}</p>}
    </main>
  </div>
}

type Client = ReturnType<typeof liveSession>
type Demo = ReturnType<typeof demoSession>
type Action = 'prompt' | 'cancel' | 'answer'
type Receipt = { action: Action; value: unknown; failed?: boolean; draft?: string }
const receiptLabels: Record<string, string> = {
  applied_new_work: '已接纳为新一轮工作',
  applied_current_work: '已接纳到当前工作',
  applied_best_effort: '已尽力交付',
  not_applied: '输入未应用',
  controlAcknowledged: '已确认取消请求，等待状态更新',
  noActiveWork: '当前没有可取消的工作',
}
const actionLabels: Record<Action, string> = { prompt: '发送', cancel: '取消', answer: '回答' }

function Chat({ binding, title, demo }: { binding: Binding; title: string; demo: boolean }) {
  const [connection, setConnection] = useState<'connecting' | 'ready' | 'ended'>('connecting')
  const [reason, setReason] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [initialWork, setInitialWork] = useState('')
  const [history, setHistory] = useState<SessionEvent[]>([])
  const [live, setLive] = useState<SessionEvent[]>([])
  const [next, setNext] = useState<string>()
  const [historyError, setHistoryError] = useState('')
  const [historyLoaded, setHistoryLoaded] = useState(false)
  const [loading, setLoading] = useState(false)
  const [draft, setDraft] = useState('')
  const [raw, setRaw] = useState(false)
  const [receipt, setReceipt] = useState<Receipt>()
  const [pending, setPending] = useState<Record<string, boolean>>({})
  const [answered, setAnswered] = useState<Record<string, boolean>>({})
  const [cancelAcknowledgedAt, setCancelAcknowledgedAt] = useState<string>()
  const client = useRef<Client | Demo | null>(null)
  const viewport = useRef<HTMLDivElement>(null)
  const demoMenu = useRef<HTMLDetailsElement>(null)
  const follow = useRef(true)
  const locks = useRef(new Set<string>())
  const events = mergeEvents(history, live)
  const stateUpdate = [...live].reverse().find(event => event.type === 'session.state_update' && !event.replay)
  const work = stateUpdate ? text(stateUpdate.payload.state) : initialWork
  const ready = connection === 'ready'
  const workStateKey = stateUpdate?.eventId || `connection-${attempt}`
  const canCancel = ready && work === 'running' && !pending.cancel && cancelAcknowledgedAt !== workStateKey
  const answers = Object.fromEntries(events.filter(event => event.type === 'session.interaction_closed').map(event =>
    [text(event.payload.interaction), { closed: true, answered: false, outcome: text(event.payload.outcome) }]))
  for (const interaction of Object.keys({ ...answered, ...pending })) {
    if (!answers[interaction] && (answered[interaction] || pending[interaction])) {
      answers[interaction] = { answered: true, closed: false, outcome: '' }
    }
  }

  async function readHistory(current: Client | Demo, cursor?: string) {
    setLoading(true)
    setHistoryError('')
    try {
      const page = await current.history(cursor)
      if (client.current !== current) return
      setHistory(previous => cursor ? mergeEvents(previous, page.events) : page.events)
      setNext(page.next)
      setHistoryLoaded(true)
    } catch (error) {
      if (client.current === current) setHistoryError((error as Error).message)
    } finally {
      if (client.current === current) setLoading(false)
    }
  }

  useEffect(() => {
    setConnection('connecting')
    setReason('')
    setInitialWork('')
    setLive([])
    setPending({})
    setAnswered({})
    locks.current.clear()
    const receive = (event: SessionEvent) => setLive(previous => [...previous, event])
    const end = (reason: string) => {
      setReason(reason)
      setConnection('ended')
    }
    const current = demo ? demoSession(binding, receive, end) : liveSession(binding, receive, end)
    client.current = current
    current.connect().then(state => {
      if (client.current !== current) return
      setInitialWork(state)
      setConnection('ready')
      void readHistory(current)
    }).catch(error => {
      if (client.current !== current) return
      current.close()
      end(error.message)
    })
    return () => {
      client.current = null
      current.close()
    }
  }, [attempt, binding.id, binding.namespace, binding.name, binding.sessionId, binding.sessionUid, demo])

  useLayoutEffect(() => {
    const node = viewport.current
    if (node && follow.current) node.scrollTop = node.scrollHeight
  }, [history, live, raw])

  async function act(action: Action, value?: unknown, interaction?: string) {
    const current = client.current
    const key = interaction || action
    if (!current || !ready || locks.current.has(key) || (action === 'cancel' && !canCancel)) return
    locks.current.add(key)
    setPending(previous => ({ ...previous, [key]: true }))
    try {
      const result = action === 'prompt' ? await current.prompt(text(value))
        : action === 'cancel' ? await current.cancel()
        : await current.answer(interaction!, value)
      if (client.current !== current) return
      setReceipt({ action, value: result, draft: action === 'prompt' ? text(value) : undefined })
      if (action === 'cancel' && record(result).result === 'controlAcknowledged') setCancelAcknowledgedAt(workStateKey)
      if (action === 'prompt' && record(result).result !== 'not_applied') setDraft(previous => previous === value ? '' : previous)
      if (interaction) setAnswered(previous => ({ ...previous, [interaction]: true }))
    } catch (error) {
      if (client.current === current) setReceipt({ action, value: (error as Error).message, failed: true })
    } finally {
      if (client.current === current) {
        locks.current.delete(key)
        setPending(previous => ({ ...previous, [key]: false }))
      }
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (draft.trim()) void act('prompt', draft)
  }

  function showStream(enabled: boolean) {
    follow.current = !enabled
    if (enabled) viewport.current?.scrollTo({ top: 0 })
    setRaw(enabled)
  }

  const demoClient = demo ? client.current as Demo | null : null
  const result = text(record(receipt?.value).result)
  const workLabel = ready ? workLabels[work] || work || '工作状态未知' : connection === 'connecting' ? '正在连接' : '已断开'
  const receiptText = receipt && (receipt.failed ? text(receipt.value) : receiptLabels[result] || result || '已收到回执')
  const showReceipt = receipt && (receipt.failed || result === 'not_applied' || result === 'noActiveWork' ||
    (result === 'controlAcknowledged' && ready && work === 'running' && cancelAcknowledgedAt === workStateKey))
  const reply = { answers, allowAnswer: ready, answer: (interaction: string, value: unknown) => void act('answer', value, interaction) }

  return <section className="sc-chat" aria-label="Session 对话">
      <header className="sc-chat-header sc-width">
        <div className="sc-avatar">{binding.role === 'topic' ? <Hash size={19}/> : <Terminal size={18}/>}</div>
        <div className="sc-chat-title"><h1>{title}</h1><p>{targetLabel(binding.role)}</p></div>
        <div className="sc-header-actions">
          <Button size="sm" variant="ghost" aria-pressed={raw} aria-label={raw ? '返回对话' : '查看事件流'} onClick={() => showStream(!raw)}>
            {raw ? <MessageSquare size={15}/> : <List size={15}/>}<span>{raw ? '返回对话' : '事件流'}</span>
          </Button>
          {demo && <details className="sc-menu" name="session-chat-menu" ref={demoMenu}>
            <summary aria-label="演示场景" title="演示场景"><FlaskConical size={16}/></summary>
            <div className="sc-popover">
              <h2>演示场景</h2>
              <div className="sc-scenarios">
                <Button size="sm" variant="outline" disabled={!ready || work !== 'idle'} onClick={() => {
                  if (demoMenu.current) demoMenu.current.open = false
                  showStream(true)
                  demoClient?.publish()
                }}>查看全部事件 <small>{Object.keys(eventLabels).length} 类</small></Button>
                {Object.entries({ permission: '工具权限', form: 'Agent 提问', url: '外部流程' }).map(([kind, label]) =>
                  <Button key={kind} size="sm" variant="outline" disabled={!ready || work !== 'idle'} onClick={() => {
                    if (demoMenu.current) demoMenu.current.open = false
                    showStream(false)
                    demoClient?.request(kind)
                  }}>{label}</Button>)}
                <Button size="sm" variant="ghost" disabled={!ready} onClick={() => {
                  if (demoMenu.current) demoMenu.current.open = false
                  demoClient?.disconnect()
                }}>模拟断开连接</Button>
              </div>
            </div>
          </details>}
          <details className="sc-menu" name="session-chat-menu">
            <summary aria-label="Session 详情" title="Session 详情"><Info size={16}/></summary>
            <div className="sc-popover">
              <h2>Session 详情</h2>
              <dl>
                <div><dt>名称</dt><dd><code>{binding.name}</code></dd></div>
                <div><dt>Session ID</dt><dd><code>{binding.sessionId}</code></dd></div>
                <div><dt>连接</dt><dd>{demo ? '演示' : 'massd'} · {ready ? '已订阅' : connection === 'connecting' ? '正在连接' : '已断开'}</dd></div>
                <div><dt>工作状态</dt><dd>{workLabel}</dd></div>
                {!demo && <div><dt>历史</dt><dd>{historyError ? '读取失败' : loading ? '读取中' : next ? '还有下一页' : historyLoaded ? '已读取' : '未读取'}</dd></div>}
              </dl>
              {receipt && <details><summary>最近回执 · {actionLabels[receipt.action]}</summary>
                <p>{receiptText}</p>
                {!receipt.failed && <>{receipt.draft && <pre>{receipt.draft}</pre>}<pre>{json(receipt.value)}</pre></>}
              </details>}
            </div>
          </details>
        </div>
      </header>
      <div className="sc-viewport" ref={viewport} aria-label={raw ? '事件流' : '对话记录'} tabIndex={0} onScroll={event => {
        const node = event.currentTarget
        follow.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40
      }}>
        <div className="sc-width sc-scroll-body">
          {raw && <div className="sc-stream-title"><h2>事件流</h2><span>{events.length} 条事件</span></div>}
          {historyError && <p className="sc-error" role="alert">历史读取失败 · {historyError}</p>}
          {events.length === 0 && <div className="sc-empty">
            <MessageSquare size={24}/><strong>{loading ? '正在读取历史' : raw ? '暂无事件' : '开始对话'}</strong>
            {!loading && !raw && <p>发一条消息给 {targetLabel(binding.role)}。</p>}
          </div>}
          {raw ? <EventRows events={events} {...reply}/> : <Conversation events={events} running={ready && work === 'running'} {...reply}/>}
          {next && <div className="sc-history-more"><Button size="sm" variant="ghost" disabled={loading || !ready}
            onClick={() => { if (client.current) void readHistory(client.current, next) }}><ChevronDown size={14}/> 继续读取历史</Button></div>}
        </div>
      </div>
      <div className="sc-width sc-dock">
        {reason && <div className="sc-feedback" role="status"><span className="sc-error">{reason}</span>
          {connection === 'ended' && <Button size="sm" variant="ghost" onClick={() => setAttempt(previous => previous + 1)}>重新连接</Button>}
        </div>}
        {showReceipt &&
          <p className={receipt.failed || result === 'not_applied' ? 'sc-error' : 'sc-subtle'} role="status">{receiptText}</p>}
        <form className="sc-composer" onSubmit={submit}>
          <textarea rows={2} aria-label="消息" placeholder={`发给 ${targetLabel(binding.role)}…`} value={draft} onChange={event => setDraft(event.target.value)}
            onKeyDown={event => {
              if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault()
                event.currentTarget.form?.requestSubmit()
              }
            }}/>
          <div className="sc-composer-footer">
            <span className="sc-status" data-state={ready ? work : connection}><i/>{ready && pending.prompt && ['offline', 'closing'].includes(work) ? '正在恢复 Session' : workLabel}</span>
            <div className="sc-actions">
              <Button type="button" className="sc-cancel" variant="destructive" size="sm" disabled={!canCancel} onClick={() => void act('cancel')}><Square size={13}/> 取消工作</Button>
              <Button type="submit" className="sc-send" size="icon-sm" aria-label="发送" title="发送" disabled={!ready || pending.prompt || !draft.trim()}><ArrowUp size={17}/></Button>
            </div>
          </div>
        </form>
        <div className="sc-composer-hint">{demo && <span>原型演示</span>}<span>Enter 发送 · Shift + Enter 换行</span></div>
      </div>
  </section>
}
