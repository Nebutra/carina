/** Carina JSON-RPC SDK for Runtime 0.6.2. */
import { createConnection, type Socket } from 'node:net'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { createHash } from 'node:crypto'

export const compatibleRuntimeVersion = '0.6.2'

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
  raw_cursor?: number
}

export interface SessionAttachment {
  events: CarinaEvent[]
  from: number
  cursor: number
  event_mode: 'compat'|'canonical'
}
export interface EventSubscription { subscription_id:string;cursor:number;replayed:number;event_mode:'compat'|'canonical' }

export interface ReviewItem { id: string; type: string; status: string; task_id?: string; details?: Record<string, unknown> }
export interface SessionReview {
  session_id: string
  projection_version: string
  source_cursor: string
  state: string
  summary?: string
  waiting_reason?: string
  intent?: string
  success_criteria: unknown[]
  changes: ReviewItem[]
  commands: ReviewItem[]
  tools: ReviewItem[]
  checks: ReviewItem[]
  diagnostics: ReviewItem[]
  policy_decisions: ReviewItem[]
  questions: ReviewItem[]
  conflicts: ReviewItem[]
  risk_and_policy: ReviewItem[]
  artifact_ids: string[]
  rollback: { available: boolean; patch_ids: string[] }
  stats: Record<string, number>
}
export interface SessionItemEvent { type:string;session_id:string;turn_id?:string;task_id?:string;item_id?:string;source_event_id?:string;timestamp?:string;details?:Record<string,unknown>;item?:ReviewItem }
export interface SessionItemsPage { data:SessionItemEvent[];next_cursor?:string;projection_version:string }
export interface CursorRecovery { code:'invalid_cursor'|'cursor_expired';projection_version:string;recovery:string;snapshot_method:'session.items';earliest_cursor?:string }

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
export interface RuntimeInfo { runtime_version: string; protocol_version: string; projection_version?: string; minimum_protocol_version?: string; capabilities: Record<string, unknown> }
export type JsonSchema = Record<string, unknown>
export interface RunOptions { outputSchema?: JsonSchema; signal?: AbortSignal; pollIntervalMs?: number }
export interface TurnResult { task: Task; finalResponse: string; structuredOutput?: unknown }
export interface AgentViewEntry { session_id: string; task_id?: string; state: string; title?: string; summary?: string; workspace_root?: string; updated_at?: string }
export interface AgentView { needs_input: AgentViewEntry[]; working: AgentViewEntry[]; completed: AgentViewEntry[] }
export interface SuccessCheck { kind: string; path?: string; pattern?: string; command?: string[] }
export interface Checkpoint { checkpoint_id: string; task_id: string; session_id: string; turn: number; summary?: string; applied_patches: string[] }
export interface ArtifactScope { session_id: string; task_id?: string; call_id?: string }
export interface ArtifactMetadata { id:string; scope:ArtifactScope; media_type?:string; bytes:number; created_at:string; expires_at?:string; preview?:string; truncated:boolean; preview_utf8:boolean }
export interface ArtifactReadPage { metadata:ArtifactMetadata; offset:number; next_offset:number; eof:boolean; content_base64:string }

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

interface RpcError { code: number; message: string; data?: unknown }
interface PendingCall {
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timer: ReturnType<typeof setTimeout>
}
type NotificationHandler = (method: string, params: unknown) => void

