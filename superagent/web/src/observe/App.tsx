import { useEffect, useState, type ComponentType, type MouseEvent } from 'react'
import { Activity, BarChart3, Boxes, CircleDot, GitBranch, ListTree, Mail, MessagesSquare, Pause, Play, Radio, RefreshCw, ScanLine, ShieldCheck, type LucideIcon } from 'lucide-react'
import { Button } from '../components/ui/button'
import { RefreshContext, navigate, observerHome, observerView, perspectives, perspectiveFromPath, useObserverLocation, viewHref, type Perspective, type ViewProps } from './observer'
import MessageView from './MessageView'
import RouterView from './RouterView'
import TopicView from './TopicView'
import TopicConversation from './TopicConversation'
import IssueView from './IssueView'
import Diagnostics from './diagnostics/Diagnostics'
import TraceView from './TraceView'
import MetricsView from './MetricsView'
import WatchdogView from './WatchdogView'

const views = {
  'topic-conversation': { title: 'Topic 会话', subtitle: '', icon: MessagesSquare, component: TopicConversation },
  message: { title: 'Message', subtitle: '消息', icon: Mail, component: MessageView },
  router: { title: 'Router', subtitle: '路由', icon: GitBranch, component: RouterView },
  topic: { title: 'Topic', subtitle: '主题', icon: Boxes, component: TopicView },
  issue: { title: 'Issue', subtitle: '事项', icon: CircleDot, component: IssueView },
  trace: { title: 'Trace', subtitle: '调用链', icon: ListTree, component: TraceView },
  metrics: { title: 'Metrics', subtitle: '', icon: BarChart3, component: MetricsView },
  watchdog: { title: 'Watchdog', subtitle: '', icon: ShieldCheck, component: WatchdogView },
} satisfies Record<Perspective, { title: string; subtitle: string; icon: LucideIcon; component: ComponentType<ViewProps> }>

export default function App() {
  const route = useObserverLocation()
  const query = route.searchParams
  const view = observerView(route)
  const diagnostics = view === 'diagnostics'
  const {
    component: View,
    icon: Icon,
    title,
    subtitle,
  } = diagnostics ? { component: Diagnostics, icon: Activity, title: '诊断', subtitle: '记录' } : views[view]
  const [paused, setPaused] = useState(() => sessionStorage.getItem('observer-paused') === 'true')
  const [online, setOnline] = useState(navigator.onLine)
  const [revision, setRevision] = useState(0)
  const refreshMs = !diagnostics && view === 'metrics' ? 30000 : 5000
  const refresh = () => setRevision((tick) => tick + 1)
  useEffect(() => {
    const update = () => setOnline(navigator.onLine)
    window.addEventListener('online', update)
    window.addEventListener('offline', update)
    const timer = window.setInterval(() => {
      if (!paused && !document.hidden && navigator.onLine) refresh()
    }, refreshMs)
    return () => {
      clearInterval(timer)
      window.removeEventListener('online', update)
      window.removeEventListener('offline', update)
    }
  }, [paused, refreshMs])
  function follow(event: MouseEvent) {
    const link = (event.target as Element).closest('a')
    if (
      !link ||
      event.defaultPrevented ||
      event.button ||
      event.metaKey ||
      event.ctrlKey ||
      event.altKey ||
      event.shiftKey ||
      link.target ||
      link.hasAttribute('download')
    )
      return
    const url = new URL(link.href)
    if (url.origin !== location.origin || (url.pathname !== observerHome && !perspectiveFromPath(url.pathname))) return
    event.preventDefault()
    navigate(url.pathname + url.search)
  }
  return (
    <RefreshContext.Provider value={{ revision, online, view }}>
      <div className="observer" onClick={follow}>
        <header className="topbar">
          <div className="shell top-inner">
            <a className="brand" href={observerHome} aria-label="Superagent Web 首页">
              <span className="brand-icon">
                <Activity />
              </span>
              <strong>Superagent</strong>
              <span className="brand-divider" />
              <span>Web</span>
            </a>
            <div className="secondary">
              <span className="readonly">
                <ScanLine size={14} />
                只读
              </span>
              <a href="/superagent/?view=internal-events">
                <Activity size={16} />
                诊断
              </a>
            </div>
          </div>
        </header>
        <nav className="tabs" aria-label="观察视角">
          <div className="shell tab-inner">
            {perspectives.map((name) => {
              const TabIcon = views[name].icon
              return (
                <a
                  key={name}
                  data-view={name}
                  href={viewHref(name)}
                  aria-current={!diagnostics && name === view ? 'page' : undefined}
                >
                  <TabIcon size={17} />
                  {views[name].title}
                </a>
              )
            })}
          </div>
        </nav>
        <main className="shell workspace">
          <div className="workspace-heading">
            <div className="workspace-title">
              <span className="view-glyph" data-view={view}>
                <Icon size={22} />
              </span>
              <h1>{title}</h1>
              {subtitle && <span className="section-name">{subtitle}</span>}
            </div>
            <div className="refresh-controls">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  sessionStorage.setItem('observer-paused', String(!paused))
                  setPaused(!paused)
                }}
              >
                {paused ? <Play /> : <Pause />}
                {paused ? '恢复自动刷新' : '暂停自动刷新'}
              </Button>
              <Button variant="outline" size="sm" onClick={refresh} disabled={!online}>
                <RefreshCw />
                刷新
              </Button>
            </div>
          </div>
          {!online && !(diagnostics && query.get('id')) && (
            <div className="read-error" role="alert">
              <Radio size={16} />
              浏览器已离线 · 快照已过时
            </div>
          )}
          <View query={query} />
        </main>
      </div>
    </RefreshContext.Provider>
  )
}
