import assert from 'node:assert/strict'
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
