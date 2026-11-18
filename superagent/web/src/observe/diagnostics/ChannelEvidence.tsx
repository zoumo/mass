import { routingDeliveryLabel, stateLabel } from '../../workbench'
import { identity, type RecordProps, type Kind } from './records'

export default function ChannelEvidence({ kind, detail, navigate }: RecordProps) {
  const showRecord = (view: Kind, id: string) => navigate(view, '', undefined, id)
  return (
    <>
      {kind === 'messages' && (
        <dl>
          <dt>渠道消息</dt>
          <dd>{detail.sourceMessageId}</dd>
          <dt>渠道时间</dt>
          <dd>{detail.at || '未记录'}</dd>
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
        <button onClick={() => navigate('routing-deliveries', identity(detail), 'messageId')} type="button">
          查看路由投递
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
                  {relation.status && <> · {routingDeliveryLabel(relation.status)}</>}
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
      {kind === 'messages' && detail.echo && (
        <>
          <h2>{stateLabel(detail.echo.status)}</h2>
          <dl>
            {detail.echo.deadline && (
              <>
                <dt>等待截止时间</dt>
                <dd>{detail.echo.deadline}</dd>
              </>
            )}
            {detail.echo.resolvedAt && (
              <>
                <dt>确认时间</dt>
                <dd>{detail.echo.resolvedAt}</dd>
              </>
            )}
            {detail.echo.releaseReason && (
              <>
                <dt>人工放行原因</dt>
                <dd>{detail.echo.releaseReason}</dd>
              </>
            )}
            {detail.echo.match && (
              <>
                <dt>匹配的发送记录</dt>
                <dd>
                  <button onClick={() => showRecord('outbox', detail.echo!.match!.outboxId)} type="button">
                    {detail.echo.match.outboxId}
                  </button>
                </dd>
              </>
            )}
            {!!detail.echo.candidates?.length && (
              <>
                <dt>接收时的关联发送记录</dt>
                {detail.echo.candidates.map((candidate) => (
                  <dd key={candidate.outboxId}>
                    <button onClick={() => showRecord('outbox', candidate.outboxId)} type="button">
                      {candidate.outboxId}
                    </button>
                  </dd>
                ))}
              </>
            )}
          </dl>
          {detail.echo.routingEventId && (
            <button onClick={() => showRecord('routing-deliveries', detail.echo!.routingEventId!)} type="button">
              回声放行后的投递
            </button>
          )}
        </>
      )}
      {kind === 'threads' && (
        <>
          <dl>
            <dt>来源</dt>
            <dd>{detail.ref?.source}</dd>
            <dt>来源标识</dt>
            <dd>{detail.ref?.id}</dd>
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
              {detail.receipt?.conversationId && (
                <>
                  <dt>发送到的会话</dt>
                  <dd>{detail.receipt.conversationId}</dd>
                </>
              )}
              {detail.receipt?.messageId && (
                <>
                  <dt>渠道消息</dt>
                  <dd>{detail.receipt.messageId}</dd>
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
