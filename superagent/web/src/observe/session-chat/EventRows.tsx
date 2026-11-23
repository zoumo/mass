import { useState } from 'react'
import {
  Activity, ArrowRight, Bot, Brain, CircleAlert, CircleCheck, Clock3, Gauge, History, Info, ListTree,
  MessageSquareDashed, Radio, ShieldQuestion, SlidersHorizontal, SquareTerminal, Terminal, UserRound, Workflow, type LucideIcon,
} from 'lucide-react'
import { Button } from '../../components/ui/button'
import { conversationRows, eventLabels, list, record, text, workLabels, type SessionEvent } from './protocol'

const clock = new Intl.DateTimeFormat('zh-CN', { timeStyle: 'medium', hourCycle: 'h23' })
const imageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/avif']
const audioTypes = ['audio/wav', 'audio/mpeg', 'audio/ogg', 'audio/mp4', 'audio/webm', 'audio/flac']
const stopLabels: Record<string, string> = { end_turn: '本轮结束', completed: '本轮结束', cancelled: '已取消', interrupted: '已中断' }
const outcomeLabels: Record<string, string> = { answered: '已回答', completed: '已完成', cancelled: '已取消', expired: '已过期', failed: '失败' }
const toolLabels: Record<string, string> = { pending: '待执行', in_progress: '执行中', completed: '已完成', failed: '失败', declined: '已拒绝' }
const phaseLabels: Record<string, string> = { started: '已开始', native_completed: '原生回放结束', failed: '失败', canceled: '已取消' }

const eventStyles: Record<string, { icon: LucideIcon; tone: string }> = {
  'user.message': { icon: UserRound, tone: 'user' },
  'agent.message': { icon: Bot, tone: 'reply' },
  'agent.message_chunk': { icon: MessageSquareDashed, tone: 'reply-stream' },
  'agent.thinking': { icon: Brain, tone: 'thinking' },
  'agent.thinking_chunk': { icon: Radio, tone: 'thinking-stream' },
  'agent.tool_call': { icon: SquareTerminal, tone: 'tool-call' },
  'agent.tool_call_chunk': { icon: Terminal, tone: 'tool-stream' },
  'agent.tool_result': { icon: CircleCheck, tone: 'tool-result' },
  'session.state_update': { icon: Activity, tone: 'state' },
  'session.error': { icon: CircleAlert, tone: 'error' },
  'session.info_update': { icon: Info, tone: 'info' },
  'session.config_option_update': { icon: SlidersHorizontal, tone: 'config' },
  'session.available_commands_update': { icon: ListTree, tone: 'commands' },
  'session.usage_update': { icon: Gauge, tone: 'usage' },
  'session.history_replay': { icon: History, tone: 'replay' },
  'session.interaction_requested': { icon: ShieldQuestion, tone: 'interaction' },
  'session.interaction_closed': { icon: Workflow, tone: 'settled' },
}

export const json = (value: unknown) => JSON.stringify(value, null, 2)
const label = (dictionary: Record<string, string>, value: unknown) => dictionary[text(value)] || text(value)
const configValue = (value: unknown) => typeof value === 'boolean' ? (value ? '开启' : '关闭') : text(value)
const stateName = (value: unknown) => label(workLabels, value) || '未知'

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

