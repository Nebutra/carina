import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { CarinaClient, CarinaTransportError, compatibleRuntimeVersion } from '../dist/index.js'

async function withServer(onRequest, run) {
  const dir = await mkdtemp(join(tmpdir(), 'carina-sdk-ts-'))
  const socketPath = join(dir, 'daemon.sock')
  const server = createServer((socket) => {
    let buffer = ''
    socket.on('data', (chunk) => {
      buffer += chunk.toString('utf8')
      let newline
      while ((newline = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, newline)
        buffer = buffer.slice(newline + 1)
        if (line.trim()) onRequest(JSON.parse(line), socket)
      }
    })
  })
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(socketPath, resolve)
  })
  try {
    await run(socketPath)
  } finally {
    await new Promise((resolve) => server.close(resolve))
    await rm(dir, { recursive: true, force: true })
  }
}

test('typed parity wrappers and event subscription use canonical RPC methods', async () => {
  const methods = []
  await withServer((request, socket) => {
    methods.push(request.method)
    let result = {}
    if (request.method === 'session.attach') result = { events: [], from: request.params.since, cursor: 7 }
    if (request.method === 'session.fork') result = { session_id: 'child' }
    if (request.method === 'usage.cost') result = { providers: [], totals: {}, estimated: false }
    if (request.method === 'task.steer') result = { queued: true, task_id: request.params.task_id, status: 'running' }
    if (request.method === 'task.user.answer') result = { question_id: request.params.question_id, accepted: true, value: request.params.value }
    socket.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }) + '\n')
    if (request.method === 'session.events.stream') {
      setImmediate(() => socket.write(JSON.stringify({
        jsonrpc: '2.0', method: 'event', params: { session_id: 's1', type: 'ModelResponded', timestamp: 'now' },
      }) + '\n'))
    }
  }, async (socketPath) => {
    const client = new CarinaClient(socketPath, 500)
    assert.equal(compatibleRuntimeVersion, '0.6.1')
    assert.equal((await client.attachSession('s1', 3)).cursor, 7)
    assert.equal((await client.forkSession('s1')).session_id, 'child')
    assert.equal((await client.cost('s1')).estimated, false)
    assert.equal((await client.steerTask('t1', 'continue')).queued, true)
    assert.equal((await client.answerQuestion('q1', 'yes')).accepted, true)
    const event = new Promise((resolve) => client.streamSessionEvents('s1', resolve))
    assert.equal((await event).type, 'ModelResponded')
    client.close()
  })
  assert.deepEqual(methods, [
    'session.attach', 'session.fork', 'usage.cost', 'task.steer', 'task.user.answer', 'session.events.stream',
  ])
})

test('disconnect rejects every pending call immediately', async () => {
  await withServer((_request, socket) => socket.destroy(), async (socketPath) => {
    const client = new CarinaClient(socketPath, 5_000)
    await assert.rejects(client.call('daemon.status'), CarinaTransportError)
    client.close()
  })
})

test('call timeout bounds an unresponsive daemon', async () => {
  await withServer(() => {}, async (socketPath) => {
    const client = new CarinaClient(socketPath, 30)
    await assert.rejects(client.call('daemon.status'), /timed out after 30ms/)
    client.close()
  })
})
