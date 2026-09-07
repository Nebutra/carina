const MAX_PREVIEW = 280
const MAX_DETAIL = 12000

export type TrajectoryDetails = Record<string, unknown>

export interface SessionItem {
  type?: string
  turn_id?: string
  task_id?: string
  item_id?: string
  source_event_id?: string
  timestamp?: string
  details?: TrajectoryDetails
  item?: {
    id?: string
    type?: string
    status?: string
    task_id?: string
    started_at?: string
    completed_at?: string
    details?: TrajectoryDetails
    [key: string]: unknown
  }
  [key: string]: unknown
}

export interface TrajectoryRow {
  id: string
  kind: string
  status: string
  summary: string
  content: string
  details: TrajectoryDetails
  turnId: string
  startedAt: string
  completedAt: string
  timestamp: string
  sourceEventId: string
  synthetic: boolean
}

export interface TrajectoryTurn {
  id: string
  prompt: string
  status: string
  startedAt: string
  completedAt: string
  rows: TrajectoryRow[]
}

const text = (value: unknown) => {
  if (value == null) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  try { return JSON.stringify(value) } catch { return String(value) }
}

const time = (value: unknown) => {
  if (!value) return null
  const parsed = Date.parse(String(value))
  return Number.isFinite(parsed) ? parsed : null
}

export function kindFor(entry: SessionItem, item: SessionItem['item']) {
  const type = String(item?.type || entry?.type || '').toLowerCase()
  if (entry?.type === 'runtime.stage_changed' || type.includes('stage')) return 'stage'
  if (type.includes('error') || type.includes('violation') || type.includes('risk')) return 'error'
  if (type.includes('user') || type.includes('prompt')) return 'user'
  if (type.includes('assistant') || type.includes('agent_message') || type === 'message') return 'message'
  if (type.includes('subtool') || type.includes('subagent')) return 'subtool'
  if (type.includes('tool') || type.includes('command') || type.includes('execution')) return 'tool'
  if (type.includes('compact') || type.includes('policy') || type.includes('approval') || type.includes('question')) return 'system'
  return 'system'
}

function detailsFor(entry: SessionItem, item: SessionItem['item']): TrajectoryDetails {
  return { ...(entry?.details || {}), ...(item?.details || {}) }
}

function summaryFor(details: TrajectoryDetails, kind: string) {
  const keys = kind === 'user'
    ? ['prompt', 'user_prompt', 'text', 'content', 'message', 'summary']
    : kind === 'message'
      ? ['text', 'content', 'message', 'summary']
      : kind === 'tool' || kind === 'subtool'
        ? ['intent', 'command', 'tool', 'path', 'summary', 'result', 'output', 'stdout', 'stderr', 'error', 'last_chunk']
        : ['stage', 'summary', 'reason', 'message', 'error', 'status']
  for (const key of keys) {
    const value = text(details?.[key]).replace(/\s+/g, ' ').trim()
    if (value) return value
  }
  return ''
}

export function preview(value: unknown, limit = MAX_PREVIEW) {
  const valueText = String(value || '').replace(/\s+/g, ' ').trim()
  return valueText.length <= limit ? valueText : `${valueText.slice(0, limit - 1).trimEnd()}…`
}

