import type { Dispatch, ReactNode, SetStateAction } from 'react'
import { Activity, Badge, Button, ChevronDown, Collapsible, CollapsibleContent, CollapsibleTrigger, Icon, IconButton, Input, Label, RefreshCw, ScrollArea, X, cn } from '../design-system'
import type { BadgeTone } from '../design-system'
import { durationMs, formatDuration, preview, serializeDetails } from '../lib/trajectory'
import type { TrajectoryRow, TrajectoryTurn } from '../lib/trajectory'
import type { Session } from './types'
import { EmptyState } from './empty-state'
import { MetricGroup } from './metric-group'
import { PageHeader } from './page-header'

const statusTone = (status: unknown): BadgeTone => ({ completed: 'success', running: 'accent', working: 'accent', failed: 'danger', timed_out: 'danger', requested: 'warning', pending: 'warning' }[String(status || '').toLowerCase()] as BadgeTone || 'neutral')
const kindLabel = (kind: string) => ({ user: 'USER', message: 'MESSAGE', tool: 'TOOL', subtool: 'SUBTOOL', stage: 'STAGE', error: 'ERROR', system: 'SYSTEM' }[kind] || 'EVENT')

export type TrajectoryViewProps = {
  activeSession: Session | null
  turns: TrajectoryTurn[]
  allRows: TrajectoryRow[]
  selectedRow: TrajectoryRow | undefined
  selected: string
  setSelected: Dispatch<SetStateAction<string>>
  query: string
  setQuery: Dispatch<SetStateAction<string>>
  collapsedTurns: Set<string>
  setCollapsedTurns: Dispatch<SetStateAction<Set<string>>>
  traceLoading: boolean
  traceError: string
  traceTruncated: boolean
  onRefresh: () => void
  onOpenRuns: () => void
}

export function TrajectoryView({ activeSession, turns, allRows, selectedRow, selected, setSelected, query, setQuery, collapsedTurns, setCollapsedTurns, traceLoading, traceError, traceTruncated, onRefresh, onOpenRuns }: TrajectoryViewProps) {
  if (!activeSession) return <section className="react-trajectory-empty"><Icon icon={Activity} /><strong>No session selected</strong><p>Choose a session from Runs or the workspace panel to inspect its durable event trail.</p><Button variant="outline" onClick={onOpenRuns}>Open runs</Button></section>
  if (traceLoading && !allRows.length) return <section className="react-trajectory-empty" role="status" aria-live="polite"><span className="react-spinner" aria-hidden="true" /><strong>Loading trajectory</strong><p>Reading the authoritative session projection…</p></section>
  if (traceError) return <section className="react-trajectory-empty" role="alert"><strong>Trajectory unavailable</strong><p>{traceError}</p><Button variant="outline" onClick={onRefresh}>Retry</Button></section>
  const durationTotal = allRows.reduce((sum, row) => sum + (durationMs(row) || 0), 0)
  const tools = allRows.filter((row) => row.kind === 'tool' || row.kind === 'subtool').length
  return <section className="react-trajectory" data-trajectory-view="true">
    <PageHeader eyebrow="OBSERVABILITY" title="Trajectory" lead="A turn-aware ledger of the durable events behind this session." actions={<><Badge>{preview(activeSession.session_id, 20)}</Badge><Button variant="outline" onClick={onRefresh} disabled={traceLoading} aria-label={traceLoading ? 'Refreshing trajectory' : 'Refresh trajectory'}>{traceLoading ? <span className="react-spinner" aria-hidden="true" /> : <Icon icon={RefreshCw} />} <span>Refresh</span></Button></>} />
    <MetricGroup className="react-trace-overview" metrics={[{ label: 'Turns', value: turns.length }, { label: 'Events', value: allRows.length }, { label: 'Tools', value: tools }, { label: 'Recorded time', value: durationTotal ? formatDuration({ details: { duration_ms: durationTotal } }) : '—' }]} />
    <div className="react-trace-toolbar"><Label className="react-search" htmlFor="trajectory-search"><span className="sr-only">Search trajectory</span><Input id="trajectory-search" type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search events, tools, or status" />{query && <IconButton icon={X} label="Clear search" onClick={() => setQuery('')} />}</Label><span>{turns.flatMap((turn) => turn.rows).length}{traceTruncated ? ' · older events bounded' : ''} events</span></div>
    <div className="react-trajectory-layout"><ScrollArea className="react-ledger">{turns.length ? turns.map((turn) => <TrajectoryTurn key={turn.id} turn={turn} selected={selected} setSelected={setSelected} collapsed={collapsedTurns.has(turn.id)} onToggle={() => setCollapsedTurns((current) => { const next = new Set(current); next.has(turn.id) ? next.delete(turn.id) : next.add(turn.id); return next })} />) : <EmptyState compact title="No matching events" description="Try a different search term." />}</ScrollArea><TrajectoryInspector row={selectedRow} /></div>
  </section>
}

