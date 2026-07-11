import assert from 'node:assert/strict'
import test from 'node:test'
import { CarinaClient } from '../dist/index.js'

const socket = process.env.CARINA_CONFORMANCE_SOCKET
test('packaged daemon read-only conformance', { skip: !socket }, async () => {
  const client = new CarinaClient(socket, 5_000)
  try {
    const runtime = await client.initialize()
    assert.match(runtime.runtime_version, /^\d+\.\d+\.\d+$/)
    assert.equal(runtime.protocol_version.split('.')[0], '1')
    await client.doctor(); await client.listAgents(); await client.listWorkers(); await client.listWorkflows(); await client.listExtensions()
  } finally { client.close() }
})
