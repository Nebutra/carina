import assert from 'node:assert/strict'
import test from 'node:test'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import fs from 'node:fs'
import { RpcClient } from '../dist/rpc.js'

const extension=fs.readFileSync(new URL('../src/extension.ts',import.meta.url),'utf8')
test('session inspection is paged and artifacts are bounded',()=>{assert.match(extension,/session\.items/);assert.match(extension,/MAX_TIMELINE_PAGES/);assert.match(extension,/artifact\.read/);assert.match(extension,/preview_utf8/);assert.match(extension,/showSaveDialog/);assert.doesNotMatch(extension,/function transcript\(es:Event\[\]\)/)})

const waitFor=(check,ms=3000)=>new Promise((resolve,reject)=>{const started=Date.now();const tick=()=>{if(check())return resolve();if(Date.now()-started>ms)return reject(new Error('condition timed out'));setTimeout(tick,10)};tick()})
test('rpc consumes notifications and reconnects after disconnect',async()=>{
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'carina-vscode-')),sock=path.join(dir,'d.sock');let connection,connections=0
  const server=net.createServer(c=>{connection=c;connections++;c.on('data',b=>{for(const line of b.toString().trim().split('\n')){const q=JSON.parse(line);c.write(JSON.stringify({jsonrpc:'2.0',id:q.id,result:q.method==='agent.view'?{needs_input:[],working:[{session_id:'s1',state:'running'}],completed:[]}:{}})+'\n')}})})
  await new Promise(r=>server.listen(sock,r));const client=new RpcClient(sock),states=[],notifications=[];client.onState(s=>states.push(s));client.onNotification((m,p)=>notifications.push([m,p]))
  assert.equal((await client.call('agent.view')).working[0].session_id,'s1');connection.write(JSON.stringify({jsonrpc:'2.0',method:'event',params:{session_id:'s1',type:'TaskUpdated'}})+'\n');await waitFor(()=>notifications.length===1);assert.equal(notifications[0][0],'event')
  connection.destroy();await waitFor(()=>states.includes('stale'));await waitFor(()=>connections>=2);assert.equal((await client.call('agent.view')).working.length,1);assert.equal(states.at(-1),'connected')
  client.dispose();await new Promise(r=>server.close(r));fs.rmSync(dir,{recursive:true})
})
