import type { Dispatch, RefObject, SetStateAction } from 'react'
import { ArrowUp, Button, FileImage, Icon, IconButton, Input, Label, Paperclip, RefreshCw, Textarea, X, cn } from '../design-system'
import type { Attachment } from './types'

export type ComposerProps = {
  prompt: string
  setPrompt: Dispatch<SetStateAction<string>>
  attachments: Attachment[]
  addAttachments: (files: FileList | null) => void
  removeAttachment: (index: number) => void
  fileInputRef: RefObject<HTMLInputElement | null>
  onSubmit: () => Promise<void>
  blockedReason: string
  onOpenConnection: () => void
  submitting: boolean
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export function Composer({ prompt, setPrompt, attachments, addAttachments, removeAttachment, fileInputRef, onSubmit, blockedReason, onOpenConnection, submitting }: ComposerProps) {
  const uploading = attachments.some((entry) => entry.status === 'uploading')
  const canRun = !blockedReason && Boolean(prompt.trim()) && !submitting && !uploading

  return <div className="react-composer" aria-busy={submitting || uploading}>
    <Label className="sr-only" htmlFor="prompt-input">Prompt</Label>
    <Textarea
      id="prompt-input"
      value={prompt}
      onChange={(event) => setPrompt(event.target.value)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
          event.preventDefault()
          if (canRun) void onSubmit()
        }
      }}
      placeholder="Describe what you want to build... / commands, @ files or sessions"
      disabled={submitting}
      aria-describedby="composer-status"
    />
    {attachments.length > 0 && <div className="react-attachment-list" aria-label="Attached images">
      {attachments.map((entry, index) => <div className="react-attachment" key={`${entry.file.name}-${entry.file.lastModified}`}>
        <Icon icon={FileImage} />
        <span><strong>{entry.file.name}</strong><small>{formatBytes(entry.file.size)} · {entry.status}</small></span>
        <IconButton icon={X} label={`Remove ${entry.file.name}`} onClick={() => removeAttachment(index)} disabled={submitting} />
      </div>)}
    </div>}
    <div className="react-composer-footer">
      <span className="react-composer-tools">
        <Input ref={fileInputRef} className="react-file-input" type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple disabled={submitting} tabIndex={-1} aria-hidden="true" onChange={(event) => { addAttachments(event.target.files); event.target.value = '' }} />
        <Button className="react-attach-button" variant="ghost" size="compact" type="button" onClick={() => fileInputRef.current?.click()} disabled={submitting} aria-label="Attach images" title="Attach images"><Icon icon={Paperclip} /><span>{attachments.length ? `${attachments.length} attached` : 'Attach images'}</span></Button>
        {blockedReason ? <Button className="react-run-state" variant="ghost" size="compact" id="composer-status" type="button" onClick={blockedReason.startsWith('Switch to Operator') ? onOpenConnection : undefined} disabled={!blockedReason.startsWith('Switch to Operator') || submitting} aria-live="polite">{blockedReason}</Button> : <small id="composer-status" aria-live="polite">{submitting ? 'Submitting run…' : uploading ? 'Uploading attachments…' : 'PNG, JPEG, GIF, WebP · 4 MiB total'}</small>}
      </span>
      <Button variant="primary" size="icon" onClick={() => void onSubmit()} disabled={!canRun} aria-label={submitting ? 'Submitting prompt' : 'Run prompt'} title={blockedReason || (submitting ? 'Submitting prompt' : 'Run prompt')}>
        {submitting ? <Icon icon={RefreshCw} className={cn('react-submit-icon', 'react-icon-spin')} /> : <Icon icon={ArrowUp} />}
      </Button>
    </div>
  </div>
}
