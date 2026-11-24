import { ArrowUpRight } from 'lucide-react'
import { Button } from '../components/ui/button'
import { formatTime } from '../workbench'
import { useRecord, useRefresh, viewHref } from './observer'
import './watchdog.css'

const statuses = {
  finding: '有发现',
  no_finding: '无发现',
  incomplete: '巡检未完成',
}

const reasons: Record<string, string> = {
  suspected_missed_acceptance: '疑似漏验收',
  no_candidate: '没有待巡检的 Report',
  normal_waiting: 'Report 正在正常等待验收',
  insufficient_evidence: '证据不足',
  core_read_failed: '读取 Core 状态失败',
  invalid_endpoint: 'Core 地址无效',
  agent_failed: '巡检 Session 执行失败',
  invalid_output: '巡检 Session 的输出无效',
  timed_out: '巡检超时',
  canceled: '巡检已取消',
  release_unconfirmed: '巡检 Session 的退出尚未确认',
}

type Check = {
  observedAt: string
  status: keyof typeof statuses
  reason: string
  releaseUnconfirmed?: boolean
  topicId?: string
  issueId?: string
  judgment?: string
  recommendation?: string
}

export default function WatchdogView() {
  const { online } = useRefresh()
  const record = useRecord<{ enabled: boolean; running: boolean; latest?: Check }>('watchdog')
  const latest = record.data?.latest
  return <>
    {online && record.loading && !record.data && !record.error &&
      <p className="empty" role="status">正在读取巡检结果…</p>}
    {record.error && <p className="read-error" role="alert">
      巡检结果读取失败 · {record.error}{record.data && ' · 快照已过时'}
    </p>}
    {record.data && <p className="panel empty" role="status">
      {!record.data.enabled ? 'Watchdog 尚未启用。' : !record.data.running ? 'Watchdog 已停止。'
        : latest ? 'Watchdog 正在运行。' : 'Watchdog 正在运行，等待首轮巡检完成。'}
    </p>}
    {latest && <section className="panel watchdog-check" aria-labelledby="watchdog-latest">
      <header className="panel-heading">
        <div className="panel-title"><h2 id="watchdog-latest">最近一次巡检</h2></div>
        <span className="watchdog-status" data-status={latest.status}>{statuses[latest.status] ?? latest.status}</span>
      </header>
      <dl className="watchdog-facts">
        <div>
          <dt>巡检时间</dt>
          <dd><time dateTime={latest.observedAt} title={latest.observedAt}>{formatTime(latest.observedAt)}</time></dd>
        </div>
        <div><dt>结果</dt><dd>{reasons[latest.reason] ?? latest.reason}</dd></div>
        {latest.releaseUnconfirmed && latest.reason !== 'release_unconfirmed' && <div>
          <dt>进程回收</dt><dd>{reasons.release_unconfirmed}</dd>
        </div>}
        {latest.judgment && <div>
          <dt>{latest.status === 'finding' ? '发现' : '判断'}</dt>
          <dd>{latest.judgment}</dd>
        </div>}
        {latest.recommendation && <div><dt>建议</dt><dd>{latest.recommendation}</dd></div>}
      </dl>
      {(latest.topicId || latest.issueId) && <footer className="watchdog-links">
        {latest.topicId && <Button asChild variant="outline" size="sm">
          <a href={viewHref('topic', latest.topicId)}>查看 Topic <ArrowUpRight aria-hidden="true" /></a>
        </Button>}
        {latest.issueId && <Button asChild variant="outline" size="sm">
          <a href={viewHref('issue', latest.issueId)}>查看 Issue <ArrowUpRight aria-hidden="true" /></a>
        </Button>}
      </footer>}
    </section>}
  </>
}
