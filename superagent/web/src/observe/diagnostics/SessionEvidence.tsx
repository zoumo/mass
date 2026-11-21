import { eventStatusLabel, stateLabel } from '../../workbench'
import SessionPreview from './SessionPreview'
import { identity, reminderBasis, type RecordProps, type Kind } from './records'

export default function SessionEvidence({ kind, detail, navigate }: RecordProps) {
  const release = detail.close || detail.preparation?.close
  const showRecord = (view: Kind, id: string) => navigate(view, '', undefined, id)
  return (
    <>
      {kind === 'executions' && (
        <dl>
          <dt>执行权</dt>
          <dd>
            {stateLabel(detail.authority)} · 第 {detail.generation} 代
          </dd>
          <dt>启动时的目标版本</dt>
          <dd>版本 {detail.goalRevision}</dd>
          <dt>准备状态</dt>
          <dd>
            {stateLabel(String(detail.preparationAtRetirement || detail.preparation?.state || ''))}
            {detail.preparation?.sessionId ? ' · ' + detail.preparation.sessionId : ''}
          </dd>
          <dt>启动输入</dt>
          <dd>
            {detail.startup?.event_id
              ? eventStatusLabel(detail.startup.status)
              : stateLabel(detail.startup?.status)}
          </dd>
          {detail.startup?.receipt && (
            <>
              <dt>输入回执</dt>
              <dd>
                <pre>{JSON.stringify(detail.startup.receipt, null, 2)}</pre>
              </dd>
            </>
          )}
          {detail.startup?.lastError && (
            <>
              <dt>输入错误</dt>
              <dd>{detail.startup.lastError}</dd>
            </>
          )}
          <dt>报告提醒</dt>
          <dd>
            {reminderBasis[String(detail.reminderEligibility)] || '未知'}
            {detail.reminder && (
              <span>
                {' '}
                ·{' '}
                {detail.reminder.event_id
                  ? eventStatusLabel(detail.reminder.status)
                  : stateLabel(detail.reminder.status)}
              </span>
            )}
          </dd>
          {detail.workObservation && (
            <>
              <dt>观测到的任务</dt>
              <dd>
                {detail.workObservation.provider || '提供方未知'} ·{' '}
                {detail.workObservation.workId || '任务标识未确认'} · {stateLabel(detail.workObservation.state)}
              </dd>
            </>
          )}
          {detail.workObservation && (
            <>
              <dt>观测到的 Session</dt>
              <dd>
                {detail.workObservation.sessionId} · {detail.workObservation.sessionUid} · 第{' '}
                {detail.workObservation.generation} 代
              </dd>
            </>
          )}
          {detail.workObservation && (
            <>
              <dt>实时观测</dt>
              <dd>
                {detail.workObservation.continuous ? '连续' : '未确认'} · {detail.workObservation.observedAt}
              </dd>
            </>
          )}
          {detail.workObservation?.end && (
            <>
              <dt>{detail.workObservation.end.reason === 'idle' ? 'Session 空闲' : '原生任务结束'}</dt>
              <dd>
                {detail.workObservation.end.reason} · {detail.workObservation.end.at} ·{' '}
                {detail.workObservation.end.eventId}
              </dd>
            </>
          )}
          {detail.workObservation?.observationGap && (
            <>
              <dt>任务观测中断</dt>
              <dd>{detail.workObservation.observationGap}</dd>
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
      {release && (
        <dl data-testid="release-evidence">
          <dt>Session 已确认释放</dt>
          <dd>
            {release.method} · {release.at}
          </dd>
        </dl>
      )}
      {kind === 'executions' && detail.startEventId && (
        <button onClick={() => showRecord('internal-events', String(detail.startEventId))} type="button">
          启动事件
        </button>
      )}
      {kind === 'executions' && detail.reminderEventId && (
        <button onClick={() => showRecord('internal-events', String(detail.reminderEventId))} type="button">
          提醒事件
        </button>
      )}
      {kind === 'executions' && detail.issueId && (
        <button onClick={() => navigate('reports', String(detail.issueId), 'issueId')} type="button">
          此 Issue 的报告历史
        </button>
      )}
      {kind === 'bindings' && (
        <>
          <dl>
            <dt>角色</dt>
            <dd>
              {stateLabel(detail.role)} · 第 {detail.generation} 代
            </dd>
            <dt>MASS Session</dt>
            <dd>{detail.sessionId || '尚未准备'}</dd>
            <dt>运行时</dt>
            <dd>{detail.runtimeRef}</dd>
            {detail.model && <><dt>模型</dt><dd>{detail.model}</dd></>}
            {detail.effort && <><dt>推理强度</dt><dd>{detail.effort}</dd></>}
            <dt>工作目录</dt>
            <dd>{detail.workspace}</dd>
          </dl>
          {detail.sessionId && detail.sessionUid && (
            <SessionPreview
              key={identity(detail) + ':' + detail.sessionUid}
              bindingId={identity(detail)}
            ></SessionPreview>
          )}
          {!(detail.sessionId && detail.sessionUid) && <p>核心服务记录 Session 标识后，即可查看历史。</p>}
        </>
      )}
    </>
  )
}
