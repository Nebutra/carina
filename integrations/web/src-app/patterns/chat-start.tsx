import type { Dispatch, RefObject, SetStateAction } from 'react'
import { Button, ChevronDown, Folder, Icon, Settings2 } from '../design-system'
import { Composer } from './composer'
import type { Attachment, Role, Session } from './types'

export type ChatStartProps = {
  activeSession: Session | null
  itemCount: number
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
  onOpenConnection: () => void
  onPickWorkspace: () => void
  submitting: boolean
}

/** Application-first start state. It keeps the empty surface useful without marketing chrome. */
export function ChatStart({ activeSession, itemCount, role, requestedRole, workspace, connected, prompt, setPrompt, attachments, addAttachments, removeAttachment, fileInputRef, settingsTriggerRef, onSubmit, onOpenConnection, onPickWorkspace, submitting }: ChatStartProps) {
  const blockedReason = !connected ? 'Connect the Gateway before running' : role !== 'operator' ? 'Switch to Operator to run' : !workspace ? 'Choose a workspace before running' : ''
  const roleLabel = role
    ? role === 'operator' ? 'Creator mode' : 'Observer mode'
    : requestedRole === 'operator' ? 'Operator selected' : 'Observer selected'
  return <section className="react-chat-surface">
    <div className="react-chat-scroll"><div className="react-welcome">
      <span className="eyebrow">CONTROLLED AGENT WORKSPACE</span>
      <h2>{activeSession ? activeSession.title || activeSession.session_id : 'Full power, with a return path.'}</h2>
      <p>{activeSession ? `${itemCount} projected session events ready for inspection.` : 'Describe the change, attach context, and keep every effect inside the Gateway policy boundary.'}</p>
      {!activeSession && <div className="react-empty-context"><Button variant="ghost" size="compact" onClick={onPickWorkspace}><Icon icon={Folder} /><span>{workspace || 'Choose workspace'}</span><Icon icon={ChevronDown} /></Button><Button ref={settingsTriggerRef} variant="ghost" size="compact" onClick={onOpenConnection}><Icon icon={Settings2} /><span>{roleLabel}</span><Icon icon={ChevronDown} /></Button></div>}
    </div></div>
    <div className="react-chat-composer"><Composer prompt={prompt} setPrompt={setPrompt} attachments={attachments} addAttachments={addAttachments} removeAttachment={removeAttachment} fileInputRef={fileInputRef} onSubmit={onSubmit} blockedReason={blockedReason} onOpenConnection={onOpenConnection} submitting={submitting} /><div className="react-session-metrics" aria-label="Session metrics"><span>{activeSession ? `${itemCount} events` : 'New session'}</span><span>{roleLabel}</span><span>{connected ? 'Gateway connected' : 'Gateway offline'}</span></div></div>
  </section>
}
