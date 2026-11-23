import { eventStatusLabel, stateLabel } from '../../workbench'
import { identity, type RecordProps, type Kind } from './records'

export default function ChannelEvidence({ kind, detail, navigate }: RecordProps) {
  const showRecord = (view: Kind, id: string) => navigate(view, '', undefined, id)
  return (
    <>
      {kind === 'messages' && (
        <dl>
          <dt>渠道消息</dt>
          <dd>{detail.sourceMessageId}</dd>
          <dt>线程键</dt>
          <dd>{detail.threadKey}</dd>
          <dt>路由状态</dt>
          <dd>
            {detail.routing ? `第 ${detail.routing.round} 轮 · ${stateLabel(detail.routing.state)}` : '无路由记录'}
          </dd>
        </dl>
      )}
      {kind === 'messages' && (
        <button onClick={() => navigate('outbox', identity(detail), 'messageId')} type="button">
          查看消息回执
        </button>
      )}
      {kind === 'messages' && (
        <button onClick={() => navigate('routing-events', identity(detail), 'messageId')} type="button">
          查看路由事件
        </button>
      )}
      {!!(kind === 'messages' && detail.relations?.length) && (
        <section aria-label="路由历史">
          <h2>路由历史</h2>
          <ul className="routing-history">
            {detail.relations.map((relation) => (
              <li key={relation.id}>
                <span>
                  {stateLabel(relation.action)}
                  {relation.status && <> · {eventStatusLabel(relation.status)}</>}
                </span>
                <p>{relation.reason}</p>
                <button
                  onClick={() => showRecord('topics', relation.topicId)}
                  type="button"
                >{`查看 Topic ${relation.topicId}`}</button>
              </li>
            ))}
          </ul>
        </section>
      )}
      {kind === 'threads' && (
        <>
          <dl>
            <dt>线程键</dt>
            <dd>{detail.key}</dd>
            <dt>最近一条来源消息</dt>
            <dd>{detail.lastMessageAt || '暂未收到来源消息'}</dd>
          </dl>
          <button onClick={() => navigate('messages', identity(detail), 'threadKey')} type="button">
            查看线程消息
          </button>
          <button onClick={() => navigate('subscriptions', identity(detail), 'threadKey')} type="button">
            查看线程订阅
          </button>
          <button onClick={() => navigate('outbox', identity(detail), 'threadKey')} type="button">
            查看线程回执
          </button>
        </>
      )}
      {kind === 'messages' && detail.quotedReply && (
        <>
          <button onClick={() => showRecord('outbox', detail.quotedReply!.outboxId)} type="button">
            查看被引用的追问
          </button>
        </>
      )}
      {kind === 'outbox' && (
        <>
          {(detail.receipt || detail.blockerRef) && (
            <dl>
              {detail.receipt?.taskId && (
                <>
                  <dt>渠道任务</dt>
                  <dd>{detail.receipt.taskId}</dd>
                </>
              )}
              {detail.receipt?.errorCode && (
                <>
                  <dt>原因</dt>
                  <dd>{detail.receipt.errorCode}</dd>
                </>
              )}
              {detail.receipt?.retryAt && (
                <>
                  <dt>允许重试时间</dt>
                  <dd>{detail.receipt.retryAt}</dd>
                </>
              )}
              {detail.blockerRef && (
                <>
                  <dt>关联 blocker</dt>
                  <dd>{detail.blockerRef.blockerId}</dd>
                </>
              )}
            </dl>
          )}
          {detail.receipt?.uncertain && <p>之前的发送结果未确认，恢复时可能重复发送。</p>}
          {detail.messageId && (
            <button onClick={() => showRecord('messages', detail.messageId!)} type="button">
              查看目标消息
            </button>
          )}
          {detail.blockerRef && (
            <button onClick={() => showRecord('issues', detail.blockerRef!.issueId)} type="button">
              查看关联 Issue
            </button>
          )}
        </>
      )}
    </>
  )
}
