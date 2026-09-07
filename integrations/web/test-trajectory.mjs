import assert from 'node:assert/strict'
import { deriveTrajectoryRows, durationMs, serializeDetails } from './src-app/lib/trajectory.ts'

const projected = deriveTrajectoryRows([
  { type: 'turn.started', task_id: 'turn-1', timestamp: '2026-01-01T00:00:00Z', details: { prompt: 'Ship it' } },
  { type: 'item.started', task_id: 'turn-1', item: { id: 'call-1', type: 'tool_call', status: 'running', task_id: 'turn-1', started_at: '2026-01-01T00:00:01Z', details: { tool: 'run', command: 'go test ./...' } } },
  { type: 'item.updated', task_id: 'turn-1', item: { id: 'call-1', type: 'tool_call', status: 'completed', task_id: 'turn-1', started_at: '2026-01-01T00:00:01Z', completed_at: '2026-01-01T00:00:04Z', details: { stdout: 'ok' } } },
  { type: 'turn.completed', task_id: 'turn-1', timestamp: '2026-01-01T00:00:05Z', details: {} },
])

assert.equal(projected.length, 1)
assert.equal(projected[0].rows.length, 2)
assert.equal(projected[0].rows[0].kind, 'user')
assert.equal(projected[0].rows[1].kind, 'tool')
assert.equal(durationMs(projected[0].rows[1]), 3000)
assert.match(serializeDetails({ token: 'never show', output: 'x'.repeat(5000) }), /redacted/)
console.log('React trajectory projection: ok')
