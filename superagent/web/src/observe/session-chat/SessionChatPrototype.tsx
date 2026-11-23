import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowLeft, ArrowUp, Cable, Hash, List, Radio, Square, Terminal } from 'lucide-react'
import { Button } from '../../components/ui/button'
import { sessionChatDemoHref, useRecord, viewHref, type ViewProps } from '../observer'
import { demoBindings, demoSession } from './demo'
import { EventRows, RawEvents, json } from './EventRows'
import { eventLabels, liveSession, mergeEvents, record, text, workLabels, type Binding, type SessionEvent } from './protocol'
import './session-chat.css'

const targetLabel = (role: string) => role === 'topic' ? 'Topic Session' : 'Worker Session'

export default function SessionChatPrototype({ query }: ViewProps) {
  const id = query.get('binding') || ''
  const binding = useRecord<Binding>(id ? 'session-bindings/' + encodeURIComponent(id) : '')
  const role = binding.data?.role === 'issue' ? 'issue' : 'topic'
  const owner = useRecord<{ title: string; topicId?: string }>(binding.data?.ownerId ? `${role}s/${encodeURIComponent(binding.data.ownerId)}` : '')
  const [demoIndex, setDemoIndex] = useState(0)
  const target = id ? binding.data : demoBindings[demoIndex]
  const back = id && target ? viewHref(role === 'topic' ? 'topic-conversation' : 'issue', target.ownerId) : viewHref('topic-conversation')
  const title = id ? owner.data?.title || target?.name : demoIndex === 0 ? '发布评审' : '检查 README'
  return <div className="observer">
    <header className="topbar">
      <div className="shell top-inner">
        <a href={back} className="brand"><ArrowLeft size={17}/> <strong>Superagent</strong><span className="brand-divider"/>Web</a>
        <div className="secondary"><a href={sessionChatDemoHref}><Terminal size={14}/> 交互演示</a><span>开发原型</span></div>
      </div>
    </header>
    <main className="shell sc-main">
      <div className="sc-page-title">
        <div>
          <p className="sc-subtle">{id ? 'massd · 实时连接' : 'massd · 示例事件'}</p>
          <h1>Session 对话</h1>
        </div>
        {!id && <nav className="sc-selector" aria-label="演示目标">
          {demoBindings.map((entry, index) => <Button key={entry.id} size="sm" variant={demoIndex === index ? 'secondary' : 'ghost'}
            aria-pressed={demoIndex === index} onClick={() => setDemoIndex(index)}>{targetLabel(entry.role)}</Button>)}
        </nav>}
      </div>
      {!target && <p className="panel sc-unavailable" role="status">{binding.error || '正在读取 Session 绑定…'}</p>}
      {target && (!target.sessionId || !target.sessionUid || !['topic', 'issue'].includes(target.role) || target.state !== 'ready') &&
        <p className="panel sc-unavailable" role="status">此绑定当前无法发起对话 · {target.state}</p>}
      {target?.sessionId && target.sessionUid && ['topic', 'issue'].includes(target.role) && target.state === 'ready' &&
        <Chat key={`${target.id}:${target.sessionUid}`} binding={target} title={title || target.name} demo={!id}/>}
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
  const client = useRef<Client | Demo | null>(null)
  const viewport = useRef<HTMLDivElement>(null)
  const follow = useRef(true)
  const locks = useRef(new Set<string>())
  const events = mergeEvents(history, live)
  const stateUpdate = [...live].reverse().find(event => event.type === 'session.state_update' && !event.replay)
  const work = stateUpdate ? text(stateUpdate.payload.state) : initialWork
  const answers = Object.fromEntries(live.filter(event => event.type === 'session.interaction_closed').map(event =>
    [text(event.payload.interaction), { closed: true, answered: false }]))
  for (const interaction of Object.keys({ ...answered, ...pending })) {
    if (!answers[interaction] && (answered[interaction] || pending[interaction])) answers[interaction] = { answered: true, closed: false }
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
    if (!current || connection !== 'ready' || locks.current.has(key)) return
    locks.current.add(key)
    setPending(previous => ({ ...previous, [key]: true }))
    try {
      const result = action === 'prompt' ? await current.prompt(text(value))
        : action === 'cancel' ? await current.cancel()
        : await current.answer(interaction!, value)
      if (client.current !== current) return
      setReceipt({ action, value: result, draft: action === 'prompt' ? text(value) : undefined })
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

  const demoClient = demo ? client.current as Demo | null : null
  const ready = connection === 'ready'
  const result = text(record(receipt?.value).result)
  return <div className="sc-workspace">
    <section className="panel sc-chat" aria-label="Session 对话">
      <header className="sc-chat-header">
        <div className="sc-avatar">{binding.role === 'topic' ? <Hash size={22}/> : <Terminal size={20}/>}</div>
        <div className="sc-chat-title"><h2>{title}</h2><p>{targetLabel(binding.role)} <code>{binding.name}</code></p></div>
        <span className="sc-status" data-ready={ready}><Radio size={14}/>{ready ? '已订阅' : connection === 'connecting' ? '正在连接' : '已断开'}</span>
      </header>
      <div className="sc-toolbar">
        <span>{raw ? '事件流' : '对话'} <small>{events.length} 条事件</small></span>
        <Button size="sm" variant="ghost" aria-pressed={raw} onClick={() => setRaw(previous => !previous)}><List size={15}/>{raw ? '显示对话' : '查看原始事件'}</Button>
      </div>
      <div className="sc-viewport" ref={viewport} aria-label={raw ? '原始事件' : '对话记录'} tabIndex={0} onScroll={event => {
        const node = event.currentTarget
        follow.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40
      }}>
        {next && <div className="sc-actions"><Button size="sm" variant="outline" disabled={loading || !ready}
          onClick={() => { if (client.current) void readHistory(client.current, next) }}>继续读取历史</Button></div>}
        {historyError && <p className="sc-error" role="alert">历史读取失败 · {historyError}</p>}
        {events.length === 0 && <div className="sc-empty">
          <Terminal size={28}/><strong>{loading ? '正在读取历史' : '暂无对话事件'}</strong>
          <p>{demo ? '发送一条消息，或从右侧运行一个事件场景。' : '连接就绪后发送消息，实时回复会显示在这里。'}</p>
        </div>}
        {raw ? <RawEvents events={events}/> : <EventRows events={events} answers={answers} allowAnswer={ready}
          answer={(interaction, value) => void act('answer', value, interaction)}/>}
      </div>
      <form className="sc-composer" onSubmit={submit}>
        <textarea rows={2} aria-label="消息" placeholder={`发给 ${targetLabel(binding.role)}…`} value={draft} onChange={event => setDraft(event.target.value)}
          onKeyDown={event => {
            if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); event.currentTarget.form?.requestSubmit() }
          }}/>
        <div className="sc-composer-footer">
          <small>⌘ / Ctrl + Enter 发送</small>
          <div className="sc-actions">
            <Button type="button" variant="outline" size="sm" disabled={!ready || pending.cancel} onClick={() => void act('cancel')}><Square size={13}/> 取消工作</Button>
            <Button type="submit" size="sm" disabled={!ready || pending.prompt || !draft.trim()}><ArrowUp size={16}/> 发送</Button>
          </div>
        </div>
      </form>
    </section>
    <aside className="sc-sidebar" aria-label="Session 状态">
      <section className="panel sc-aside">
        <h2><Cable size={16}/> Session 状态</h2>
        <dl>
          <div><dt>目标</dt><dd>{targetLabel(binding.role)}</dd></div>
          <div><dt>Session ID</dt><dd><code>{binding.sessionId}</code></dd></div>
          <div><dt>工作状态</dt><dd>{ready ? workLabels[work] || work || '未知' : '未知'}</dd></div>
          {!demo && <div><dt>历史</dt><dd>{historyError ? '读取失败' : loading ? '读取中' : next ? '还有下一页' : historyLoaded ? '已读取' : '未读取'}</dd></div>}
        </dl>
        {reason && <p className="sc-error" role="status">{reason}。期间的实时事件可能缺失。</p>}
        {connection === 'ended' && <Button size="sm" variant="outline" onClick={() => setAttempt(previous => previous + 1)}>重新连接</Button>}
      </section>
      <section className="panel sc-aside" aria-label="最近回执">
        <h2>最近回执</h2>
        <div role="status">
          {receipt ? <>
            <p className={receipt.failed ? 'sc-error' : undefined}>{actionLabels[receipt.action]} · {receipt.failed ? text(receipt.value) : receiptLabels[result] || result || '已收到回执'}</p>
            {!receipt.failed && <details><summary>回执详情</summary>{receipt.draft && <pre>{receipt.draft}</pre>}<pre>{json(receipt.value)}</pre></details>}
          </> : <p className="sc-subtle">等待操作</p>}
        </div>
      </section>
      {demo && <section className="panel sc-aside" aria-label="演示场景">
        <h2>演示场景</h2>
        <div className="sc-scenarios">
          <Button size="sm" variant="outline" disabled={!ready || work !== 'idle'} onClick={() => { demoClient?.publish(); setRaw(true) }}>
            查看全部事件 <small>{Object.keys(eventLabels).length} 类</small>
          </Button>
          {Object.entries({ permission: '工具权限', form: 'Agent 提问', url: '外部流程' }).map(([kind, label]) =>
            <Button key={kind} size="sm" variant="outline" disabled={!ready || work !== 'idle'} onClick={() => { setRaw(false); demoClient?.request(kind) }}>{label}</Button>)}
          <Button size="sm" variant="ghost" disabled={!ready} onClick={() => demoClient?.disconnect()}>模拟断开连接</Button>
        </div>
      </section>}
    </aside>
  </div>
}
