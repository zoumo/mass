import type { ReactNode } from 'react'
import { Bot, ChevronRight, ClipboardList, GitBranch, Hash, Network } from 'lucide-react'
import StructuredContent, { type StructuredBody } from './StructuredContent'
import { TraceLink, type TraceAssociation } from './TraceLinks'
import './conversation-card.css'

const icons = { router: GitBranch, topic: Hash, issue: ClipboardList, core: Network, "issue-session": Bot }
export type Participant = { kind: keyof typeof icons; label: string; href?: string; title?: string }

export function ObjectAvatar({ kind, href, title }: Participant) {
  const Icon = icons[kind]
  return href
    ? <a className="conversation-avatar conversation-avatar-link" data-actor={kind} href={href} title={title} aria-label={title}><Icon size={17} aria-hidden="true"/></a>
    : <span className="conversation-avatar" data-actor={kind}><Icon size={17} aria-hidden="true"/></span>
}

type CardProps = {
  kind: string
  recordId: string
  preview: string
  content?: StructuredBody
  at: string
  time: string
  sender: Participant
  recipient: Participant
  facts: { label: string; value: ReactNode }[]
  href: string
  selected: boolean
  traceAssociations?: TraceAssociation[]
}

export function ConversationCard({ kind, recordId, preview, content, at, time, sender, recipient, facts, href, selected, traceAssociations }: CardProps) {
  const excerptText = Array.from(preview.trim())
  const description = excerptText.length > 200 ? excerptText.slice(0, 200).join('') + '…' : preview
  const traceId = traceAssociations?.[0]?.traceId

  return <article className="conversation-chat-row" data-actor={sender.kind}>
    <div className="conversation-card-party conversation-card-sender" role="group" aria-label="发送方">
      <ObjectAvatar {...sender}/>
      <span>{sender.label}</span>
    </div>
    <div className="conversation-card-party conversation-card-recipient" role="group" aria-label="收件方">
      <ObjectAvatar {...recipient}/>
      <span>{recipient.label}</span>
    </div>
    <div className="conversation-card" data-selected={selected}>
      <header>
        <div className="conversation-card-heading">
          <strong className="conversation-card-type">{kind}</strong>
          <code className="conversation-card-record-id">{recordId}</code>
        </div>
        {traceId && <TraceLink id={traceId} at={at} compact/>}
      </header>
      <div className="conversation-card-content" data-content-type={content?.type}>
        {content ? <StructuredContent content={content} previewOptions/>
          : <p className="conversation-card-excerpt">{description}</p>}
      </div>
      <footer>
        {facts.length > 0 && <dl className="conversation-card-facts">
          {facts.map(fact => <div key={fact.label}><dt>{fact.label}</dt><dd>{fact.value}</dd></div>)}
        </dl>}
        <div className="conversation-card-footer-actions">
          <time dateTime={at}>{time}</time>
          <a href={href} className="conversation-card-details" aria-current={selected ? 'true' : undefined} aria-label={`查看消息详情 · ${kind} · ${description}`}>
            <span>详情</span><ChevronRight size={14} aria-hidden="true"/>
          </a>
        </div>
      </footer>
    </div>
  </article>
}