export class CarinaRpcError extends Error {
  constructor(public readonly code: number, message: string, public readonly data?: unknown) {
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
  getSession(sessionId: string): Promise<Session> { return this.call('session.get', { session_id: sessionId }) }
  listSessions(): Promise<Session[]> { return this.call('session.list') }
  submitTask(sessionId: string, prompt: string): Promise<Task> {
    return this.call('task.submit', { session_id: sessionId, prompt })
  }
  submitGoal(sessionId: string, prompt: string, successCriteria: SuccessCheck[]): Promise<Task> {
    return this.call('task.submit', { session_id: sessionId, prompt, success_criteria: successCriteria })
  }
  replaySession(sessionId: string): Promise<CarinaEvent[]> { return this.call('session.replay', { session_id: sessionId }) }
  attachSession(sessionId: string, since = 0, eventMode: 'compat'|'canonical' = 'compat'): Promise<SessionAttachment> {
    return this.call('session.attach', { session_id: sessionId, since, event_mode: eventMode })
  }
  reviewSession(sessionId: string): Promise<SessionReview> { return this.call('session.review', { session_id: sessionId }) }
  listSessionItems(sessionId:string,cursor='',limit=50):Promise<SessionItemsPage>{return this.call('session.items',{session_id:sessionId,limit,...(cursor?{cursor}:{})})}
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
  async initialize(clientName = '@carina/sdk', clientVersion = '0.2.0'): Promise<RuntimeInfo> {
    const info = await this.call<RuntimeInfo>('runtime.initialize', { protocol_version: '1.2.0', schema_version: '1.2.0', projection_version: '1.0.0', client_name: clientName, client_version: clientVersion })
    if (info.protocol_version.replace(/^v/, '').split('.')[0] !== '1') throw new Error(`sdk: incompatible runtime protocol ${JSON.stringify(info.protocol_version)}`)
    if (info.capabilities?.tool_call_lifecycle !== true) throw new Error('sdk: runtime lacks required tool_call_lifecycle capability')
    const eventSchema=String(info.capabilities?.event_schema_version??'').replace(/^v/,'').split('.')
    if(eventSchema.length!==3||eventSchema[0]!=='0'||eventSchema[1]!=='3')throw new Error(`sdk: incompatible event schema ${String(info.capabilities?.event_schema_version)}; require 0.3.x`)
    return info
  }
  workflowDetail(runId: string): Promise<Record<string, unknown>> { return this.call('workflow.detail', { run_id: runId }) }
  runWorkflow(sessionId: string, workflow: string, input = ''): Promise<WorkflowRun> { return this.call('workflow.run', { session_id: sessionId, workflow, input }) }
  pauseWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.pause', { run_id: runId }) }
  resumeWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.resume', { run_id: runId }) }
  stopWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.stop', { run_id: runId }) }
  restartWorkflow(runId: string): Promise<WorkflowRun> { return this.call('workflow.restart', { run_id: runId }) }
  listWorkers(): Promise<Worker[]> { return this.call('worker.list') }
  resolveApproval(decisionId: string, allow: boolean, approver = '', scope: 'once'|'session'|'project' = 'once'): Promise<void> { return this.call('task.approval.resolve', { decision_id: decisionId, approve: allow, approver, scope }) }
  doctor(): Promise<Record<string, unknown>> { return this.call('daemon.doctor') }
  statArtifact(scope:ArtifactScope,artifactId:string):Promise<ArtifactMetadata>{return this.call('artifact.stat',{...scope,artifact_id:artifactId})}
  readArtifactPage(scope:ArtifactScope,artifactId:string,offset=0,limit=65536):Promise<ArtifactReadPage>{if(offset<0||limit<1||limit>1048576)return Promise.reject(new RangeError('offset must be non-negative and limit must be 1..1048576'));return this.call('artifact.read',{...scope,artifact_id:artifactId,offset,limit})}
  async downloadArtifact(scope:ArtifactScope,artifactId:string,maxBytes:number):Promise<{content:Buffer;metadata:ArtifactMetadata}>{if(maxBytes<=0)throw new RangeError('maxBytes must be positive');const chunks:Buffer[]=[];let size=0,offset=0,metadata:ArtifactMetadata|undefined;for(;;){const page=await this.readArtifactPage(scope,artifactId,offset,1048576);metadata=page.metadata;const chunk=Buffer.from(page.content_base64,'base64');size+=chunk.length;if(size>maxBytes)throw new RangeError(`artifact exceeds download limit ${maxBytes}`);chunks.push(chunk);if(page.eof)break;if(page.next_offset<=offset)throw new Error('artifact pagination did not advance');offset=page.next_offset}const content=Buffer.concat(chunks);if(createHash('sha256').update(content).digest('hex')!==artifactId)throw new Error('artifact digest mismatch');return{content,metadata:metadata!}}
  listAgents(workspaceRoot = ''): Promise<Record<string, unknown>> { return this.call('agent.list', { workspace_root: workspaceRoot }) }
  agentView(): Promise<AgentView> { return this.call('agent.view') }
  listCheckpoints(sessionId: string): Promise<Checkpoint[]> { return this.call('session.checkpoint.list', { session_id: sessionId }) }
  previewCheckpoint(sessionId: string, checkpointId: string): Promise<Record<string, unknown>> { return this.call('session.checkpoint.preview', { session_id: sessionId, checkpoint_id: checkpointId }) }
  summarizeCheckpoint(sessionId: string, checkpointId: string): Promise<Record<string, unknown>> { return this.call('session.checkpoint.summarize', { session_id: sessionId, checkpoint_id: checkpointId }) }
  restoreCheckpoint(sessionId: string, checkpointId: string, confirmed = false): Promise<Record<string, unknown>> { return this.call('session.checkpoint.restore', { session_id: sessionId, checkpoint_id: checkpointId, confirmed }) }
  injectChannelEvent(event: ChannelEvent, signature: string): Promise<Record<string, unknown>> { return this.call('channel.event.inject', { event, signature }) }
  listExtensions(): Promise<{ plugins: Extension[]; safe_mode: boolean; total_prompt_tokens: number }> { return this.call('extension.list') }
  setExtensionEnabled(name: string, enabled: boolean): Promise<Extension> { return this.call(enabled ? 'extension.enable' : 'extension.disable', { name }) }

  async startThread(options: { workingDirectory: string; profile?: string } ): Promise<CarinaThread> { await this.initialize();const session=await this.createSession(options.workingDirectory,options.profile);return new CarinaThread(this,session) }
  async resumeThread(sessionId: string): Promise<CarinaThread> { await this.initialize();return new CarinaThread(this,await this.getSession(sessionId)) }
  async forkThread(sessionId: string, boundary: { lastTaskId?: string; throughTurn?: number } = {}): Promise<CarinaThread> { await this.initialize();const session=await this.call<Session>('session.fork',{session_id:sessionId,...(boundary.lastTaskId?{last_task_id:boundary.lastTaskId}:{}),...(boundary.throughTurn?{through_turn:boundary.throughTurn}:{})});return new CarinaThread(this,session) }

  async streamSessionEvents(sessionId: string, handler: (event: CarinaEvent) => void, eventMode: 'compat'|'canonical' = 'compat'): Promise<() => Promise<void>> {
    const listener: NotificationHandler = (method, params) => {
      if (method !== 'event' || typeof params !== 'object' || params === null) return
      const event = params as CarinaEvent
      if (event.session_id === sessionId) handler(event)
    }
    this.notifications.add(listener)
    try {
      const subscription = await this.call<{subscription_id?:string}>('session.events.stream', { session_id: sessionId, event_mode: eventMode })
      return async () => {
        this.notifications.delete(listener)
        if (subscription.subscription_id) await this.call('session.events.unsubscribe', { subscription_id: subscription.subscription_id }).catch(() => {})
      }
    } catch (error) {
      this.notifications.delete(listener)
      throw error
    }
  }

  subscribeSessionEventsFrom(sessionId:string,since=0,eventMode:'compat'|'canonical'='compat'):Promise<EventSubscription>{return this.call('session.events.stream',{session_id:sessionId,since,event_mode:eventMode})}

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
        if (msg.error) waiter.reject(new CarinaRpcError(msg.error.code, msg.error.message, msg.error.data))
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

export class CarinaThread {
  constructor(private readonly client: CarinaClient, public readonly session: Session) {}
  async fork(boundary: { lastTaskId?: string; throughTurn?: number } = {}): Promise<CarinaThread> { return this.client.forkThread(this.session.session_id,boundary) }
  async run(input: string, options: RunOptions = {}): Promise<TurnResult> {
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('aborted')
    const task=await this.client.call<Task>('task.submit',{session_id:this.session.session_id,prompt:input,...(options.outputSchema?{output_schema:options.outputSchema}:{})})
    const cancel=()=>{void this.client.call('task.cancel',{task_id:task.task_id}).catch(()=>{})};options.signal?.addEventListener('abort',cancel,{once:true})
    try { for (;;) { if(options.signal?.aborted)throw options.signal.reason??new Error('aborted');const current=await this.client.call<Task>('task.result',{task_id:task.task_id});if(['completed','degraded','failed','cancelled','needs_input'].includes(current.status)){let structuredOutput:unknown;try{structuredOutput=options.outputSchema?JSON.parse((current as Task & {summary?:string}).summary??''):undefined}catch{};return{task:current,finalResponse:(current as Task & {summary?:string}).summary??'',structuredOutput}};await new Promise(r=>setTimeout(r,options.pollIntervalMs??50))} } finally { options.signal?.removeEventListener('abort',cancel) }
  }
  async runStreamed(input: string, options: RunOptions = {}): Promise<{events: AsyncGenerator<CarinaEvent|{type:'turn.completed';result:TurnResult}>}> {
    const queue:CarinaEvent[]=[];let wake:(()=>void)|undefined;const stop=await this.client.streamSessionEvents(this.session.session_id,e=>{queue.push(e);wake?.();wake=undefined});const run=this.run(input,options);async function* events(){try{for(;;){while(queue.length)yield queue.shift()!;const done=await Promise.race([run.then(result=>({result})),new Promise<null>(resolve=>{wake=()=>resolve(null)})]);if(done){yield{type:'turn.completed' as const,result:done.result};return}}}finally{await stop()}};return{events:events()}
  }
}