function Interaction({ event, enabled, answered, answer }: { event: SessionEvent; enabled: boolean; answered: boolean; answer: (value: unknown) => void }) {
  const request = record(event.payload.request)
  const [form, setForm] = useState('{}')
  const [error, setError] = useState('')
  const disabled = !enabled || answered
  const url = safeURL(request.url)
  return <div className="sc-interaction">
    <div className="sc-event-inline">
      <span className="sc-event-chip">{label({ permission: '权限', form: '表单', url: '外部流程' }, request.kind)}</span>
      {event.payload.blocking !== false && <small>等待处理时暂停工作</small>}
    </div>
    <p className="sc-event-title">{text(request.title || request.message) || '等待交互'}</p>
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

export function EventRows({ events, raw, allowAnswer, answers, answer }: {
  events: SessionEvent[]
  raw: boolean
  allowAnswer: boolean
  answers: Record<string, InteractionState>
  answer: (interaction: string, value: unknown) => void
}) {
  const rows = raw ? events.map(event => [event]) : conversationRows(events)
  return <ol className="sc-events" data-mode={raw ? 'stream' : 'chat'}>
    {rows.map(group => {
      const first = group[0]
      const item = group.filter(event => !event.type.endsWith('_chunk')).at(-1) || first
      const { type, payload } = item
      const { icon: Icon, tone } = eventStyles[type] || { icon: Info, tone: 'info' }
      let body
      let status = ''
      if (type === 'user.message' || type.startsWith('agent.message') || type.startsWith('agent.thinking')) {
        const content = type.endsWith('_chunk') ? group.flatMap(event => list(event.payload.content)) : payload.content
        body = type === 'agent.thinking' && !raw ? <details><summary>查看思考内容</summary><Content value={content}/></details> : <Content value={content}/>
      } else if (type.startsWith('agent.tool_')) {
        const call = group.find(event => event.type === 'agent.tool_call')
        const result = group.filter(event => event.type === 'agent.tool_result').at(-1)
        const chunks = group.filter(event => event.type === 'agent.tool_call_chunk')
        const tool = result?.payload || call?.payload || payload
        status = text(tool.status)
        const input = call?.payload.rawInput ?? tool.rawInput
        const details = <>
          {input !== undefined && <div className="sc-tool-section"><small>输入</small><pre>{json(input)}</pre></div>}
          {call && <Content value={call.payload.content}/>}
          {chunks.length > 0 && <div className="sc-tool-section"><small>工具增量</small>
            <div className="sc-tool-log"><Content value={chunks.flatMap(row => list(row.payload.content))}/></div>
          </div>}
          {result && <div className="sc-tool-section"><small>结果</small><Content value={tool.content}/><pre>{json(tool.rawOutput)}</pre>
            {tool.exitCode != null && <span className="sc-event-chip">退出码 {String(tool.exitCode)}</span>}
            {!!tool.output && <details><summary>输出保留情况</summary><pre>{json(tool.output)}</pre></details>}
          </div>}
        </>
        body = <>
          <div className="sc-event-inline">
            <strong>{text(call?.payload.title || tool.title || tool.toolName || tool.id)}</strong>
            {!!tool.provider && <code>{text(tool.provider)} · {text(tool.toolName)}</code>}
            {status && <span className="sc-event-chip">{label(toolLabels, status)}</span>}
          </div>
          {raw || type === 'agent.tool_call_chunk' ? details : <details><summary>查看工具详情</summary>{details}</details>}
        </>
      } else if (type === 'session.interaction_requested') {
        const interaction = text(payload.interaction)
        const state = answers[interaction]
        body = <Interaction event={item} answered={!!state?.answered && !state.closed} enabled={allowAnswer && !state?.closed && !item.replay}
          answer={value => answer(interaction, value)}/>
      } else {
        switch (type) {
          case 'session.interaction_closed':
            status = text(payload.outcome)
            body = <div className="sc-event-inline"><span className="sc-event-chip">{label(outcomeLabels, status)}</span>{text(payload.reason)}</div>
            break
          case 'session.state_update': {
            status = text(payload.state)
            body = <>
              <div className="sc-event-inline">
                <span className="sc-state-before">{stateName(payload.previousState)}</span><ArrowRight size={14} aria-hidden="true"/>
                <span className="sc-event-chip">{stateName(status)}</span><small>{label(stopLabels, payload.stopReason)}</small>
              </div>
              {!!payload.error && <p className="sc-error">{text(record(payload.error).message)}</p>}
            </>
            break
          }
          case 'session.info_update':
            body = <div className="sc-event-inline">
              {typeof payload.title === 'string' && <strong>{payload.title}</strong>}
              {typeof payload.updatedAt === 'string' && <time dateTime={payload.updatedAt}>{new Date(payload.updatedAt).toLocaleString('zh-CN')}</time>}
            </div>
            break
          case 'session.error':
            body = <><p className="sc-event-title">{text(payload.message)}</p>{!!payload.reason && <code>{text(payload.reason)}</code>}</>
            break
          case 'session.history_replay':
            status = text(payload.phase)
            body = <>
              <div className="sc-event-inline"><span className="sc-event-chip">{label(phaseLabels, status)}</span><code>{text(payload.retainedScope)}</code></div>
              <small>回放 ID <code>{text(payload.loadId)}</code></small>
              {!!payload.error && <p className="sc-error">{text(record(payload.error).message)}</p>}
            </>
            break
          case 'session.usage_update': {
            const used = Number(payload.used)
            const limit = Number(payload.size)
            const cost = record(payload.cost)
            body = <div className="sc-usage">
              <div className="sc-event-inline">
                <strong>{used.toLocaleString()} / {limit.toLocaleString()} tokens</strong>
                {!!payload.cost && <small>{String(cost.amount)} {text(cost.currency)}</small>}
              </div>
              {limit > 0 && <progress value={Math.min(used, limit)} max={limit} aria-label="上下文用量"/>}
            </div>
            break
          }
          case 'session.available_commands_update':
            body = list(payload.availableCommands).length > 0 ? <ul className="sc-command-list">
              {list(payload.availableCommands).map(command => <li key={text(command.name)}><code>{text(command.name)}</code>{text(command.description)}</li>)}
            </ul> : <small>暂无可用命令</small>
            break
          case 'session.config_option_update':
            body = list(payload.configOptions).length > 0 ? <dl className="sc-config-list">
              {list(payload.configOptions).map(option => {
                const choices = list(option.options).flatMap(choice => 'options' in choice ? list(choice.options) : [choice])
                const current = choices.find(choice => choice.value === option.currentValue)
                return <div key={text(option.id)}>
                  <dt title={text(option.description)}>{text(option.name)}</dt>
                  <dd><span className="sc-event-chip">{text(current?.name) || configValue(option.currentValue)}</span></dd>
                </div>
              })}
            </dl> : <small>暂无配置选项</small>
            break
          default:
            body = <pre>{json(payload)}</pre>
        }
      }
      return <li key={first.eventId} className="sc-event-card" data-tone={tone} data-status={status || undefined} data-event-type={type}>
        <div className="sc-row-heading">
          <span className="sc-event-icon"><Icon size={15} aria-hidden="true"/></span>
          <strong>{eventLabels[type] || type}</strong>
          {type.endsWith('_chunk') && <span className="sc-event-chip">增量</span>}
          {item.replay && type !== 'session.history_replay' && <span className="sc-event-chip"><Clock3 size={12}/>回放</span>}
          <time dateTime={item.time}>{clock.format(new Date(item.time))}</time>
        </div>
        <div className="sc-event-body">{body}</div>
        {raw && <details className="sc-event-json"><summary>原始字段 <code>{type}</code></summary><pre>{json(item)}</pre></details>}
      </li>
    })}
  </ol>
}
