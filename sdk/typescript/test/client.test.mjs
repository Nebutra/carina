import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { CarinaClient, CarinaRpcError, CarinaTransportError, compatibleRuntimeVersion } from '../dist/index.js'

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
    if (request.method === 'session.review') result = { session_id:'s1',projection_version:'1.0.0',source_cursor:'cp1.payload.signature',state:'completed',intent:'ship',success_criteria:[{kind:'command'}],changes:[],commands:[],tools:[],checks:[],diagnostics:[],policy_decisions:[],questions:[],conflicts:[],risk_and_policy:[],artifact_ids:[],rollback:{available:false,patch_ids:[]},stats:{} }
    if (request.method === 'session.items') result = { data:[{type:'turn.started',session_id:'s1',task_id:'t1'}],next_cursor:'cp1.payload.signature',projection_version:'1.0.0' }
    if (request.method === 'usage.cost') result = { providers: [], totals: {}, estimated: false }
    if (request.method === 'task.steer') result = { queued: true, task_id: request.params.task_id, status: 'running' }
    if (request.method === 'task.resume') result = { task_id: request.params.task_id, session_id: 's1', status: 'running' }
    const checkpoint = { checkpoint_id:'t1:1:9',created_at:'2026-07-14T00:00:00Z',sequence:'00000000000000000009',task_id:'t1',session_id:'s1',turn:1,applied_patches:[] }
    if (request.method === 'session.checkpoint.preview') result = { checkpoint,conversation_turns:1,rollback_patches:[],will_resume:'paused' }
    if (request.method === 'session.checkpoint.summarize') result = { checkpoint_id:checkpoint.checkpoint_id,task_id:'t1',turn:1,recent:[] }
    if (request.method === 'session.checkpoint.restore') result = { restored:true,checkpoint_id:checkpoint.checkpoint_id,task_id:'t1',turn:1,rolled_back:[],status:'paused',idempotent:true,reconciliation_required:false,journal_cleanup_pending:false }
    if (request.method === 'task.user.answer') result = { question_id: request.params.question_id, accepted: true, value: request.params.value }
    if (request.method === 'session.events.stream') result = { subscription_id: 'sub_1', cursor: 0, replayed: 0 }
    if (request.method === 'session.events.unsubscribe') result = { unsubscribed: true }
    socket.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }) + '\n')
    if (request.method === 'session.events.stream') {
      setImmediate(() => socket.write(JSON.stringify({
        jsonrpc: '2.0', method: 'event', params: { session_id: 's1', type: 'ModelResponded', timestamp: 'now' },
      }) + '\n'))
    }
  }, async (socketPath) => {
    const client = new CarinaClient(socketPath, 500)
    assert.equal(compatibleRuntimeVersion, '0.6.2')
    assert.equal((await client.attachSession('s1', 3)).cursor, 7)
    assert.equal((await client.forkSession('s1')).session_id, 'child')
    assert.equal((await client.reviewSession('s1')).projection_version, '1.0.0')
    assert.equal((await client.listSessionItems('s1','',1)).data[0].task_id,'t1')
    assert.equal((await client.cost('s1')).estimated, false)
    assert.equal((await client.steerTask('t1', 'continue')).queued, true)
    assert.equal((await client.resumeTask('t1')).status, 'running')
    assert.equal((await client.previewCheckpoint('s1', 't1:1:9')).checkpoint.sequence, '00000000000000000009')
    assert.equal((await client.summarizeCheckpoint('s1', 't1:1:9')).turn, 1)
    assert.equal((await client.restoreCheckpoint('s1', 't1:1:9', true)).idempotent, true)
    assert.equal((await client.answerQuestion('q1', 'yes')).accepted, true)
    let resolveEvent
    const event = new Promise((resolve) => { resolveEvent = resolve })
    const stop = await client.streamSessionEvents('s1', resolveEvent)
    assert.equal((await event).type, 'ModelResponded')
    await stop()
    client.close()
  })
  assert.deepEqual(methods, [
    'session.attach', 'session.fork', 'session.review', 'session.items', 'usage.cost', 'task.steer', 'task.resume', 'session.checkpoint.preview', 'session.checkpoint.summarize', 'session.checkpoint.restore', 'task.user.answer', 'session.events.stream', 'session.events.unsubscribe',
  ])
})

