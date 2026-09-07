export type { GatewayRole as Role } from '../lib/harness-preferences'

export type AttachmentStatus = 'ready' | 'uploading'

export type Attachment = {
  file: File
  status: AttachmentStatus
}

export type { WorkspaceFile } from '../lib/workspace'

export type Session = {
  session_id: string
  title?: string
  summary?: string
  status?: string
  task_status?: string
  workspace_root?: string
  [key: string]: unknown
}