export function deriveTrajectoryRows(items: SessionItem[] = []): TrajectoryTurn[] {
  const groups: TrajectoryTurn[] = []
  const turns = new Map<string, TrajectoryTurn>()
  const itemRows = new Map<string, TrajectoryRow>()
  const realUsers = new Set<string>()
  const ensureTurn = (id: string): TrajectoryTurn => {
    const existing = turns.get(id)
    if (existing) return existing
    const group: TrajectoryTurn = { id, prompt: '', status: 'running', startedAt: '', completedAt: '', rows: [] }
    turns.set(id, group)
    groups.push(group)
    return group
  }
  const mergeTime = (current: string, next: string | undefined, earliest: boolean) => {
    if (!next) return current
    if (!current) return next
    const a = time(current), b = time(next)
    if (a == null || b == null) return earliest ? current : next
    return earliest ? (b < a ? next : current) : (b > a ? next : current)
  }

  items.forEach((entry, index) => {
    const item = entry?.item
    const turnId = String(entry?.turn_id || entry?.task_id || item?.task_id || 'session')
    const group = ensureTurn(turnId)
    const eventType = String(entry?.type || '').toLowerCase()
    const details = detailsFor(entry, item)
    if (eventType === 'turn.started') {
      group.prompt = text(details.prompt || details.user_prompt || details.intent).trim()
      group.startedAt = entry.timestamp || group.startedAt
      return
    }
    if (eventType === 'turn.completed' || eventType === 'turn.failed') {
      group.status = eventType === 'turn.failed' ? 'failed' : 'completed'
      group.completedAt = entry.timestamp || group.completedAt
      if (!group.prompt) group.prompt = text(details.prompt || details.user_prompt).trim()
      return
    }
    if (item) {
      const kind = kindFor(entry, item)
      const id = String(item.id || entry.item_id || entry.source_event_id || `item-${index}`)
      const key = `${turnId}:${id}`
      let row = itemRows.get(key)
      if (!row) {
        row = { id, kind, status: item.status || 'changed', summary: '', content: '', details: {}, turnId, startedAt: '', completedAt: '', timestamp: entry.timestamp || '', sourceEventId: '', synthetic: false }
        itemRows.set(key, row)
        group.rows.push(row)
      }
      row.kind = kind
      row.status = item.status || row.status
      row.details = { ...row.details, ...details }
      row.startedAt = mergeTime(row.startedAt, item.started_at || entry.timestamp, true)
      row.completedAt = mergeTime(row.completedAt, item.completed_at || (eventType === 'item.completed' ? entry.timestamp : ''), false)
      row.timestamp ||= entry.timestamp || ''
      row.sourceEventId = entry.source_event_id || row.sourceEventId
      row.content = summaryFor(row.details, row.kind)
      row.summary = preview(row.content || item.type || entry.type || 'Event')
      if (kind === 'user') realUsers.add(turnId)
      return
    }
    if (entry?.type === 'runtime.stage_changed') {
      const summary = summaryFor(details, 'stage') || 'runtime'
      group.rows.push({ id: String(entry.item_id || entry.source_event_id || `stage-${index}`), kind: 'stage', status: text(details.status || 'changed'), summary: preview(summary), content: summary, details, turnId, startedAt: entry.timestamp || '', completedAt: '', timestamp: entry.timestamp || '', sourceEventId: entry.source_event_id || '', synthetic: false })
    }
  })

  for (const group of groups) {
    if (group.id !== 'session' && group.prompt && !realUsers.has(group.id)) {
      group.rows.unshift({ id: `turn:${group.id}:user`, kind: 'user', status: 'completed', summary: preview(group.prompt), content: group.prompt, details: { prompt: group.prompt }, turnId: group.id, startedAt: group.startedAt, completedAt: group.startedAt, timestamp: group.startedAt, sourceEventId: '', synthetic: true })
    }
  }
  return groups.filter((group) => group.rows.length || group.prompt || group.id !== 'session')
}

export function durationMs(row: Partial<TrajectoryRow> | null | undefined) {
  const start = time(row?.startedAt), end = time(row?.completedAt)
  if (start != null && end != null && end >= start) return end - start
  const explicit = Number(row?.details?.duration_ms)
  return Number.isFinite(explicit) && explicit >= 0 ? explicit : null
}

export function formatDuration(row: Partial<TrajectoryRow> | null | undefined) {
  const duration = durationMs(row)
  if (duration == null) return '—'
  if (duration < 1000) return `${Math.round(duration)}ms`
  if (duration < 60000) return `${(duration / 1000).toFixed(1)}s`
  return `${Math.floor(duration / 60000)}m ${Math.round((duration % 60000) / 1000)}s`
}

function bounded(value: unknown, depth = 0): unknown {
  if (depth > 4) return '[depth limited]'
  if (typeof value === 'string') return value.length > 1600 ? `${value.slice(0, 1599)}…` : value
  if (value == null || typeof value === 'number' || typeof value === 'boolean') return value
  if (Array.isArray(value)) return value.slice(0, 32).map((entry) => bounded(entry, depth + 1))
  if (typeof value === 'object') return Object.fromEntries(Object.entries(value).slice(0, 48).map(([key, entry]) => [key, /token|secret|password|authorization|api[_-]?key/i.test(key) ? '[redacted]' : bounded(entry, depth + 1)]))
  return String(value)
}

export function serializeDetails(details: TrajectoryDetails = {}) {
  let value
  try { value = JSON.stringify(bounded(details), null, 2) } catch { value = '{}' }
  return value.length > MAX_DETAIL ? `${value.slice(0, MAX_DETAIL - 1)}…` : value
}
