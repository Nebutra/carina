import { Button, cn } from '../design-system'
import type { Session } from './types'

export type SessionListProps = {
  sessions: Session[]
  activeSession: Session | null
  onSelect: (session: Session | null) => Promise<void>
  status?: 'idle' | 'loading' | 'ready' | 'error'
  error?: string
  onRetry?: () => void
}

function sessionState(status: Session['status'] | Session['task_status']) {
  return status === 'failed' ? 'failed' : status === 'running' || status === 'working' ? 'running' : 'completed'
}

export function SessionList({ sessions, activeSession, onSelect, status = 'ready', error = '', onRetry }: SessionListProps) {
  return <div className="react-session-list" aria-label="Sessions" aria-busy={status === 'loading'}>
    {status === 'error' && <div className="react-session-empty react-session-error" role="alert">
      <strong>Sessions unavailable</strong>
      <span>{error || 'The Gateway roster could not be loaded.'}</span>
      {onRetry && <Button variant="ghost" size="compact" type="button" onClick={onRetry}>Try again</Button>}
    </div>}
    {status === 'loading' && !sessions.length && <div className="react-session-empty" role="status">
      <strong>Loading sessions</strong>
      <span>Reading the Gateway roster.</span>
    </div>}
    {sessions.length ? sessions.slice(0, 40).map((session) => {
      const active = activeSession?.session_id === session.session_id
      return <Button
        key={session.session_id}
        variant="ghost"
        className={cn('react-session-item', active && 'active')}
        onClick={() => void onSelect(session)}
        type="button"
        aria-current={active ? 'true' : undefined}
        aria-label={`${session.title || session.summary || session.session_id}${active ? ', selected' : ''}`}
      >
        <span className={cn('session-state', sessionState(session.status || session.task_status))} aria-hidden="true" />
        <span>
          <strong>{session.title || session.summary || session.session_id}</strong>
          <small>{session.status || session.task_status || 'ready'} · {session.workspace_root || ''}</small>
        </span>
      </Button>
    }) : status === 'ready' ? <div className="react-session-empty" role="status">
      <strong>No sessions yet</strong>
      <span>Your first run will appear here.</span>
    </div> : null}
  </div>
}
