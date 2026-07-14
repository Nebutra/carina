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
