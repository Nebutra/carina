import { createConnection, Socket } from 'node:net'

export type ConnectionState = 'connecting' | 'connected' | 'stale' | 'disposed'
type Pending = { ok(v: unknown): void; fail(e: Error): void; timer: NodeJS.Timeout }

export class RpcClient {
  private socket?: Socket
  private id = 0
  private buffer = ''
  private disposed = false
  private connecting?: Promise<void>
  private reconnectTimer?: NodeJS.Timeout
  private backoffMs = 250
  private pending = new Map<number, Pending>()
  private notifications = new Set<(method: string, params: unknown) => void>()
  private states = new Set<(state: ConnectionState, error?: Error) => void>()
  constructor(readonly path: string, readonly timeoutMs = 10_000) {}
  onNotification(fn: (method: string, params: unknown) => void): () => void { this.notifications.add(fn); return () => this.notifications.delete(fn) }
  onState(fn: (state: ConnectionState, error?: Error) => void): () => void { this.states.add(fn); return () => this.states.delete(fn) }
  async connect(): Promise<void> {
    if (this.disposed) throw new Error('client disposed')
    if (this.socket && !this.socket.destroyed) return
    if (this.connecting) return this.connecting
    this.emitState('connecting')
    this.connecting = new Promise<void>((ok, fail) => {
      const socket = createConnection(this.path)
      const initialFailure = (error: Error) => { socket.destroy(); fail(error) }
      socket.once('error', initialFailure)
      socket.once('connect', () => {
        if (this.disposed) { socket.destroy(); fail(new Error('client disposed')); return }
        socket.removeListener('error', initialFailure); this.socket = socket; this.buffer = ''; this.backoffMs = 250
        socket.on('data', b => this.read(b.toString())); socket.on('error', e => this.disconnect(socket, e)); socket.on('close', () => this.disconnect(socket, new Error('daemon disconnected')))
        this.emitState('connected'); ok()
      })
    }).finally(() => { this.connecting = undefined })
    try { await this.connecting } catch (e) { if (!this.disposed) { this.emitState('stale', asError(e)); this.scheduleReconnect() } throw e }
  }
  async call<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    await this.connect(); const socket = this.socket; if (!socket || socket.destroyed) throw new Error('daemon disconnected')
    const id = ++this.id
    return new Promise<T>((ok, fail) => {
      const timer = setTimeout(() => { this.pending.delete(id); fail(new Error(`${method} timed out`)) }, this.timeoutMs)
      this.pending.set(id, { ok: v => ok(v as T), fail, timer }); socket.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n')
    })
  }
  dispose(): void { this.disposed = true; if (this.reconnectTimer) clearTimeout(this.reconnectTimer); const socket=this.socket;this.socket=undefined;socket?.destroy(); this.failPending(new Error('client disposed')); this.emitState('disposed') }
  private read(chunk: string): void { this.buffer += chunk; for (;;) { const i=this.buffer.indexOf('\n'); if(i<0)return; const line=this.buffer.slice(0,i);this.buffer=this.buffer.slice(i+1);if(!line)continue;let msg:any;try{msg=JSON.parse(line)}catch{continue} if(msg.id!==undefined){const p=this.pending.get(msg.id);if(!p)continue;this.pending.delete(msg.id);clearTimeout(p.timer);msg.error?p.fail(new Error(msg.error.message)):p.ok(msg.result)}else if(msg.method){for(const fn of this.notifications)fn(msg.method,msg.params)}} }
  private disconnect(socket: Socket,error: Error): void { if(this.socket!==socket)return;this.socket=undefined;if(this.disposed)return;this.failPending(error);this.emitState('stale',error);this.scheduleReconnect() }
  private failPending(error: Error): void { for(const p of this.pending.values()){clearTimeout(p.timer);p.fail(error)}this.pending.clear() }
  private scheduleReconnect(): void { if(this.disposed||this.reconnectTimer)return;const delay=this.backoffMs;this.backoffMs=Math.min(this.backoffMs*2,10_000);this.reconnectTimer=setTimeout(()=>{this.reconnectTimer=undefined;void this.connect().catch(()=>{})},delay) }
  private emitState(state: ConnectionState,error?: Error): void { for(const fn of this.states)fn(state,error) }
}
const asError=(e:unknown):Error=>e instanceof Error?e:new Error(String(e))
