export type WorkspaceFile = {
  path: string
  size: number
  binary: boolean
  large: boolean
  language: string
  mtime: number
}

export type WorkspaceTreeResult = {
  files: WorkspaceFile[]
  truncated: boolean
}

function numberField(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** Decode the daemon's bounded harness.workspace.tree projection at the RPC boundary. */
export function decodeWorkspaceTree(value: unknown): WorkspaceTreeResult {
  const payload = Array.isArray(value) ? value : (value && typeof value === 'object' ? (value as { data?: unknown; files?: unknown; truncated?: unknown }) : {})
  const rawFiles = Array.isArray(payload) ? payload : Array.isArray(payload.data) ? payload.data : Array.isArray(payload.files) ? payload.files : []
  const files = rawFiles.flatMap((entry): WorkspaceFile[] => {
    if (!entry || typeof entry !== 'object') return []
    const record = entry as Record<string, unknown>
    const path = typeof record.path === 'string' ? record.path.replaceAll('\\', '/') : ''
    if (!path || path === '.' || path.startsWith('../') || path.startsWith('/')) return []
    return [{
      path,
      size: numberField(record.size),
      binary: record.binary === true,
      large: record.large === true,
      language: typeof record.language === 'string' ? record.language : '',
      mtime: numberField(record.mtime),
    }]
  })
  return { files, truncated: payload && !Array.isArray(payload) && payload.truncated === true }
}
