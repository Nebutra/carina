import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import test from 'node:test'

import { CarinaClient } from '../dist/index.js'

test('submitTask forwards an optional client submission id', async () => {
  const client = new CarinaClient('/tmp/not-used')
  let captured
  client.call = async (method, params) => {
    captured = { method, params }
    return { task_id: 'task_1', client_submission_id: params.client_submission_id }
  }
  const task = await client.submitTask('sess_1', 'work', 'sdk_request_1')
  assert.deepEqual(captured, {
    method: 'task.submit',
    params: { session_id: 'sess_1', prompt: 'work', client_submission_id: 'sdk_request_1' },
  })
  assert.equal(task.client_submission_id, 'sdk_request_1')
  client.close()
})

test('submitGoal forwards command success criteria as strings', async () => {
  const client = new CarinaClient('/tmp/not-used')
  let captured
  client.call = async (method, params) => {
    captured = { method, params }
    return { task_id: 'task_1', status: 'queued' }
  }
  await client.submitGoal('sess_1', 'verify', [{ kind: 'command_zero_exit', command: 'go test ./...' }])
  assert.equal(captured.method, 'task.submit')
  assert.equal(captured.params.success_criteria[0].command, 'go test ./...')
  assert.equal(Array.isArray(captured.params.success_criteria[0].command), false)
  client.close()
})

test('submitTask forwards media refs without changing legacy arguments', async () => {
  const client = new CarinaClient('/tmp/not-used')
  let captured
  client.call = async (method, params) => {
    captured = { method, params }
    return { task_id: 'task_1', input_media_refs: params.input_media_refs }
  }
  const ref = { artifact_id: 'a'.repeat(64), media_type: 'image/png', bytes: 3, origin: 'paste' }
  const task = await client.submitTask('sess_1', 'work', undefined, [ref])
  assert.deepEqual(captured.params.input_media_refs, [ref])
  assert.deepEqual(task.input_media_refs, [ref])
  client.close()
})

test('uploadArtifact sends ordered 512 KiB chunks', async () => {
  const client = new CarinaClient('/tmp/not-used')
  const content = Buffer.alloc(512 * 1024 + 7, 0x78)
  const digest = createHash('sha256').update(content).digest('hex')
  const calls = []
  client.call = async (method, params) => {
    calls.push({ method, params })
    if (!params.final) return { upload_id: params.upload_id, next_chunk_index: params.chunk_index + 1 }
    return { artifact_id: digest, media_type: 'image/png', bytes: content.length, origin: 'paste' }
  }
  const ref = await client.uploadArtifact('sess_1', 'upload_1', 'image/png', 'paste', content)
  assert.equal(calls.length, 2)
  assert.deepEqual(calls.map(({ params }) => [params.chunk_index, params.final, Buffer.from(params.content_base64, 'base64').length]), [[0, false, 512 * 1024], [1, true, 7]])
  assert.equal(ref.artifact_id, digest)
  client.close()
})
