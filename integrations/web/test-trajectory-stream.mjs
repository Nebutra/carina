import assert from 'node:assert/strict'
import { TrajectoryStreamController } from './src-app/lib/trajectory-stream.ts'

const deferred = () => {
  let resolve
  let reject
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}

const createRpc = () => {
  const calls = []
  const subscriptions = []
  const call = (method, params = {}) => {
    calls.push({ method, params })
    if (method === 'session.events.stream') {
      const next = deferred()
      subscriptions.push(next)
      return next.promise
    }
    if (method === 'session.events.unsubscribe') return Promise.resolve({ unsubscribed: true })
    return Promise.reject(new Error(`unexpected RPC: ${method}`))
  }
  return { call, calls, subscriptions }
}

async function replayThenTailPreservesCursor() {
  const rpc = createRpc()
  const received = []
  const ready = []
  const errors = []
  const stream = new TrajectoryStreamController(rpc.call)
  stream.setHandlers({ onEvent: (event) => received.push(event), onReady: (value) => ready.push(value), onError: (error) => errors.push(error) })

  const attached = stream.select('session-1')
  assert.deepEqual(rpc.calls[0], {
    method: 'session.events.stream',
    params: { session_id: 'session-1', since: 0, event_mode: 'canonical' },
  })
  assert.equal(stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'session-1', raw_cursor: 1 }), true)
  assert.equal(stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'session-1', raw_cursor: 1 }), true)
  assert.equal(stream.handleNotification('event', { type: 'ToolCallStarted', session_id: 'session-1', raw_cursor: 2 }), true)
  assert.equal(received.length, 0, 'pre-ACK events must remain buffered')

  rpc.subscriptions[0].resolve({ subscription_id: 'sub-1', cursor: 2, replayed: 2, event_mode: 'canonical' })
  await attached
  assert.deepEqual(received.map(({ event, phase }) => [event.raw_cursor, phase]), [[1, 'catch-up'], [2, 'catch-up']])
  assert.equal(stream.cursorFor('session-1'), 2)
  assert.deepEqual(ready[0], { cursor: 2, replayed: 2, sessionId: 'session-1' })

  stream.handleNotification('event', { type: 'ToolCallStarted', session_id: 'session-1', raw_cursor: 2 })
  stream.handleNotification('event', { type: 'ToolCallCompleted', session_id: 'session-1', raw_cursor: 3 })
  assert.deepEqual(received.map(({ event }) => event.raw_cursor), [1, 2, 3], 'raw_cursor must be monotonic and deduplicated')
  assert.equal(stream.cursorFor('session-1'), 3)
  assert.deepEqual(errors, [])

  stream.handleDisconnect()
  const reattached = stream.select('session-1')
  assert.deepEqual(rpc.calls.at(-1), {
    method: 'session.events.stream',
    params: { session_id: 'session-1', since: 3, event_mode: 'canonical' },
  })
  stream.handleNotification('event', { type: 'TaskCompleted', session_id: 'session-1', raw_cursor: 4 })
  rpc.subscriptions[1].resolve({ subscription_id: 'sub-2', cursor: 4, replayed: 1, event_mode: 'canonical' })
  await reattached
  assert.deepEqual(received.map(({ event }) => event.raw_cursor), [1, 2, 3, 4])
  assert.equal(stream.cursorFor('session-1'), 4)
  assert.deepEqual(ready[1], { cursor: 4, replayed: 1, sessionId: 'session-1' })
}

async function switchingSessionsUnsubscribesAndIsolatesEvents() {
  const rpc = createRpc()
  const received = []
  const stream = new TrajectoryStreamController(rpc.call)
  stream.setHandlers({ onEvent: (event) => received.push(event) })

  const first = stream.select('session-1')
  rpc.subscriptions[0].resolve({ subscription_id: 'sub-old', cursor: 0, replayed: 0, event_mode: 'canonical' })
  await first
  const second = stream.select('session-2')
  assert.ok(rpc.calls.some(({ method, params }) => method === 'session.events.unsubscribe' && params.subscription_id === 'sub-old'))
  assert.equal(stream.handleNotification('event', { type: 'TaskCompleted', session_id: 'session-1', raw_cursor: 1 }), false)
  assert.equal(stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'session-2', raw_cursor: 1 }), true)
  rpc.subscriptions[1].resolve({ subscription_id: 'sub-new', cursor: 1, replayed: 1, event_mode: 'canonical' })
  await second
  assert.deepEqual(received.map(({ sessionId }) => sessionId), ['session-2'])
}

async function staleSubscribeCannotPolluteTheNewGeneration() {
  const rpc = createRpc()
  const received = []
  const stream = new TrajectoryStreamController(rpc.call)
  stream.setHandlers({ onEvent: (event) => received.push(event) })

  const stale = stream.select('session-1')
  const current = stream.select('session-2')
  rpc.subscriptions[0].resolve({ subscription_id: 'sub-stale', cursor: 9, replayed: 9, event_mode: 'canonical' })
  await stale
  assert.ok(rpc.calls.some(({ method, params }) => method === 'session.events.unsubscribe' && params.subscription_id === 'sub-stale'))
  stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'session-2', raw_cursor: 1 })
  rpc.subscriptions[1].resolve({ subscription_id: 'sub-current', cursor: 1, replayed: 1, event_mode: 'canonical' })
  await current
  assert.deepEqual(received.map(({ event }) => event.raw_cursor), [1])
  assert.equal(stream.cursorFor('session-1'), 0, 'a stale ACK must not advance another session cursor')
}

async function malformedFramesAreIgnoredAndInvalidAcksFailClosed() {
  const rpc = createRpc()
  const received = []
  const errors = []
  const stream = new TrajectoryStreamController(rpc.call)
  stream.setHandlers({ onEvent: (event) => received.push(event), onError: (error) => errors.push(error.message) })

  const attached = stream.select('session-1')
  assert.equal(stream.handleNotification('notice', { type: 'TaskSubmitted', session_id: 'session-1', raw_cursor: 1 }), false)
  assert.equal(stream.handleNotification('event', { session_id: 'session-1', raw_cursor: 1 }), false)
  assert.equal(stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'session-1', raw_cursor: -1 }), false)
  assert.equal(stream.handleNotification('event', { type: 'TaskSubmitted', session_id: 'another-session', raw_cursor: 1 }), false)
  rpc.subscriptions[0].resolve({ subscription_id: '', cursor: 0, replayed: 0, event_mode: 'canonical' })
  await attached
  assert.deepEqual(received, [])
  assert.match(errors[0], /invalid session\.events\.stream acknowledgement/)
}

await replayThenTailPreservesCursor()
await switchingSessionsUnsubscribesAndIsolatesEvents()
await staleSubscribeCannotPolluteTheNewGeneration()
await malformedFramesAreIgnoredAndInvalidAcksFailClosed()

console.log('React trajectory replay-tail stream: ok')
