export type RpcCall = <T = unknown>(method: string, params?: Record<string, unknown>) => Promise<T>

export interface TrajectoryEvent {
  type: string
  session_id: string
  raw_cursor?: number
  [key: string]: unknown
}

export interface TrajectoryStreamEvent {
  event: TrajectoryEvent
  phase: 'catch-up' | 'live'
  sessionId: string
}

export interface TrajectoryStreamHandlers {
  onEvent?: (event: TrajectoryStreamEvent) => void
  onError?: (error: Error, sessionId: string) => void
  onReady?: (stream: { cursor: number; replayed: number; sessionId: string }) => void
}

interface StreamAck {
  subscription_id: string
  cursor: number
  replayed: number
  event_mode: 'canonical'
}

interface BufferedEvent {
  event: TrajectoryEvent
  generation: number
}

const isRecord = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === 'object' && !Array.isArray(value)

function decodeEvent(method: unknown, params: unknown): TrajectoryEvent | null {
  if (method !== 'event' || !isRecord(params)) return null
  if (typeof params.type !== 'string' || !params.type.trim()) return null
  if (typeof params.session_id !== 'string' || !params.session_id.trim()) return null
  if (params.raw_cursor !== undefined && (!Number.isSafeInteger(params.raw_cursor) || Number(params.raw_cursor) < 1)) return null
  return params as TrajectoryEvent
}

function decodeAck(value: unknown): StreamAck | null {
  if (!isRecord(value)) return null
  if (typeof value.subscription_id !== 'string' || !value.subscription_id) return null
  if (!Number.isSafeInteger(value.cursor) || Number(value.cursor) < 0) return null
  if (!Number.isSafeInteger(value.replayed) || Number(value.replayed) < 0) return null
  if (value.event_mode !== 'canonical') return null
  return value as unknown as StreamAck
}

function asError(value: unknown) {
  return value instanceof Error ? value : new Error(String(value))
}

/**
 * Owns the replay-then-tail lifecycle for the multiplexed WebSocket RPC client.
 * Cursor state survives transport disconnects, while subscription identity and
 * pre-ACK notifications are scoped to one connection generation.
 */
export class TrajectoryStreamController {
  private activeSessionId: string | null = null
  private attaching = false
  private buffered: BufferedEvent[] = []
  private readonly call: RpcCall
  private cursors = new Map<string, number>()
  private generation = 0
  private handlers: TrajectoryStreamHandlers = {}
  private subscriptionId: string | null = null

  constructor(call: RpcCall) {
    this.call = call
  }

  setHandlers(handlers: TrajectoryStreamHandlers) {
    this.handlers = handlers
  }

  cursorFor(sessionId: string) {
    return this.cursors.get(sessionId) ?? 0
  }

  async select(sessionId: string | null): Promise<void> {
    if (sessionId === this.activeSessionId && (this.subscriptionId || this.attaching)) return

    const previousSubscription = this.subscriptionId
    const generation = ++this.generation
    this.activeSessionId = sessionId
    this.subscriptionId = null
    this.attaching = Boolean(sessionId)
    this.buffered = []
    if (previousSubscription) void this.release(previousSubscription)
    if (!sessionId) return

    const since = this.cursorFor(sessionId)
    try {
      // Gateway WebSockets remain multiplexed; replay_tail_version=1 is reserved
      // for the local client's fresh, exclusive stream connection.
      const rawAck = await this.call('session.events.stream', {
        session_id: sessionId,
        since,
        event_mode: 'canonical',
      })
      const ack = decodeAck(rawAck)
      if (!ack) throw new Error('Gateway returned an invalid session.events.stream acknowledgement')
      if (generation !== this.generation || sessionId !== this.activeSessionId) {
        await this.release(ack.subscription_id)
        return
      }

      const catchUp = this.buffered
      this.buffered = []
      this.attaching = false
      this.subscriptionId = ack.subscription_id
      for (const buffered of catchUp) {
        if (buffered.generation === generation) this.commit(buffered.event, 'catch-up')
      }
      this.cursors.set(sessionId, Math.max(this.cursorFor(sessionId), ack.cursor))
      this.handlers.onReady?.({ cursor: this.cursorFor(sessionId), replayed: ack.replayed, sessionId })
    } catch (error) {
      if (generation !== this.generation || sessionId !== this.activeSessionId) return
      this.attaching = false
      this.buffered = []
      this.handlers.onError?.(asError(error), sessionId)
    }
  }

  handleNotification(method: unknown, params: unknown): boolean {
    const event = decodeEvent(method, params)
    if (!event || event.session_id !== this.activeSessionId) return false
    if (this.attaching) {
      this.buffered.push({ event, generation: this.generation })
      return true
    }
    if (!this.subscriptionId) return false
    this.commit(event, 'live')
    return true
  }

  handleDisconnect() {
    this.generation += 1
    this.subscriptionId = null
    this.attaching = false
    this.buffered = []
  }

  dispose() {
    const subscriptionId = this.subscriptionId
    this.generation += 1
    this.activeSessionId = null
    this.subscriptionId = null
    this.attaching = false
    this.buffered = []
    if (subscriptionId) void this.release(subscriptionId)
  }

  private commit(event: TrajectoryEvent, phase: TrajectoryStreamEvent['phase']) {
    const cursor = event.raw_cursor
    if (cursor !== undefined) {
      if (cursor <= this.cursorFor(event.session_id)) return
      this.cursors.set(event.session_id, cursor)
    }
    this.handlers.onEvent?.({ event, phase, sessionId: event.session_id })
  }

  private async release(subscriptionId: string) {
    try {
      await this.call('session.events.unsubscribe', { subscription_id: subscriptionId })
    } catch {
      // A closed transport implicitly releases its subscriptions.
    }
  }
}
