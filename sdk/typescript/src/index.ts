/**
 * Pi-OS TypeScript SDK (Phase 0).
 *
 * A thin JSON-RPC 2.0 client for the pi-daemon unix socket. The Agent
 * Surface (and any IDE/CI integration) talks to the runtime exclusively
 * through this protocol — see protocol/jsonrpc/methods.json.
 */
import { createConnection, type Socket } from 'node:net'
import { homedir } from 'node:os'
import { join } from 'node:path'

export interface Session {
  session_id: string
  workspace_id: string
  workspace_root: string
  status: 'active' | 'paused' | 'closed'
  permission_profile: string
  created_at: string
}

export interface Task {
  task_id: string
  session_id: string
  workspace_id: string
  status: string
  user_prompt: string
  created_at: string
  updated_at: string
  risk_level: number
}

export interface PiEvent {
  event_id: string
  session_id: string
  task_id?: string
  type: string
  timestamp: string
  payload?: Record<string, unknown>
  permission_decision_id?: string
}

interface RpcError {
  code: number
  message: string
}

export const defaultSocketPath = (): string => join(homedir(), '.pi-os', 'daemon.sock')

export class PiClient {
  private socket: Socket | null = null
  private nextId = 0
  private buffer = ''
  private pending = new Map<number, { resolve: (v: unknown) => void; reject: (e: Error) => void }>()

  constructor(private readonly socketPath: string = defaultSocketPath()) {}

  async connect(): Promise<void> {
    if (this.socket) return
    await new Promise<void>((resolve, reject) => {
      const socket = createConnection(this.socketPath)
      socket.once('connect', () => {
        this.socket = socket
        socket.on('data', (chunk) => this.onData(chunk.toString('utf8')))
        resolve()
      })
      socket.once('error', (err) =>
        reject(new Error(`cannot reach pi-daemon at ${this.socketPath}: ${err.message}`)),
      )
    })
  }

  async call<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    await this.connect()
    const id = ++this.nextId
    const payload = JSON.stringify({ jsonrpc: '2.0', id, method, params })
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: (v) => resolve(v as T), reject })
      this.socket!.write(payload + '\n')
    })
  }

  // Convenience wrappers over the Session / Task APIs.
  createSession(workspaceRoot: string, profile = 'safe-edit'): Promise<Session> {
    return this.call<Session>('session.create', { workspace_root: workspaceRoot, profile })
  }

  listSessions(): Promise<Session[]> {
    return this.call<Session[]>('session.list')
  }

  submitTask(sessionId: string, prompt: string): Promise<Task> {
    return this.call<Task>('task.submit', { session_id: sessionId, prompt })
  }

  replaySession(sessionId: string): Promise<PiEvent[]> {
    return this.call<PiEvent[]>('session.replay', { session_id: sessionId })
  }

  close(): void {
    this.socket?.end()
    this.socket = null
  }

  private onData(chunk: string): void {
    this.buffer += chunk
    let newline: number
    while ((newline = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, newline)
      this.buffer = this.buffer.slice(newline + 1)
      if (!line.trim()) continue
      try {
        const msg = JSON.parse(line) as { id?: number; result?: unknown; error?: RpcError }
        if (msg.id === undefined) continue // server notification (event stream)
        const waiter = this.pending.get(msg.id)
        if (!waiter) continue
        this.pending.delete(msg.id)
        if (msg.error) {
          waiter.reject(new Error(`rpc ${msg.error.code}: ${msg.error.message}`))
        } else {
          waiter.resolve(msg.result)
        }
      } catch {
        // ignore malformed frames; the daemon is line-delimited JSON
      }
    }
  }
}
