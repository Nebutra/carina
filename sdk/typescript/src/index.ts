/** Carina JSON-RPC SDK for Runtime 0.6.1. */
import { createConnection, type Socket } from 'node:net'
import { homedir } from 'node:os'
import { join } from 'node:path'

export const compatibleRuntimeVersion = '0.6.1'

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

export interface CarinaEvent {
  event_id?: string
  session_id: string
  task_id?: string
  type: string
  timestamp: string
  payload?: Record<string, unknown>
  permission_decision_id?: string
}

export interface SessionAttachment {
  events: CarinaEvent[]
  from: number
  cursor: number
}

export interface UsageCostRow {
  provider: string
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  pricing_known: boolean
  estimated: boolean
}

export interface UsageCostReport {
  providers: UsageCostRow[]
  totals: Omit<UsageCostRow, 'provider' | 'model' | 'estimated'>
  estimated: boolean
}

export interface WorkflowRun { id: string; workflow: string; session_id: string; status: string; attempt: number; progress?: number }
export interface Worker { worker_id: string; name: string; kind: string; status: string }
export interface ChannelEvent { id: string; sender_id: string; session_id: string; kind: string; timestamp: string; payload?: Record<string, unknown>; permission_decision_id?: string; permission_allow?: boolean }
export interface Extension { manifest: { name: string; version: string; estimated_prompt_tokens?: number }; source: string; enabled: boolean; trusted: boolean }

export interface PatchFile { path: string; new_content: string }
export interface Patch {
  patch_id: string
  session_id: string
  status: string
  affected_files: string[]
  diff: string
  reason: string
  approval_status: string
  rollback_pointer?: string
}
export interface Decision {
  decision_id: string
  capability: string
  resource: string
  decision: 'allowed' | 'denied' | 'requires_approval'
  reason: string
  policy_id: string
}
export interface ExecResult {
  decision: Decision
  result?: { exit_code: number; duration_ms: number; stdout: string[]; stderr: string[]; timed_out: boolean }
}
export interface AuditReport {
  session_id: string
  total_events: number
  events_by_type: Record<string, number>
  policy_violations: unknown[]
  files_read: unknown[]
  commands: unknown[]
}

interface RpcError { code: number; message: string }
interface PendingCall {
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timer: ReturnType<typeof setTimeout>
}
type NotificationHandler = (method: string, params: unknown) => void

export class CarinaRpcError extends Error {
  constructor(public readonly code: number, message: string) {
    super(`rpc ${code}: ${message}`)
    this.name = 'CarinaRpcError'
  }
}

export class CarinaTransportError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'CarinaTransportError'
  }
}

export const defaultSocketPath = (): string => join(homedir(), '.carina', 'daemon.sock')

export class CarinaClient {
  private socket: Socket | null = null
  private connecting: Promise<void> | null = null
  private nextId = 0
  private buffer = ''
  private pending = new Map<number, PendingCall>()
  private notifications = new Set<NotificationHandler>()

  constructor(
    private readonly socketPath: string = defaultSocketPath(),
    private readonly callTimeoutMs = 15_000,
  ) {
    if (!Number.isFinite(callTimeoutMs) || callTimeoutMs <= 0) throw new Error('callTimeoutMs must be positive')
  }

  async connect(): Promise<void> {
    if (this.socket && !this.socket.destroyed) return
    if (this.connecting) return this.connecting
    this.connecting = new Promise<void>((resolve, reject) => {
      const socket = createConnection(this.socketPath)
      const failConnect = (error: Error): void => {
        socket.destroy()
        reject(new CarinaTransportError(`cannot reach carina-daemon at ${this.socketPath}: ${error.message}`))
      }
      socket.once('error', failConnect)
      socket.once('connect', () => {
        socket.off('error', failConnect)
        this.socket = socket
        socket.on('data', (chunk) => this.onData(chunk.toString('utf8')))
        socket.on('error', (error) => this.handleDisconnect(socket, new CarinaTransportError(error.message)))
        socket.on('end', () => this.handleDisconnect(socket, new CarinaTransportError('carina-daemon closed the connection')))
        socket.on('close', () => this.handleDisconnect(socket, new CarinaTransportError('carina-daemon connection closed')))
        resolve()
      })
    }).finally(() => { this.connecting = null })
    return this.connecting
  }

