import { Fragment, useState, type ReactNode } from 'react'
import { ArrowRight, Boxes, ChevronDown, CircleDot, FileText, GitBranch, Mail, Milestone, Play, Workflow } from 'lucide-react'
import './perspective-flow.css'

export type FlowNode = {
  kind: 'message' | 'router' | 'topic' | 'issue' | 'session' | 'report' | 'state'
  label: string
  href?: string
}
export type FlowLane = { id: string; nodes: FlowNode[]; edges: string[]; detail?: ReactNode }

const types = {
  message: [Mail, 'Message'], router: [GitBranch, 'Router'], topic: [Boxes, 'Topic'],
  issue: [CircleDot, 'Issue'], session: [Play, 'Session'], report: [FileText, '报告'], state: [Milestone, '状态'],
} as const

function Node({ node }: { node: FlowNode }) {
  const [Icon, name] = types[node.kind]
  const label = <><Icon size={15} aria-hidden="true"/><span>{node.label !== name && <small>{name}</small>}<strong>{node.label}</strong></span></>
  const style = 'flow-node flow-' + node.kind
  return node.href ? <a className={style} href={node.href} title={node.label}>{label}</a> :
    <span className={style} title={node.label}>{label}</span>
}

function LaneDetails({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  return <details className="flow-detail" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>展开记录<ChevronDown size={12}/></summary>{open && <div>{children}</div>}
  </details>
}

export default function PerspectiveFlow({ title, description, lanes, empty }: {
  title: string; description?: string; lanes: FlowLane[]; empty?: string
}) {
  const [all, setAll] = useState(false)
  const visible = all ? lanes : lanes.slice(0, 3)
  return <section className="panel perspective-flow" aria-label={title}>
    <header className="panel-heading"><div className="panel-title"><Workflow size={17}/><h2>{title}</h2></div></header>
    {description && <p className="flow-description">{description}</p>}
    {visible.length ? <ul className="flow-canvas">{visible.map(lane => <li key={lane.id} data-flow-id={lane.id}>
      <div className="flow-route">{lane.nodes.map((node, index) => <Fragment key={index}>
        {index > 0 && <span className="flow-edge"><span>{lane.edges[index - 1]}</span><ArrowRight size={14} aria-hidden="true"/></span>}
        <Node node={node}/>
      </Fragment>)}</div>
      {lane.detail && <LaneDetails>{lane.detail}</LaneDetails>}
    </li>)}</ul> : <p className="empty">{empty || '暂无流程记录'}</p>}
    {lanes.length > 3 && <button className="flow-more" type="button" aria-expanded={all} onClick={() => setAll(!all)}>
      {all ? '收起流程' : '再看 ' + (lanes.length - visible.length) + ' 条流程'}<ChevronDown size={14}/>
    </button>}
  </section>
}
