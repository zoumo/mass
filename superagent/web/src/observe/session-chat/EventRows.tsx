import { useState } from 'react'
import { Button } from '../../components/ui/button'
import { conversationRows, eventLabels, list, record, text, workLabels, type SessionEvent } from './protocol'

const clock = new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium', hourCycle: 'h23' })
const imageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/avif']
const audioTypes = ['audio/wav', 'audio/mpeg', 'audio/ogg', 'audio/mp4', 'audio/webm', 'audio/flac']
const stopLabels: Record<string, string> = { end_turn: '本轮结束', completed: '本轮结束', cancelled: '已取消', interrupted: '已中断' }
const outcomeLabels: Record<string, string> = { answered: '已回答', completed: '已完成', cancelled: '已取消', expired: '已过期', failed: '失败' }
const toolLabels: Record<string, string> = { pending: '待执行', in_progress: '执行中', completed: '已完成', failed: '失败', declined: '已拒绝' }
const phaseLabels: Record<string, string> = { started: '已开始', native_completed: '原生回放结束', failed: '失败', canceled: '已取消' }

export const json = (value: unknown) => JSON.stringify(value, null, 2)
const label = (dictionary: Record<string, string>, value: unknown) => dictionary[text(value)] || text(value)

function safeURL(value: unknown) {
  try {
    const url = new URL(text(value))
    if (url.protocol === 'https:' || url.protocol === 'http:') return url.href
  } catch {
    return undefined
  }
}

function Content({ value }: { value: unknown }) {
  return list(value).map((block, index) => {
    const mimeType = text(block.mimeType)
    if (block.type === 'text') return <span className="sc-text" key={index}>{text(block.text)}</span>
    if (block.type === 'image' && imageTypes.includes(mimeType)) return <img key={index} alt="消息图片" src={`data:${mimeType};base64,${text(block.data)}`} />
    if (block.type === 'audio' && audioTypes.includes(mimeType)) return <audio key={index} controls preload="none" src={`data:${mimeType};base64,${text(block.data)}`} />
    if (block.type === 'resource_link') {
      const href = safeURL(block.uri)
      return <p key={index}>{href ? <a href={href} target="_blank" rel="noreferrer">{text(block.title || block.name)}</a> : <code>{text(block.name)} · {text(block.uri)}</code>}</p>
    }
    if (block.type === 'resource') {
      const resource = record(block.resource)
      return <details key={index}><summary>{text(resource.uri)}</summary>
        {typeof resource.text === 'string' ? <pre>{resource.text}</pre> : <p>内嵌二进制资源 · {text(resource.mimeType)}</p>}
      </details>
    }
    return <details key={index}><summary>{text(block.type) || '内容块'} {mimeType}</summary><pre>{json(block)}</pre></details>
  })
}

export function RawEvents({ events }: { events: SessionEvent[] }) {
  return <ol className="sc-raw">
    {events.map(event => <li key={event.eventId} data-event-type={event.type}>
      <details>
        <summary><time dateTime={event.time}>{clock.format(new Date(event.time))}</time> <code>{event.type}</code>{event.replay && ' · 回放'}</summary>
        <pre>{json(event)}</pre>
      </details>
    </li>)}
  </ol>
}

function Interaction({ event, enabled, answered, answer }: { event: SessionEvent; enabled: boolean; answered: boolean; answer: (value: unknown) => void }) {
  const request = record(event.payload.request)
  const [form, setForm] = useState('{}')
  const [error, setError] = useState('')
  const disabled = !enabled || answered
  const url = safeURL(request.url)
  return <div className="sc-interaction">
    <strong>{text(request.title || request.message) || '等待交互'}</strong>
    {event.payload.blocking === false && <small> · 非阻塞</small>}
    <Content value={request.content}/>
    {request.kind === 'permission' && <div className="sc-actions">
      {list(request.options).map(option => <Button key={text(option.optionId)} variant="outline" size="sm" disabled={disabled}
        onClick={() => answer({ outcome: 'selected', optionId: option.optionId })}>{text(option.name)}</Button>)}
    </div>}
    {request.kind === 'form' && <>
      <details><summary>查看表单 Schema</summary><pre>{json(request.requestedSchema)}</pre></details>
      <textarea aria-label="表单 JSON" rows={3} value={form} disabled={disabled} onChange={event => setForm(event.target.value)}/>
    </>}
    {request.kind === 'url' && <p>{url ? <a href={url} rel="noreferrer" target="_blank">打开外部流程 ↗</a> : <code>{text(request.url)}</code>}</p>}
    {(request.kind === 'form' || request.kind === 'url') && <div className="sc-actions">
      <Button size="sm" disabled={disabled} onClick={() => {
        try {
          const value = request.kind === 'form' ? { action: 'accept', content: JSON.parse(form) } : { action: 'accept' }
          setError('')
          answer(value)
        } catch {
          setError('表单内容需要是有效 JSON')
        }
      }}>提交</Button>
      <Button size="sm" variant="outline" disabled={disabled} onClick={() => answer({ action: 'decline' })}>拒绝</Button>
      <Button size="sm" variant="ghost" disabled={disabled} onClick={() => answer({ action: 'cancel' })}>取消交互</Button>
    </div>}
    {error && <p role="alert">{error}</p>}
    {answered && <small>已提交回答，等待原生结算</small>}
  </div>
}

