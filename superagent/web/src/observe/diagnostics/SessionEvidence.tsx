import { eventStatusLabel, stateLabel } from '../../workbench'
import SessionPreview from './SessionPreview'
import { identity, reminderBasis, type RecordProps, type Kind } from './records'

export default function SessionEvidence({ kind, detail, navigate }: RecordProps) {
  const isBinding = kind === 'session-bindings'
  const isIssueBinding = isBinding && detail.role === 'issue'
  const { close, recovery, workObservation } = detail
  const showRecord = (view: Kind, id: string) => navigate(view, '', undefined, id)
  return (
    <>
      {isBinding && (
        <dl>
          <dt>角色</dt>
          <dd>{stateLabel(detail.role)}</dd>
          <dt>准备状态</dt>
          <dd>{stateLabel(detail.state)}</dd>
          <dt>MASS Session</dt>
          <dd>{detail.sessionId || '尚未准备'}</dd>
          <dt>运行时</dt>
          <dd>{detail.runtimeRef}</dd>
          {detail.model && <><dt>模型</dt><dd>{detail.model}</dd></>}
          {detail.effort && <><dt>推理强度</dt><dd>{detail.effort}</dd></>}
          <dt>工作目录</dt>
          <dd>{detail.workspace}</dd>
          {detail.retiredAt && <><dt>停用时间</dt><dd>{detail.retiredAt}</dd></>}
        </dl>
      )}
      {isIssueBinding && (
        <dl>
          {detail.startGoalRevision && <><dt>启动时的目标版本</dt><dd>{detail.startGoalRevision}</dd></>}
          {detail.startup && <><dt>启动输入</dt><dd>{eventStatusLabel(detail.startup.status)}</dd></>}
          <dt>报告提醒</dt>
          <dd>
            {reminderBasis[String(detail.reminderEligibility)] || '未知'}
            {detail.reminder && ' · ' + eventStatusLabel(detail.reminder.status)}
          </dd>
          {workObservation && (
            <>
              <dt>观测到的任务</dt>
              <dd>
                {workObservation.provider || '提供方未知'} ·{' '}
                {workObservation.workId || '任务标识未确认'} · {stateLabel(workObservation.state)}
              </dd>
              <dt>观测到的 Session</dt>
              <dd>{workObservation.sessionId} · {workObservation.sessionUid}</dd>
              <dt>实时观测</dt>
              <dd>{workObservation.continuous ? '连续' : '未确认'} · {workObservation.observedAt}</dd>
              {workObservation.end && (
                <>
                  <dt>{workObservation.end.reason === 'idle' ? 'Session 空闲' : '原生任务结束'}</dt>
                  <dd>{workObservation.end.reason} · {workObservation.end.at} · {workObservation.end.eventId}</dd>
                </>
              )}
              {workObservation.observationGap && <><dt>任务观测中断</dt><dd>{workObservation.observationGap}</dd></>}
            </>
          )}
        </dl>
      )}
      {detail.stop && (
        <dl data-testid="stop-evidence">
          <dt>停止请求</dt>
          <dd>
            {detail.stop.requestedAt} · {stateLabel(detail.stop.status)}
          </dd>
          <dt>原生任务状态</dt>
          <dd>{stateLabel(detail.stop.workState)}</dd>
          {detail.stop.receipt && (
            <>
              <dt>停止回执</dt>
              <dd>
                {stateLabel(detail.stop.receipt.result)} {detail.stop.receipt.target}
              </dd>
            </>
          )}
          {detail.stop.errorCode && (
            <>
              <dt>停止错误</dt>
              <dd>{detail.stop.errorCode}</dd>
            </>
          )}
          {detail.stop.observation && (
            <>
              <dt>Session 实时观测</dt>
              <dd>
                {stateLabel(detail.stop.observation.state)} · {detail.stop.observation.at}{' '}
                {detail.stop.observation.stopReason}
              </dd>
            </>
          )}
          {detail.stop.observationGap && (
            <>
              <dt>观测中断</dt>
              <dd>{detail.stop.observationGap}</dd>
            </>
          )}
        </dl>
      )}
      {close && (
        <dl data-testid="release-evidence">
          <dt>Session 已确认释放</dt>
          <dd>
            {close.method} · {close.at}
          </dd>
        </dl>
      )}
      {recovery && (
        <dl data-testid="recovery-evidence">
          <dt>恢复状态</dt>
          <dd>{stateLabel(recovery.status)} · {recovery.mode}</dd>
          <dt>请求恢复时间</dt>
          <dd>{recovery.createdAt}</dd>
          {recovery.completedAt && <><dt>恢复完成时间</dt><dd>{recovery.completedAt}</dd></>}
          {recovery.lastError && <><dt>恢复错误</dt><dd>{recovery.lastError}</dd></>}
        </dl>
      )}
      {recovery?.replacementSessionBindingId && (
        <button onClick={() => showRecord('session-bindings', recovery.replacementSessionBindingId!)} type="button">
          查看恢复后的会话绑定
        </button>
      )}
      {detail.previousSessionBindingId && (
        <button onClick={() => showRecord('session-bindings', detail.previousSessionBindingId!)} type="button">
          查看上一个会话绑定
        </button>
      )}
      {isIssueBinding && detail.startEventId && (
        <button onClick={() => showRecord('internal-events', detail.startEventId!)} type="button">
          启动事件
        </button>
      )}
      {isIssueBinding && detail.reminderEventId && (
        <button onClick={() => showRecord('internal-events', detail.reminderEventId!)} type="button">
          提醒事件
        </button>
      )}
      {isIssueBinding && detail.issueId && (
        <button onClick={() => navigate('reports', String(detail.issueId), 'issueId')} type="button">
          此 Issue 的报告历史
        </button>
      )}
      {isBinding && (detail.sessionId && detail.sessionUid
        ? <SessionPreview key={identity(detail) + ':' + detail.sessionUid} sessionBindingId={identity(detail)}/>
        : <p>核心服务记录 Session 标识后，即可查看历史。</p>)}
    </>
  )
}
