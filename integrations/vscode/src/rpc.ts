import { createConnection, Socket } from 'node:net'

export class RpcClient {
  private socket?: Socket
  private id = 0
  private buffer = ''
  private pending = new Map<number, { ok(v: unknown): void; fail(e: Error): void; timer: NodeJS.Timeout }>()
  constructor(readonly path: string, readonly timeoutMs = 10_000) {}
  async connect(): Promise<void> {
    if (this.socket && !this.socket.destroyed) return
    await new Promise<void>((ok, fail) => {
      const socket = createConnection(this.path)
      socket.once('error', fail)
      socket.once('connect', () => { socket.removeListener('error', fail); this.socket = socket; socket.on('data', b => this.read(b.toString())); socket.on('error', e => this.close(e)); socket.on('close', () => this.close(new Error('daemon disconnected'))); ok() })
    })
  }
  async call<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    await this.connect(); const socket = this.socket; if (!socket) throw new Error('daemon disconnected')
    const id = ++this.id
    return new Promise<T>((ok, fail) => {
      const timer = setTimeout(() => { this.pending.delete(id); fail(new Error(`${method} timed out`)) }, this.timeoutMs)
      this.pending.set(id, { ok: v => ok(v as T), fail, timer })
      socket.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n')
    })
  }
  dispose(): void { this.socket?.destroy(); this.close(new Error('client disposed')) }
  private read(chunk: string): void { this.buffer += chunk; for (;;) { const i = this.buffer.indexOf('\n'); if (i < 0) return; const line = this.buffer.slice(0, i); this.buffer = this.buffer.slice(i + 1); if (!line) continue; const msg = JSON.parse(line); const p = this.pending.get(msg.id); if (!p) continue; this.pending.delete(msg.id); clearTimeout(p.timer); msg.error ? p.fail(new Error(msg.error.message)) : p.ok(msg.result) } }
  private close(error: Error): void { for (const p of this.pending.values()) { clearTimeout(p.timer); p.fail(error) }; this.pending.clear(); this.socket = undefined }
}
