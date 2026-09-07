import type { Dispatch, RefObject, SetStateAction } from 'react'
import { EmptyState } from './empty-state'
import { ChatStart } from './chat-start'
import { MetricGroup } from './metric-group'
import { PageHeader } from './page-header'
import { SessionList } from './session-list'
import type { Attachment, Role, Session } from './types'
import type { SessionItem } from '../lib/trajectory'

export type SurfaceView = 'chat' | 'inbox' | 'runs' | 'graph'

export type SurfaceProps = {
  view: SurfaceView
  activeSession: Session | null
  items: SessionItem[]
  sessions: Session[]
  rosterStatus: 'idle' | 'loading' | 'ready' | 'error'
  rosterError: string
  role: Role | null
  requestedRole: Role
  workspace: string
  connected: boolean
  prompt: string
  setPrompt: Dispatch<SetStateAction<string>>
  attachments: Attachment[]
  addAttachments: (files: FileList | null) => void
  removeAttachment: (index: number) => void
  fileInputRef: RefObject<HTMLInputElement | null>
  settingsTriggerRef: RefObject<HTMLButtonElement | null>
  onSubmit: () => Promise<void>
  onSelectSession: (session: Session | null) => Promise<void>
  onRefreshRoster: () => void
  onOpenConnection: () => void
  onPickWorkspace: () => void
  submitting: boolean
}

/** Routes operational surfaces through stable project patterns. */
export function Surface({ view, activeSession, items, sessions, rosterStatus, rosterError, role, requestedRole, workspace, connected, prompt, setPrompt, attachments, addAttachments, removeAttachment, fileInputRef, settingsTriggerRef, onSubmit, onSelectSession, onRefreshRoster, onOpenConnection, onPickWorkspace, submitting }: SurfaceProps) {
  if (view === 'chat') return <ChatStart activeSession={activeSession} itemCount={items.length} role={role} requestedRole={requestedRole} workspace={workspace} connected={connected} prompt={prompt} setPrompt={setPrompt} attachments={attachments} addAttachments={addAttachments} removeAttachment={removeAttachment} fileInputRef={fileInputRef} settingsTriggerRef={settingsTriggerRef} onSubmit={onSubmit} onOpenConnection={onOpenConnection} onPickWorkspace={onPickWorkspace} submitting={submitting} />
  if (view === 'runs') return <section className="react-surface"><PageHeader eyebrow="RUNTIME" title="Runs" lead="Live sessions from the Gateway authoritative roster." /><MetricGroup metrics={[{ label: 'Sessions', value: sessions.length }, { label: 'Selected', value: activeSession ? '1' : '0' }, { label: 'Projection', value: activeSession ? 'ready' : 'idle' }]} /><div className="react-runs-list"><SessionList sessions={sessions} activeSession={activeSession} onSelect={onSelectSession} status={rosterStatus} error={rosterError} onRetry={onRefreshRoster} /></div></section>
  if (view === 'inbox') return <section className="react-surface"><PageHeader eyebrow="INBOX" title="Needs input" lead="Approvals, questions, and policy blocks from the daemon." /><EmptyState title="Inbox is clear" description="No approvals or questions are waiting." /></section>
  return <section className="react-surface"><PageHeader eyebrow="OPERATOR VIEW" title={view[0].toUpperCase() + view.slice(1)} lead="The Gateway projection is the source of truth." /><EmptyState title="No records" description="Connect a Gateway to load this projection." /></section>
}
