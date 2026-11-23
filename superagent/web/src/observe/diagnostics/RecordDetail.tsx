import { useState } from 'react'
import { Button } from '../../components/ui/button'
import { TraceLink } from '../TraceLinks'
import ExecutionProfileFields from '../ExecutionProfileFields'
import StructuredContent from '../StructuredContent'
import { eventStatusLabel, messageBody, stateLabel } from '../../workbench'
import SessionEvidence from './SessionEvidence'
import ChannelEvidence from './ChannelEvidence'
import { followupLabel, identity, replyLabel, type RecordProps, type Kind } from './records'

function TraceID({ traceId, phase, at }: { traceId: string; phase: string; at?: string }) {
  const [result, setResult] = useState('')
  async function copy() {
    try {
      await navigator.clipboard.writeText(traceId)
      setResult('已复制')
    } catch {
      setResult('复制失败')
    }
  }
  return (
    <li>
      {phase} · <TraceLink id={traceId} at={at}/>
      <Button type="button" variant="ghost" size="sm" onClick={copy}>
        <span aria-live="polite">{result || '复制 Trace ID'}</span>
      </Button>
    </li>
  )
}

export default function RecordDetail({ kind, detail, navigate }: RecordProps) {
  const showRecord = (view: Kind, id: string) => navigate(view, '', undefined, id)
  const showThread = (key: string) => showRecord('threads', key)
  const body = kind === 'messages' ? messageBody(detail) : detail.text
  return (
    <>
      {kind !== 'session-bindings' && (detail.acceptance || detail.status || detail.state) && (
        <span className="record-state">
          {kind === 'outbox'
            ? replyLabel(String(detail.status))
            : kind === 'internal-events'
              ? eventStatusLabel(detail.status)
              : stateLabel(String(detail.acceptance || detail.status || detail.state))}
        </span>
      )}
      {detail.title && <h2>{detail.title}</h2>}
      {detail.reason && <p>{detail.reason}</p>}
      {detail.lastError && <p role="status">{detail.lastError}</p>}
      {(kind === 'outbox' || kind === 'internal-events') && detail.content && typeof detail.content !== 'string' && <StructuredContent content={detail.content} />}
      {body && (
        <pre className="body-text" data-testid="message-body">
          {body}
        </pre>
      )}
      <dl>
        <dt>记录标识</dt>
        <dd>{identity(detail)}</dd>
        {detail.source && <dt>来源</dt>}
        {detail.source && <dd>{detail.source}</dd>}
        {detail.receivedAt && <dt>接收时间</dt>}
        {detail.receivedAt && <dd>{detail.receivedAt}</dd>}
      </dl>
      {!!detail.traceAssociations?.length && (
        <section>
          <h2>关联 Trace</h2>
          <ul>
            {detail.traceAssociations.map((association) => (
              <TraceID key={association.phase + '/' + association.traceId} {...association} at={detail.createdAt || detail.receivedAt} />
            ))}
          </ul>
        </section>
      )}
      {detail.observationGap && <p>观测中断 · {detail.observationGap}</p>}
      {detail.threadKey && (
        <button
          onClick={() => showThread(String(detail.threadKey))}
          type="button"
        >{`查看线程 ${detail.threadKey}`}</button>
      )}
      <ChannelEvidence kind={kind} detail={detail} navigate={navigate} />
      {detail.topicId && (
        <button
          onClick={() => showRecord('topics', String(detail.topicId))}
          type="button"
        >{`查看 Topic ${detail.topicId}`}</button>
      )}
      {detail.sessionBindingId && (
        <button onClick={() => showRecord('session-bindings', String(detail.sessionBindingId))} type="button">
          查看 SessionBinding
        </button>
      )}
      {kind === 'topics' && (
        <>
          {detail.description && <p>{detail.description}</p>}
          <dl>
            <dt>工作目录</dt>
            <dd>{detail.workspace}</dd>
          </dl>
        </>
      )}
      {kind === 'internal-events' && detail.messageId && (
        <button onClick={() => showRecord('messages', detail.messageId!)} type="button">
          查看来源消息
        </button>
      )}
      {kind === 'topics' && (
        <button onClick={() => navigate('issues', identity(detail), 'topicId')} type="button">
          查看 Topic 下的 Issue
        </button>
      )}
      {kind === 'topics' && (
        <button onClick={() => navigate('timers', identity(detail))} type="button">
          查看 Topic 定时器
        </button>
      )}
      {detail.issueId && (
        <button onClick={() => showRecord('issues', String(detail.issueId))} type="button">
          查看 Issue
        </button>
      )}
      {(detail.cancellation || detail.reopening) && (
        <dl data-testid="lifecycle-history">
          {detail.cancellation && (
            <>
              <dt>取消</dt>
              <dd>
                {detail.cancellation.reason} · {detail.cancellation.at}
              </dd>
            </>
          )}
          {detail.reopening && (
            <>
              <dt>重开</dt>
              <dd>
                {detail.reopening.reason} · {detail.reopening.at}
              </dd>
            </>
          )}
        </dl>
      )}
      {kind === 'topics' && detail.cancellation?.sessionBindingId && detail.cancellation.sessionBindingId !== detail.sessionBindingId && (
        <button onClick={() => showRecord('session-bindings', detail.cancellation!.sessionBindingId)} type="button">
          已停用的 Topic Session
        </button>
      )}
      {kind === 'topics' && detail.goal && <p>{detail.goal}</p>}
      {kind === 'issues' && (
        <>
          <h2>目标</h2>
          <pre className="body-text">{detail.goal}</pre>
          <dl>
            <dt>工作目录</dt>
            <dd>{detail.workspace}</dd>
            <dt>Issue 版本</dt>
            <dd>{detail.revision}</dd>
            <dt>目标版本</dt>
            <dd>{detail.goalRevision}</dd>
            {detail.executionProfile && <ExecutionProfileFields profile={detail.executionProfile}/>}
            <dt>Issue Session</dt>
            <dd>
              {detail.currentSessionBindingId ? stateLabel(detail.issueSession?.state) : '当前没有会话'}
              {detail.issueSession?.observedAt && <span> · {detail.issueSession.observedAt}</span>}
              {detail.issueSession?.observationGap && <span> · {detail.issueSession.observationGap}</span>}
            </dd>
            <dt>最近一次目标变更通知</dt>
            {detail.latestRequirementsNotification && (
              <dd>
                {eventStatusLabel(detail.latestRequirementsNotification.status)} · 目标版本{' '}
                {detail.latestRequirementsNotification.revision}
                <button
                  onClick={() =>
                    showRecord('internal-events', identity(detail.latestRequirementsNotification!))
                  }
                  type="button"
                >
                  通知事件
                </button>
              </dd>
            )}
            {!detail.latestRequirementsNotification && <dd>None</dd>}
          </dl>
          {!!detail.resolvedBlockers?.length && (
            <>
              <h2>已解除的阻塞</h2>
              <ul>
                {detail.resolvedBlockers.map((blocker) => (
                  <li key={blocker.id}>
                    {blocker.reason} · {blocker.resolution?.reason} · {blocker.resolution?.at}
                    {blocker.resolution?.messageId && (
                      <button onClick={() => showRecord('messages', blocker.resolution!.messageId!)} type="button">
                        查看解除依据
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            </>
          )}
          {!!detail.blockers?.length && (
            <>
              <h2>受阻原因</h2>
              <ul>
                {detail.blockers.map((blocker) => (
                  <li key={blocker.id}>
                    <strong>{blocker.kind === 'human' ? '等待人工处理' : '依赖的 Issue'}</strong>· {blocker.reason}
                    {blocker.dependencyIssueId && (
                      <button
                        onClick={() => showRecord('issues', blocker.dependencyIssueId!)}
                        type="button"
                      >{`查看依赖的 Issue ${blocker.dependencyIssueId}`}</button>
                    )}
                  </li>
                ))}
              </ul>
            </>
          )}
        </>
      )}
      {kind === 'timers' && (
        <>
          <h2>跟进内容</h2>
          <pre className="body-text">{typeof detail.content === 'string' ? detail.content : undefined}</pre>
          <dl>
            <dt>定时器状态</dt>
            <dd>{stateLabel(detail.status)}</dd>
            <dt>版本</dt>
            <dd>{detail.revision}</dd>
            <dt>触发类型</dt>
            <dd>{detail.type}</dd>
            <dt>计划触发时间 (UTC)</dt>
            <dd>{detail.dueAt}</dd>
            {detail.firedAt && (
              <>
                <dt>实际触发时间 (UTC)</dt>
                <dd>{detail.firedAt}</dd>
              </>
            )}
            {detail.followup && (
              <>
                <dt>跟进通知事件</dt>
                <dd>{followupLabel(detail.followup)}</dd>
              </>
            )}
          </dl>
          {detail.triggerMessageId && (
            <button onClick={() => showRecord('messages', detail.triggerMessageId!)} type="button">
              查看触发消息
            </button>
          )}
          {detail.followup?.kind === 'routed_message' && detail.followup.event_id && (
            <button onClick={() => showRecord('internal-events', detail.followup!.event_id!)} type="button">
              查看跟进事件
            </button>
          )}
        </>
      )}
      {!!((kind === 'issues' || kind === 'timers') && detail.materials?.length) && (
        <>
          <h2>材料</h2>
          <ul>
            {detail.materials.map((material, index) => (
              <li key={index}>
                {material.messageId && (
                  <button onClick={() => showRecord('messages', material.messageId!)} type="button">
                    {material.label || material.messageId}
                  </button>
                )}
                {!material.messageId && (
                  <span>
                    {material.label ? material.label + ' · ' : ''}
                    {material.uri}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
      {kind === 'issues' && (
        <>
          {detail.currentSessionBindingId && (
            <button onClick={() => showRecord('session-bindings', detail.currentSessionBindingId!)} type="button">
              当前会话
            </button>
          )}
          <button onClick={() => navigate('session-bindings', identity(detail), 'issueId')} type="button">
            会话绑定历史
          </button>
          <button onClick={() => navigate('reports', identity(detail))} type="button">
            报告历史
          </button>
        </>
      )}
      <SessionEvidence kind={kind} detail={detail} navigate={navigate} />
      {kind === 'reports' && (
        <>
          <h2>{detail.summary}</h2>
          <p>
            {stateLabel(detail.kind)} · {detail.createdAt}
            {!!detail.goalRevision && <span> · 目标版本 {detail.goalRevision}</span>}
            <span> · {detail.current ? '当前报告' : '历史报告'}</span>
          </p>
          {detail.details && <pre className="body-text">{detail.details}</pre>}
          {!!detail.evidence?.length && <h3>证据</h3>}
          {!!detail.evidence?.length && (
            <ul>
              {detail.evidence.map((ref, index) => (
                <li key={index}>{ref}</li>
              ))}
            </ul>
          )}
          {!!detail.questions?.length && <h3>待确认问题</h3>}
          {!!detail.questions?.length && (
            <ul>
              {detail.questions.map((question, index) => (
                <li key={index}>{question}</li>
              ))}
            </ul>
          )}
          <button onClick={() => showRecord('internal-events', String(detail.eventId))} type="button">
            Topic 通知
          </button>
        </>
      )}
      {detail.completion && (
        <>
          <h2>业务验收</h2>
          <p>{detail.completion.reason}</p>
          <p>{detail.completion.completedAt}</p>
          <ul>
            {detail.completion.evidence.map((ref, index) => (
              <li key={index}>{ref}</li>
            ))}
          </ul>
        </>
      )}
      {kind === 'internal-events' && detail.sender?.session_binding_id && (
        <button onClick={() => showRecord('session-bindings', detail.sender!.session_binding_id!)} type="button">
          查看发送会话
        </button>
      )}
      {kind === 'internal-events' && detail.recipient?.session_binding_id && (
        <button onClick={() => showRecord('session-bindings', detail.recipient!.session_binding_id!)} type="button">
          查看接收会话
        </button>
      )}
      {kind === 'internal-events' && (
        <pre data-testid="routing-record">{JSON.stringify(detail, null, 2)}</pre>
      )}
      {kind !== 'internal-events' && (
        <details>
          <summary>原始字段和引用</summary>
          <pre>{JSON.stringify(detail, null, 2)}</pre>
        </details>
      )}
    </>
  )
}