test('resolve approval uses canonical approve param', async () => {
  let params
  await withServer((request, socket) => {
    params = request.params
    socket.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: { resolved: true } }) + '\n')
  }, async (socketPath) => {
    const client = new CarinaClient(socketPath, 500)
    await client.resolveApproval('decision-1', true, 'sdk', 'once')
    client.close()
  })
  assert.equal(params.approve, true)
  assert.equal('allow' in params, false)
})

test('event subscription preserves raw cursor contract',async()=>{let params;await withServer((request,socket)=>{params=request.params;socket.write(JSON.stringify({jsonrpc:'2.0',id:request.id,result:{subscription_id:'sub',cursor:17,replayed:2,event_mode:'canonical'}})+'\n')},async socketPath=>{const client=new CarinaClient(socketPath,500);const result=await client.subscribeSessionEventsFrom('s',11,'canonical');assert.equal(result.cursor,17);assert.equal(result.event_mode,'canonical');client.close()});assert.equal(params.since,11)})

test('disconnect rejects every pending call immediately', async () => {
  await withServer((_request, socket) => socket.destroy(), async (socketPath) => {
    const client = new CarinaClient(socketPath, 5_000)
    await assert.rejects(client.call('daemon.status'), CarinaTransportError)
    client.close()
  })
})

test('typed cursor recovery data survives transport',async()=>{await withServer((request,socket)=>socket.write(JSON.stringify({jsonrpc:'2.0',id:request.id,error:{code:-32010,message:'cursor_expired',data:{code:'cursor_expired',projection_version:'1.0.0',recovery:'snapshot',snapshot_method:'session.items',earliest_cursor:'cp1.x.y'}}})+'\n'),async socketPath=>{const client=new CarinaClient(socketPath,500);await assert.rejects(client.listSessionItems('s','bad',1),error=>error instanceof CarinaRpcError&&error.code===-32010&&error.data.code==='cursor_expired');client.close()})})

test('call timeout bounds an unresponsive daemon', async () => {
  await withServer(() => {}, async (socketPath) => {
    const client = new CarinaClient(socketPath, 30)
    await assert.rejects(client.call('daemon.status'), /timed out after 30ms/)
    client.close()
  })
})

test('initialize rejects incompatible protocol and missing lifecycle capability', async () => {
  for (const result of [
    { runtime_version: 'x', protocol_version: '2.0.0', capabilities: { tool_call_lifecycle: true } },
    { runtime_version: 'x', protocol_version: '1.2.0', capabilities: {} },
    { runtime_version: 'x', protocol_version: '1.2.0', capabilities: { tool_call_lifecycle: true, event_schema_version: '0.4.0' } },
  ]) {
    await withServer((request, socket) => socket.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }) + '\n'), async (socketPath) => {
      const client = new CarinaClient(socketPath, 500)
      await assert.rejects(client.initialize(), /incompatible runtime protocol|lacks required|incompatible event schema/)
      client.close()
    })
  }
})

// 0.6.1 remains a deliberate patch-level compatibility fixture.
test('high-level thread run negotiates and forwards full JSON Schema', async () => {
  const methods=[];let submitted
  await withServer((request,socket)=>{methods.push(request.method);let result={};if(request.method==='runtime.initialize')result={runtime_version:'0.6.1',protocol_version:'1.2.0',projection_version:'1.0.0',capabilities:{tool_call_lifecycle:true,event_schema_version:'0.3.0'}};if(request.method==='session.create')result={session_id:'s',workspace_id:'w',workspace_root:'/tmp',status:'active',permission_profile:'safe-edit',created_at:'now'};if(request.method==='task.submit'){submitted=request.params;result={task_id:'t',session_id:'s',workspace_id:'w',status:'queued',user_prompt:'status'}};if(request.method==='task.result')result={task_id:'t',session_id:'s',workspace_id:'w',status:'completed',user_prompt:'status',summary:'{"status":"ok"}'};socket.write(JSON.stringify({jsonrpc:'2.0',id:request.id,result})+'\n')},async socketPath=>{const client=new CarinaClient(socketPath,500);const thread=await client.startThread({workingDirectory:'/tmp'});const schema={type:'object',properties:{status:{type:'string'}},required:['status'],additionalProperties:false};const turn=await thread.run('status',{outputSchema:schema,pollIntervalMs:1});assert.deepEqual(turn.structuredOutput,{status:'ok'});client.close()})
  assert.deepEqual(submitted.output_schema,{type:'object',properties:{status:{type:'string'}},required:['status'],additionalProperties:false});assert.deepEqual(methods,['runtime.initialize','session.create','task.submit','task.result'])
})