  async call<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    await this.connect()
    const socket = this.socket
    if (!socket || socket.destroyed) throw new CarinaTransportError('carina-daemon is disconnected')
    const id = ++this.nextId
    const payload = JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n'
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new CarinaTransportError(`rpc ${method} timed out after ${this.callTimeoutMs}ms`))
      }, this.callTimeoutMs)
      this.pending.set(id, {
        timer,
        resolve: (value) => { clearTimeout(timer); resolve(value as T) },
        reject: (error) => { clearTimeout(timer); reject(error) },
      })
      socket.write(payload, (error) => {
        if (!error) return
        const pending = this.pending.get(id)
        if (!pending) return
        this.pending.delete(id)
        pending.reject(new CarinaTransportError(`rpc ${method} write failed: ${error.message}`))
      })
    })
  }

  createSession(workspaceRoot: string, profile = 'safe-edit'): Promise<Session> {
    return this.call('session.create', { workspace_root: workspaceRoot, profile })
  }
  listSessions(): Promise<Session[]> { return this.call('session.list') }
  submitTask(sessionId: string, prompt: string): Promise<Task> {
    return this.call('task.submit', { session_id: sessionId, prompt })
  }
  replaySession(sessionId: string): Promise<CarinaEvent[]> { return this.call('session.replay', { session_id: sessionId }) }
  attachSession(sessionId: string, since = 0): Promise<SessionAttachment> {
    return this.call('session.attach', { session_id: sessionId, since })
  }
  forkSession(sessionId: string): Promise<Session> { return this.call('session.fork', { session_id: sessionId }) }
  cost(sessionId?: string, taskId?: string): Promise<UsageCostReport> {
    return this.call('usage.cost', { ...(sessionId ? { session_id: sessionId } : {}), ...(taskId ? { task_id: taskId } : {}) })
  }
  steerTask(taskId: string, message: string): Promise<{ queued: boolean; task_id: string; status: string }> {
    return this.call('task.steer', { task_id: taskId, message })
  }
  answerQuestion(questionId: string, value: string): Promise<{ question_id: string; accepted: boolean; value: string }> {
    return this.call('task.user.answer', { question_id: questionId, value })
  }
  listWorkflows(): Promise<WorkflowRun[]> { return this.call('workflow.list') }
  workflowDetail(runId: string): Promise<Record<string, unknown>> { return this.call('workflow.detail', { run_id: runId }) }
  runWorkflow(sessionId: string, workflow: string, input = ''): Promise<WorkflowRun> { return this.call('workflow.run', { session_id: sessionId, workflow, input }) }
  pauseWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.pause', { run_id: runId }) }
  resumeWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.resume', { run_id: runId }) }
  stopWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.stop', { run_id: runId }) }
  restartWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.restart', { run_id: runId }) }
  listWorkers(): Promise<Worker[]> { return this.call('worker.list') }
  resolveApproval(decisionId: string, allow: boolean, approver = '', scope: 'once'|'session'|'project' = 'once'): Promise<void> { return this.call('task.approval.resolve', { decision_id: decisionId, allow, approver, scope }) }
  doctor(): Promise<Record<string, unknown>> { return this.call('daemon.doctor') }
  listAgents(workspaceRoot = ''): Promise<Record<string, unknown>> { return this.call('agent.list', { workspace_root: workspaceRoot }) }
  injectChannelEvent(event: ChannelEvent, signature: string): Promise<Record<string, unknown>> { return this.call('channel.event.inject', { event, signature }) }
  listExtensions(): Promise<{ plugins: Extension[]; safe_mode: boolean; total_prompt_tokens: number }> { return this.call('extension.list') }
  setExtensionEnabled(name: string, enabled: boolean): Promise<Extension> { return this.call(enabled ? 'extension.enable' : 'extension.disable', { name }) }

  async streamSessionEvents(sessionId: string, handler: (event: CarinaEvent) => void): Promise<() => void> {
    const listener: NotificationHandler = (method, params) => {
      if (method !== 'event' || typeof params !== 'object' || params === null) return
      const event = params as CarinaEvent
      if (event.session_id === sessionId) handler(event)
    }
    this.notifications.add(listener)
    try {
      await this.call('session.events.stream', { session_id: sessionId })
    } catch (error) {
      this.notifications.delete(listener)
      throw error
    }
    return () => this.notifications.delete(listener)
  }

  search(sessionId: string, pattern: string): Promise<Array<{ file: string; line: number; text: string }>> {
    return this.call('workspace.search', { session_id: sessionId, pattern })
  }
  getFile(sessionId: string, path: string): Promise<{ content: string; hash: string }> {
    return this.call('workspace.file.get', { session_id: sessionId, path })
  }
  proposePatch(sessionId: string, files: PatchFile[], reason = ''): Promise<Patch> {
    return this.call('workspace.patch.propose', { session_id: sessionId, reason, files })
  }
  applyPatch(sessionId: string, patchId: string): Promise<Patch> {
    return this.call('workspace.patch.apply', { session_id: sessionId, patch_id: patchId })
  }
  rollbackPatch(sessionId: string, patchId: string): Promise<Patch> {
    return this.call('workspace.patch.rollback', { session_id: sessionId, patch_id: patchId })
  }
  exec(sessionId: string, argv: string[], taskId?: string): Promise<ExecResult> {
    return this.call('command.exec', { session_id: sessionId, argv, ...(taskId ? { task_id: taskId } : {}) })
  }
  approve(sessionId: string, decisionId: string): Promise<unknown> {
    return this.call('task.action.approve', { session_id: sessionId, decision_id: decisionId })
  }
  deny(sessionId: string, decisionId: string, reason = 'denied'): Promise<unknown> {
    return this.call('task.action.deny', { session_id: sessionId, decision_id: decisionId, reason })
  }
  auditReport(sessionId: string): Promise<AuditReport> { return this.call('audit.report', { session_id: sessionId }) }

  close(): void {
    const socket = this.socket
    this.socket = null
    socket?.destroy()
    this.failPending(new CarinaTransportError('Carina client closed'))
  }

  private onData(chunk: string): void {
    this.buffer += chunk
    let newline: number
    while ((newline = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, newline)
      this.buffer = this.buffer.slice(newline + 1)
      if (!line.trim()) continue
      try {
        const msg = JSON.parse(line) as { id?: number; method?: string; params?: unknown; result?: unknown; error?: RpcError }
        if (msg.id === undefined) {
          if (msg.method) for (const handler of this.notifications) handler(msg.method, msg.params)
          continue
        }
        const waiter = this.pending.get(msg.id)
        if (!waiter) continue
        this.pending.delete(msg.id)
        if (msg.error) waiter.reject(new CarinaRpcError(msg.error.code, msg.error.message))
        else waiter.resolve(msg.result)
      } catch {
        // A malformed frame cannot be correlated safely; the call timeout still bounds pending work.
      }
    }
  }

  private handleDisconnect(socket: Socket, error: Error): void {
    if (this.socket !== socket) return
    this.socket = null
    this.buffer = ''
    this.failPending(error)
  }

  private failPending(error: Error): void {
    const pending = [...this.pending.values()]
    this.pending.clear()
    for (const call of pending) call.reject(error)
  }
}
