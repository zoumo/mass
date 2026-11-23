import { useEffect, useRef, useState } from 'react'
import { readObserver } from '../observer'
import { liveConnected, liveEnded, measureView, observerRequest } from '../telemetry'

type Item = { id: string; at: string; type: string; text?: string; truncated?: boolean; replayed?: boolean }
type HistoryPage = { events: Item[]; nextCursor?: string; observedAt: string }
type Frame = { kind: string; event?: Item }

function Events({ events }: { events: Item[] }) {
  return (
    <ol>
      {events.map((e) => (
        <li key={e.id}>
          <time dateTime={e.at}>{e.at}</time> · {e.type}
          {e.replayed && ' · 回放'}
          {e.text && <pre>{e.text}</pre>}
          {e.truncated && '预览已截断'}
        </li>
      ))}
    </ol>
  )
}
export default function SessionPreview({ sessionBindingId }: { sessionBindingId: string }) {
  const path = 'session-bindings/' + encodeURIComponent(sessionBindingId)
  const [history, setHistory] = useState<Item[]>([])
  const [live, setLive] = useState<Item[]>([])
  const [next, setNext] = useState('')
  const [observed, setObserved] = useState('')
  const [error, setError] = useState(false)
  const [busy, setBusy] = useState(false)
  const [connection, setConnection] = useState('正在连接')
  const [gap, setGap] = useState(false)
  const refresh = useRef<(append?: boolean) => void>(() => {})
  useEffect(() => {
    let stopped = false
    let pageCount = 1,
      continuation = ''
    let historyRequest: AbortController | undefined, stream: AbortController | undefined
    let retry: ReturnType<typeof setTimeout>, hint: ReturnType<typeof setTimeout> | undefined
    async function read(append = false) {
      if (append && !continuation) return
      historyRequest?.abort()
      const controller = new AbortController()
      historyRequest = controller
      const { signal } = controller
      let cursor = append ? continuation : ''
      const rows: Item[] = []
      setBusy(true)
      try {
        let at = ''
        await measureView('diagnostics', signal, async parent => {
          for (let i = 0; i < (append ? 1 : pageCount); i++) {
            const page = await readObserver<HistoryPage>(
              path + '/history?' + new URLSearchParams(cursor ? { cursor } : {}),
              signal,
              parent,
            )
            rows.push(...page.events)
            cursor = page.nextCursor || ''
            at = page.observedAt
            if (!cursor) break
          }
        })
        if (signal.aborted) return
        setHistory((previous) => (append ? [...previous, ...rows] : rows))
        if (append) pageCount++
        continuation = cursor
        setNext(cursor)
        setObserved(at)
        setError(false)
      } catch {
        if (!signal.aborted) setError(true)
      } finally {
        if (!signal.aborted) setBusy(false)
      }
    }
    function disconnect() {
      clearTimeout(retry)
      stream?.abort()
      setConnection('连接已断开')
      setGap(true)
      setLive([])
    }
    function receive(frame: Frame) {
      if (frame.kind === 'connected') setConnection('实时连接')
      if ((frame.kind === 'connected' || frame.kind === 'history_changed') && !hint) {
        hint = setTimeout(() => {
          hint = undefined
          void read()
        }, 300)
      }
      if (frame.kind === 'preview' && frame.event) {
        const event = frame.event
        setLive((previous) => [...previous.filter((e) => e.id !== event.id).slice(-19), event])
      }
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
      setConnection('正在连接')
      let connected = false, failed = false
      try {
        const reader = await observerRequest(path + '/live', signal, response => {
          if (!response.ok || !response.body) throw new Error('实时观测不可用')
          return response.body.pipeThrough(new TextDecoderStream()).getReader()
        })
        connected = true
        liveConnected()
        try {
          let partial = ''
          while (!signal.aborted) {
            const { done, value } = await reader.read()
            if (done || signal.aborted) break
            const lines = (partial + value).split('\n')
            partial = lines.pop() || ''
            for (const line of lines) if (line) receive(JSON.parse(line) as Frame)
          }
        } finally {
          await reader.cancel().catch(() => undefined)
          reader.releaseLock()
        }
      } catch {
        failed = true
        /* Reconnection rereads the saved history. */
      } finally {
        liveEnded(connected, signal.aborted ? 'canceled' : failed ? 'error' : 'ok')
        if (!stopped && !signal.aborted) {
          disconnect()
          if (navigator.onLine) retry = setTimeout(connect, 2000)
        }
      }
    }
    refresh.current = read
    void read()
    void connect()
    window.addEventListener('online', connect)
    window.addEventListener('offline', disconnect)
    return () => {
      stopped = true
      historyRequest?.abort()
      stream?.abort()
      clearTimeout(retry)
      clearTimeout(hint)
      window.removeEventListener('online', connect)
      window.removeEventListener('offline', disconnect)
    }
  }, [path])
  return (
    <>
      <section aria-label="Session 历史">
        <h2>Session 历史</h2>
        {observed && <p>读取时间 {observed}</p>}
        {error && <p role="status">无法读取历史。当前显示上次成功读取的记录。</p>}
        {observed && !history.length && <p>此 Session 暂无已保存的事件。</p>}
        <Events events={history} />
        {next && (
          <button disabled={busy} onClick={() => refresh.current(true)}>
            加载更多 Session 历史
          </button>
        )}
        <button disabled={busy} onClick={() => refresh.current()}>
          刷新 Session 历史
        </button>
      </section>
      <section aria-label="Session 实时预览">
        <h2>Session 实时预览</h2>
        <span>{connection}</span>
        {gap && <p role="status">观测中断 · 断开期间的活动可能缺失。重新连接后会再次读取历史。</p>}
        <Events events={live} />
      </section>
    </>
  )
}