type InteractionState = { answered: boolean; closed: boolean }

export function EventRows({ events, allowAnswer, answers, answer }: {
  events: SessionEvent[]
  allowAnswer: boolean
  answers: Record<string, InteractionState>
  answer: (interaction: string, value: unknown) => void
}) {
  return <ol className="sc-events">
    {conversationRows(events).map(group => {
      const first = group[0]
      const item = group.filter(event => !event.type.endsWith('_chunk')).at(-1) || first
      const { type, payload } = item
      let body
      let title = eventLabels[type] || type
      let kind = 'system'
      if (type === 'user.message' || type.startsWith('agent.message') || type.startsWith('agent.thinking')) {
        kind = type === 'user.message' ? 'user' : type.startsWith('agent.thinking') ? 'thinking' : 'agent'
        const content = type.endsWith('_chunk') ? group.flatMap(event => list(event.payload.content)) : payload.content
        body = kind === 'thinking' ? <details><summary>查看思考内容</summary><Content value={content}/></details> : <Content value={content}/>
      } else if (type.startsWith('agent.tool_')) {
        kind = 'tool'
        const call = group.find(event => event.type === 'agent.tool_call')
        const result = group.filter(event => event.type === 'agent.tool_result').at(-1)
        const chunks = group.filter(event => event.type === 'agent.tool_call_chunk')
        const name = text(call?.payload.title || payload.title || payload.toolName || payload.id)
        title = `${name} · ${result ? label(toolLabels, result.payload.status) : label(toolLabels, payload.status) || title}`
        body = <details><summary>查看工具详情</summary>
          {call && <><span>{text(call.payload.provider)} · {text(call.payload.toolName)}</span><pre>{json(call.payload.rawInput)}</pre><Content value={call.payload.content}/></>}
          {chunks.length > 0 && <div><small>片段</small><div><Content value={chunks.flatMap(row => list(row.payload.content))}/></div></div>}
          {result && <><Content value={result.payload.content}/><pre>{json(result.payload.rawOutput)}</pre>
            {!!result.payload.output && <pre>{json(result.payload.output)}</pre>}
            {result.payload.exitCode != null && <small>退出码 {String(result.payload.exitCode)}</small>}
          </>}
        </details>
      } else if (type === 'session.interaction_requested') {
        kind = 'interaction'
        const interaction = text(payload.interaction)
        const state = answers[interaction]
        body = <Interaction event={item} answered={!!state?.answered && !state.closed} enabled={allowAnswer && !state?.closed && !item.replay}
          answer={value => answer(interaction, value)}/>
      } else {
        switch (type) {
          case 'session.interaction_closed':
            body = [label(outcomeLabels, payload.outcome), text(payload.reason)].filter(Boolean).join(' · ')
            break
          case 'session.state_update':
            body = [label(workLabels, payload.state) || '未知', label(stopLabels, payload.stopReason), text(record(payload.error).message)].filter(Boolean).join(' · ')
            break
          case 'session.info_update':
            body = [text(payload.title), text(payload.updatedAt)].filter(Boolean).join(' · ')
            break
          case 'session.error':
            kind = 'error'
            body = [text(payload.message), text(payload.reason)].filter(Boolean).join(' · ')
            break
          case 'session.history_replay':
            body = [label(phaseLabels, payload.phase), text(payload.retainedScope), text(record(payload.error).message)].filter(Boolean).join(' · ')
            break
          case 'session.usage_update':
            body = `${String(payload.used)} / ${String(payload.size)} tokens${payload.cost ? ` · ${String(record(payload.cost).amount)} ${text(record(payload.cost).currency)}` : ''}`
            break
          case 'session.available_commands_update':
            body = list(payload.availableCommands).map(command => <p key={text(command.name)}><code>{text(command.name)}</code> {text(command.description)}</p>)
            break
          case 'session.config_option_update':
            body = <details><summary>{list(payload.configOptions).length} 项配置</summary><pre>{json(payload.configOptions)}</pre></details>
            break
          default:
            body = <pre>{json(payload)}</pre>
        }
      }
      return <li key={first.eventId} data-kind={kind}>
        <div className="sc-row-heading"><strong>{title}</strong><time dateTime={first.time}>{clock.format(new Date(first.time))}</time>{first.replay && <small>回放</small>}</div>
        <div>{body}</div>
      </li>
    })}
  </ol>
}
