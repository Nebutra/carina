import { Folder, Icon, ShieldCheck } from '../design-system'
import { EmptyState } from './empty-state'
import type { Role } from './types'

export type ContextPanelProps = {
  workspace: string
  role: Role | null
  requestedRole: Role
  connected: boolean
  itemCount: number
}

export function ContextPanel({ workspace, role, requestedRole, connected, itemCount }: ContextPanelProps) {
  return <section className="react-context-panel" data-context-view="true">
    <div className="react-context-heading"><span className="eyebrow">SESSION CONTEXT</span><h2>Context</h2><p>Inspect the bounded inputs and authority state attached to this conversation.</p></div>
    <div className="react-context-grid">
      <div className="react-context-card"><Icon icon={Folder} /><div><span>Workspace</span><strong>{workspace || 'Not selected'}</strong></div></div>
      <div className="react-context-card"><Icon icon={ShieldCheck} /><div><span>Permission mode</span><strong>{role ? role === 'operator' ? 'Operator' : 'Observer' : `${requestedRole === 'operator' ? 'Operator' : 'Observer'} requested`}</strong></div></div>
      <div className="react-context-card"><div><span>Gateway</span><strong>{connected ? 'Connected' : 'Offline'}</strong></div></div>
      <div className="react-context-card"><div><span>Projected events</span><strong>{itemCount}</strong></div></div>
    </div>
    {!workspace && <EmptyState compact title="No workspace context" description="Choose a workspace to make the file and execution boundary explicit." />}
  </section>
}