type TrajectoryTurnProps = { turn: TrajectoryTurn; selected: string; setSelected: Dispatch<SetStateAction<string>>; collapsed: boolean; onToggle: () => void }
function TrajectoryTurn({ turn, selected, setSelected, collapsed, onToggle }: TrajectoryTurnProps) {
  return <Collapsible className={cn('react-turn', collapsed && 'collapsed')} open={!collapsed} onOpenChange={(open) => { if (open === collapsed) onToggle() }}><CollapsibleTrigger asChild><Button className="react-turn-heading" variant="ghost" type="button" aria-expanded={!collapsed}><Icon icon={ChevronDown} /><span><strong>Turn {turn.id === 'session' ? 'session' : turn.id}</strong>{turn.prompt && <small>{preview(turn.prompt, 120)}</small>}</span><span><Badge tone={statusTone(turn.status)}>{turn.status}</Badge><code>{formatDuration({ startedAt: turn.startedAt, completedAt: turn.completedAt })}</code></span></Button></CollapsibleTrigger><CollapsibleContent><div className="react-turn-rows">{turn.rows.map((row) => <TrajectoryRow key={`${row.turnId}:${row.id}`} row={row} selected={selected === `${row.turnId}:${row.id}`} onSelect={() => setSelected(`${row.turnId}:${row.id}`)} />)}</div></CollapsibleContent></Collapsible>
}

type TrajectoryRowProps = { row: TrajectoryRow; selected: boolean; onSelect: () => void }
function TrajectoryRow({ row, selected, onSelect }: TrajectoryRowProps) {
  return <Button className={cn('react-trace-row', selected && 'selected')} variant="ghost" data-trajectory-row="true" type="button" onClick={onSelect} aria-pressed={selected} aria-label={`${kindLabel(row.kind)}: ${row.summary || 'Event'}${selected ? ', selected' : ''}`}><span className={cn('react-trace-kind', `kind-${row.kind}`)}><i aria-hidden="true" />{kindLabel(row.kind)}</span><span className="react-trace-summary" title={row.content || row.summary}>{row.summary || 'Event'}</span><Badge tone={statusTone(row.status)}>{row.status || 'changed'}</Badge><code>{formatDuration(row)}</code></Button>
}

type TrajectoryInspectorProps = { row: TrajectoryRow | undefined }
function TrajectoryInspector({ row }: TrajectoryInspectorProps) {
  if (!row) return <aside className="react-inspector empty" aria-label="Event inspector"><strong>Select an event</strong><span>Inspect a bounded payload, result, and timing.</span></aside>
  const result = row.details?.result ?? row.details?.output ?? row.details?.stdout ?? row.details?.stderr ?? row.details?.error
  return <aside className="react-inspector" aria-label="Event inspector"><header><div><span className="eyebrow">EVENT INSPECTOR</span><h3>{kindLabel(row.kind)}</h3></div><Badge tone={statusTone(row.status)}>{row.status}</Badge></header><div className="react-inspector-id"><code>{row.id}</code></div><InspectorSection title="Summary"><p>{row.content || row.summary || 'No summary recorded.'}</p></InspectorSection>{result != null && <InspectorSection title="Result"><pre>{preview(result, 4000)}</pre></InspectorSection>}<InspectorSection title="Timing"><dl><div><dt>Started</dt><dd>{row.startedAt || '—'}</dd></div><div><dt>Completed</dt><dd>{row.completedAt || '—'}</dd></div><div><dt>Duration</dt><dd>{formatDuration(row)}</dd></div></dl></InspectorSection><InspectorSection title="Payload"><pre>{serializeDetails(row.details)}</pre></InspectorSection><InspectorSection title="Source"><p>{row.sourceEventId || 'Projection event'} · turn {row.turnId}</p></InspectorSection></aside>
}

function InspectorSection({ title, children }: { title: string; children: ReactNode }) { return <section><h4>{title}</h4>{children}</section> }
