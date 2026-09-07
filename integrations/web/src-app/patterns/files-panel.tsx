import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, File, Folder, FolderOpen, Icon, IconButton, Input, Label, PanelRightClose, PanelRightOpen, RefreshCw, Search, X, cn } from '../design-system'
import type { WorkspaceFile } from '../lib/workspace'

export type FilesPanelProps = {
  workspace: string
  sessionId: string
  files: WorkspaceFile[]
  loading: boolean
  error: string
  truncated: boolean
  open: boolean
  onChooseWorkspace: () => void
  onRefresh: () => void
  onClose: () => void
  onOpen: () => void
}

function basename(value: string) {
  return value.split(/[\\/]/).filter(Boolean).pop() || 'workspace'
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

type TreeRow = { path: string; directory: boolean; size?: number; language?: string; binary?: boolean; large?: boolean }

function buildTreeRows(files: WorkspaceFile[], query: string): TreeRow[] {
  const needle = query.trim().toLowerCase()
  const matching = files.filter((entry) => !needle || entry.path.toLowerCase().includes(needle))
  const directories = new Map<string, number>()
  for (const file of matching) {
    const parts = file.path.split('/').filter(Boolean)
    for (let index = 1; index < parts.length; index += 1) {
      const directory = parts.slice(0, index).join('/')
      directories.set(directory, (directories.get(directory) || 0) + 1)
    }
  }
  const rows: TreeRow[] = [...directories.entries()].map(([path, count]) => ({ path: `${path}/`, directory: true, size: count }))
  rows.push(...matching.map((file) => ({ path: file.path, directory: false, size: file.size, language: file.language, binary: file.binary, large: file.large })))
  rows.sort((left, right) => left.path.localeCompare(right.path, undefined, { sensitivity: 'base' }))
  return rows.slice(0, 1200)
}

/** DSH-inspired file surface. The Gateway remains the source of truth; no local tree is invented. */
export function FilesPanel({ workspace, sessionId, files, loading, error, truncated, open, onChooseWorkspace, onRefresh, onClose, onOpen }: FilesPanelProps) {
  const [query, setQuery] = useState('')
  const previousOpen = useRef(open)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const openButtonRef = useRef<HTMLButtonElement>(null)
  const root = basename(workspace)
  const rows = useMemo(() => buildTreeRows(files, query), [files, query])
  const emptyMessage = !workspace ? 'Choose a workspace to browse files.' : !sessionId ? 'Start or select a session to inspect files.' : 'The Gateway has not indexed files for this workspace yet.'
  const queryHint = query.trim() ? `No files match “${query.trim()}”.` : emptyMessage

  useEffect(() => {
    if (previousOpen.current === open) return
    previousOpen.current = open
    const frame = requestAnimationFrame(() => (open ? closeButtonRef.current : openButtonRef.current)?.focus())
    return () => cancelAnimationFrame(frame)
  }, [open])

  return <aside className={cn('react-files-panel', !open && 'is-collapsed')} aria-label={open ? 'Files' : 'Files panel collapsed'}>
    <div className="react-files-expanded" aria-hidden={!open}>
      <header className="react-files-header">
        <span><Icon icon={FolderOpen} /><strong>Files</strong></span>
        <span className="react-files-actions">
          <IconButton icon={RefreshCw} label="Refresh files" onClick={onRefresh} disabled={loading || !sessionId} />
          <IconButton icon={FolderOpen} label="Choose workspace" onClick={onChooseWorkspace} />
          <IconButton ref={closeButtonRef} icon={PanelRightClose} label="Collapse files panel" onClick={onClose} />
        </span>
      </header>
      <div className="react-files-search">
        <Label className="sr-only" htmlFor="files-search">Search files by name</Label>
        <Input id="files-search" type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search files by name..." />
        {query && <IconButton icon={X} label="Clear file search" onClick={() => setQuery('')} />}
      </div>
      <div className="react-files-tree" role="list" aria-label="Workspace files">
        <div className="react-files-root" role="listitem"><Icon icon={Folder} /><strong>{root}</strong></div>
        {loading ? <div className="react-files-empty" role="status"><span className="react-spinner" aria-hidden="true" /><strong>Reading workspace files…</strong></div> : error ? <div className="react-files-empty" role="alert"><Icon icon={File} /><strong>{error}</strong><Button variant="outline" size="compact" onClick={onRefresh} disabled={!sessionId}>Try again</Button></div> : rows.length ? <div className="react-files-rows">{rows.map((row) => {
          const depth = row.path.split('/').filter(Boolean).length
          const label = row.path.split('/').filter(Boolean).at(-1) || row.path
          return <div className={cn('react-files-row', row.directory && 'directory')} key={row.path} role="listitem" aria-label={row.path} title={row.path} style={{ paddingLeft: `calc(var(--spacing-2) + ${Math.max(0, depth - 1)} * var(--spacing-3))` }}>
            <Icon icon={row.directory ? Folder : File} />
            <span className="react-files-row-name">{label}{row.directory ? '/' : ''}</span>
            {!row.directory && <small>{formatBytes(row.size || 0)}{row.language ? ` · ${row.language}` : ''}{row.binary ? ' · binary' : ''}{row.large ? ' · large' : ''}</small>}
          </div>
        })}</div> : <div className="react-files-empty" role="status"><Icon icon={workspace ? File : FolderOpen} /><strong>{queryHint}</strong>{!workspace && <Button variant="outline" size="compact" onClick={onChooseWorkspace}>Choose workspace</Button>}</div>}
        {truncated && !loading && <p className="react-files-truncated" role="status">Showing a bounded file window. Refresh after narrowing the workspace.</p>}
      </div>
    </div>
    <div className="react-files-collapsed" aria-hidden={open}><IconButton ref={openButtonRef} icon={PanelRightOpen} label="Open files panel" onClick={onOpen} tabIndex={open ? -1 : undefined} /></div>
  </aside>
}
