import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { Badge, Button, ChevronDown, CircleDot, Download, Folder, Icon, IconButton, Inbox, ListChecks, Menu, PanelLeftClose, PanelLeftOpen, Plug, Plus, Search, Settings2, Sparkles, Sun, Terminal, X, cn } from './design-system'
import { loadGatewaySessionToken, loadHarnessPreferences, loadTauriWorkspace, normalizeGatewayUrl, resolveTheme, saveGatewaySessionToken, saveHarnessPreferences, saveTauriWorkspace } from './lib/harness-preferences'
import type { ThemePreference } from './lib/harness-preferences'
import { deriveTrajectoryRows } from './lib/trajectory'
import type { SessionItem } from './lib/trajectory'
import { TrajectoryStreamController } from './lib/trajectory-stream'
import { decodeWorkspaceTree } from './lib/workspace'
import type { WorkspaceFile } from './lib/workspace'
import { ContextPanel, ConversationTabs, FilesPanel, SessionList, SettingsDialog, Surface, TerminalPanel, TrajectoryView } from './patterns'
import type { Attachment, ConversationTab, Role, Session, SettingsSection } from './patterns'

const MAX_PAGES = 20
const PAGE_SIZE = 100
const MAX_ATTACHMENTS = 4
const MAX_ATTACHMENT_BYTES = 4 * 1024 * 1024
const ACCEPTED_MEDIA = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
const COMPACT_VIEWPORT_QUERY = '(max-width: 820px)'
const WORKSPACE_FOCUSABLE_SELECTOR = [
  'a[href]', 'button:not([disabled])', 'input:not([disabled])',
  'select:not([disabled])', 'textarea:not([disabled])', '[tabindex]:not([tabindex="-1"])',
].join(',')

type View = 'chat' | 'inbox' | 'runs' | 'trajectory' | 'graph'
type RosterStatus = 'idle' | 'loading' | 'ready' | 'error'
type SessionItemsResponse = { data?: SessionItem[]; next_cursor?: string | null }
type HarnessSubmitResponse = { session: Session; execution: { task_id: string; status: string } }
type HarnessMediaInput = { media_type: string; content_base64: string; origin: string }
type TauriBootstrapResult = { workspace_root: string; gateway_url: string; token: string; role: Role; expires_at: string }
type PendingRequest = { resolve: (value: unknown) => void; reject: (reason?: unknown) => void; timer: number }
type RpcMessage = { id?: number; method?: string; params?: unknown; result?: unknown; error?: { message?: string } }
type RailButtonProps = { active: boolean; label: string; onClick: () => void; children: ReactNode }
const asset = (name: string) => `${import.meta.env.BASE_URL}${name}`
const isTauriRuntime = () => typeof window !== 'undefined' && typeof window.__TAURI__?.core?.invoke === 'function'

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  const chunk = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunk) binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + chunk, bytes.length)))
  return btoa(binary)
}

function App() {
  const [initialPreferences] = useState(loadHarnessPreferences)
  const socket = useRef<WebSocket | null>(null)
  const sequence = useRef(0)
  const pending = useRef(new Map<number, PendingRequest>())
  const [connected, setConnected] = useState(false)
  const [connectionLabel, setConnectionLabel] = useState('Gateway offline')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsSection, setSettingsSection] = useState<SettingsSection>('general')
  const [connecting, setConnecting] = useState(false)
  const [url, setUrl] = useState(initialPreferences.gatewayUrl)
  const [token, setToken] = useState(() => isTauriRuntime() ? '' : loadGatewaySessionToken())
  const [role, setRole] = useState<Role>(initialPreferences.role)
  const [connectedRole, setConnectedRole] = useState<Role | null>(null)
  const [autoConnect, setAutoConnect] = useState(initialPreferences.autoConnect)
  const [workspace, setWorkspace] = useState('')
  const [sessions, setSessions] = useState<Session[]>([])
  const [rosterStatus, setRosterStatus] = useState<RosterStatus>('idle')
  const [rosterError, setRosterError] = useState('')
  const [workspaceFiles, setWorkspaceFiles] = useState<WorkspaceFile[]>([])
  const [workspaceFilesLoading, setWorkspaceFilesLoading] = useState(false)
  const [workspaceFilesError, setWorkspaceFilesError] = useState('')
  const [workspaceFilesTruncated, setWorkspaceFilesTruncated] = useState(false)
  const [activeSession, setActiveSession] = useState<Session | null>(null)
  const [items, setItems] = useState<SessionItem[]>([])
  const [view, setView] = useState<View>('chat')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState('')
  const [collapsedTurns, setCollapsedTurns] = useState<Set<string>>(() => new Set())
  const [traceLoading, setTraceLoading] = useState(false)
  const [traceError, setTraceError] = useState('')
  const [traceTruncated, setTraceTruncated] = useState(false)
  const [notice, setNotice] = useState('')
  const [themePreference, setThemePreference] = useState<ThemePreference>(initialPreferences.theme)
  const [systemPrefersDark, setSystemPrefersDark] = useState(() => typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  const [conversationTab, setConversationTab] = useState<ConversationTab>('chat')
  const [panelCollapsed, setPanelCollapsed] = useState(false)
  const [mobilePanelOpen, setMobilePanelOpen] = useState(false)
  const [compactViewport, setCompactViewport] = useState(() => typeof window !== 'undefined' && window.matchMedia(COMPACT_VIEWPORT_QUERY).matches)
  const [filesOpen, setFilesOpen] = useState(initialPreferences.filesOpen)
  const [terminalOpen, setTerminalOpen] = useState(initialPreferences.terminalOpen)
  const [prompt, setPrompt] = useState('')
  const [attachments, setAttachments] = useState<Attachment[]>([])
  const [submitting, setSubmitting] = useState(false)
  const activeSessionIdRef = useRef<string | null>(null)
  const workspaceTreeGeneration = useRef(0)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const settingsTriggerRef = useRef<HTMLButtonElement | null>(null)
  const settingsReturnFocusRef = useRef<HTMLElement | null>(null)
  const mobileMenuButtonRef = useRef<HTMLButtonElement | null>(null)
  const sidebarOpenButtonRef = useRef<HTMLButtonElement | null>(null)
  const workspacePanelRef = useRef<HTMLElement | null>(null)
  const workspaceBrandCollapseButtonRef = useRef<HTMLButtonElement | null>(null)
  const workspacePanelCollapseButtonRef = useRef<HTMLButtonElement | null>(null)
  const workspaceMobileCloseButtonRef = useRef<HTMLButtonElement | null>(null)
  const trajectoryRefresh = useRef<{ timer: number | null; running: boolean; pendingSessionId: string | null }>({ timer: null, running: false, pendingSessionId: null })
  const autoConnectStarted = useRef(false)
  const autoConnectEnabled = useRef(autoConnect)
  const reconnectTimer = useRef<number | null>(null)
  const reconnectAttempts = useRef(0)
  const connectRef = useRef<(silent?: boolean, overrides?: { url?: string; token?: string; role?: Role }) => void>(() => {})
  const appDisposing = useRef(false)
  const lastValidGatewayUrl = useRef(initialPreferences.gatewayUrl)
  const theme = resolveTheme(themePreference, systemPrefersDark)
  const activeRole: Role | null = connected ? connectedRole : null
  autoConnectEnabled.current = autoConnect
  const gatewayUrlError = useMemo(() => {
    try { normalizeGatewayUrl(url); return '' } catch (error) { return error instanceof Error ? error.message : String(error) }
  }, [url])

  const call = useCallback(<T = unknown>(method: string, params: Record<string, unknown> = {}) => new Promise<T>((resolve, reject) => {
    const current = socket.current
    if (!current || current.readyState !== WebSocket.OPEN) {
      reject(new Error('Gateway is disconnected'))
      return
    }
    const id = ++sequence.current
    const timer = window.setTimeout(() => {
      pending.current.delete(id)
      reject(new Error(`${method} timed out`))
    }, 15000)
    pending.current.set(id, { resolve: resolve as (value: unknown) => void, reject, timer })
    current.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
  }), [])
  const trajectoryStream = useMemo(() => new TrajectoryStreamController(call), [call])

  const readSessionItems = useCallback(async (sessionId: string) => {
    let cursor
    const data: SessionItem[] = []
    for (let page = 0; page < MAX_PAGES; page += 1) {
      const response: SessionItemsResponse = await call<SessionItemsResponse>('session.items', { session_id: sessionId, limit: PAGE_SIZE, ...(cursor ? { cursor } : {}) })
      data.push(...(response.data || []))
      if (!response.next_cursor) return { data, truncated: false }
      cursor = response.next_cursor
    }
    return { data, truncated: true }
  }, [call])

  const refreshRoster = useCallback(async () => {
    setRosterStatus('loading')
    setRosterError('')
    const [snapshotResult, rosterResult] = await Promise.allSettled([call('agent.view'), call<Session[]>('session.list')])
    const snapshot = snapshotResult.status === 'fulfilled' ? snapshotResult.value : null
    if (rosterResult.status === 'rejected' || !Array.isArray(rosterResult.value)) {
      const reason = rosterResult.status === 'rejected' ? rosterResult.reason : new Error('Gateway returned an invalid session roster')
      setRosterStatus('error')
      setRosterError(reason instanceof Error ? reason.message : String(reason))
      return { snapshot, ok: false }
    }
    const nextSessions = rosterResult.value
    setSessions(nextSessions)
    setWorkspace((current) => current || nextSessions[0]?.workspace_root || '')
    setRosterStatus('ready')
    return { snapshot, ok: true }
  }, [call])

  const loadTrajectory = useCallback(async (sessionId: string, background = false) => {
    if (!sessionId) return
    if (!background) setTraceLoading(true)
    setTraceError('')
    try {
      const page = await readSessionItems(sessionId)
      if (activeSessionIdRef.current !== sessionId) return
      setItems(page.data)
      setTraceTruncated(page.truncated)
    } catch (error) {
      if (activeSessionIdRef.current !== sessionId) return
      setTraceError(error instanceof Error ? error.message : String(error))
    } finally {
      if (!background && activeSessionIdRef.current === sessionId) setTraceLoading(false)
    }
  }, [readSessionItems])

  const scheduleTrajectoryRefresh = useCallback((sessionId: string) => {
    const state = trajectoryRefresh.current
    state.pendingSessionId = sessionId
    if (state.timer !== null || state.running) return
    const flush = () => {
      state.timer = null
      const pendingSessionId = state.pendingSessionId
      state.pendingSessionId = null
      if (!pendingSessionId || pendingSessionId !== activeSessionIdRef.current) return
      state.running = true
      void loadTrajectory(pendingSessionId, true).finally(() => {
        state.running = false
        if (state.pendingSessionId && state.timer === null) state.timer = window.setTimeout(flush, 80)
      })
    }
    state.timer = window.setTimeout(flush, 80)
  }, [loadTrajectory])

  const cancelTrajectoryRefresh = useCallback(() => {
    const state = trajectoryRefresh.current
    if (state.timer !== null) window.clearTimeout(state.timer)
    state.timer = null
    state.pendingSessionId = null
  }, [])

  const loadWorkspaceTree = useCallback(async (sessionId: string) => {
    if (!sessionId) return
    const generation = ++workspaceTreeGeneration.current
    setWorkspaceFilesLoading(true)
    setWorkspaceFilesError('')
    try {
      const result = decodeWorkspaceTree(await call('harness.workspace.tree', { session_id: sessionId }))
      if (generation !== workspaceTreeGeneration.current || activeSessionIdRef.current !== sessionId) return
      setWorkspaceFiles(result.files)
      setWorkspaceFilesTruncated(result.truncated)
    } catch (error) {
      if (generation !== workspaceTreeGeneration.current || activeSessionIdRef.current !== sessionId) return
      setWorkspaceFiles([])
      setWorkspaceFilesTruncated(false)
      setWorkspaceFilesError(error instanceof Error ? error.message : String(error))
    } finally {
      if (generation === workspaceTreeGeneration.current && activeSessionIdRef.current === sessionId) setWorkspaceFilesLoading(false)
    }
  }, [call])

  const scheduleReconnect = useCallback(() => {
    if (!autoConnectEnabled.current || appDisposing.current || reconnectTimer.current !== null) return
    const delay = Math.min(750 * 2 ** Math.min(reconnectAttempts.current, 4), 12000)
    reconnectAttempts.current += 1
    setConnectionLabel(`Gateway offline · retrying in ${Math.max(1, Math.round(delay / 1000))}s`)
    reconnectTimer.current = window.setTimeout(() => {
      reconnectTimer.current = null
      connectRef.current(true)
    }, delay)
  }, [])

  const connect = useCallback((silent = false, overrides: { url?: string; token?: string; role?: Role } = {}) => {
    if (connecting) return
    if (reconnectTimer.current !== null) {
      window.clearTimeout(reconnectTimer.current)
      reconnectTimer.current = null
    }
    let normalizedUrl: string
    try {
      normalizedUrl = normalizeGatewayUrl(overrides.url ?? url)
      if (normalizedUrl !== url && !overrides.url) setUrl(normalizedUrl)
    } catch (error) {
      setConnectionLabel(error instanceof Error ? error.message : String(error))
      if (!silent) {
        setSettingsSection('gateway')
        setSettingsOpen(true)
      }
      return
    }
    if (socket.current) {
      trajectoryStream.handleDisconnect()
      for (const request of pending.current.values()) {
        window.clearTimeout(request.timer)
        request.reject(new Error('Gateway reconnecting'))
      }
      pending.current.clear()
      socket.current.close()
      setConnected(false)
      setConnectedRole(null)
    }
    const effectiveRole = overrides.role ?? role
    const effectiveToken = overrides.token ?? token
    setConnecting(true)
    setConnectionLabel(`${silent ? 'Connecting automatically' : 'Connecting'} as ${effectiveRole}...`)
    let next: WebSocket
    let failureMessage = ''
    try {
      next = new WebSocket(normalizedUrl)
    } catch (error) {
      socket.current = null
      setConnecting(false)
      setConnectionLabel(error instanceof Error ? error.message : String(error))
      return
    }
    socket.current = next
    let handshakeComplete = false
    let networkFailure = false
    next.onopen = async () => {
      try {
        await call('gateway.hello', { protocol_version: 1, client_id: 'carina-harness-react', role: effectiveRole, scopes: effectiveRole === 'operator' ? ['read', 'stream', 'write'] : ['read', 'stream'], token: effectiveToken })
        await call('runtime.initialize', { protocol_version: '1.3.0', schema_version: '1.2.0', client_name: 'carina-harness-react', client_version: __CARINA_VERSION__ })
        await trajectoryStream.select(activeSessionIdRef.current)
        handshakeComplete = true
        reconnectAttempts.current = 0
        setConnected(true)
        setConnectedRole(effectiveRole)
        setConnectionLabel(`${effectiveRole === 'operator' ? 'Operator' : 'Observer'} · connected`)
        if (!silent) setSettingsOpen(false)
        const roster = await refreshRoster()
        if (!silent) setNotice(roster.ok ? 'Authoritative session roster refreshed.' : 'Connected, but the session roster is unavailable.')
      } catch (error) {
        failureMessage = error instanceof Error ? error.message : String(error)
        setConnectionLabel(failureMessage)
        next.close()
      } finally {
        setConnecting(false)
      }
    }
    next.onmessage = (event) => {
      let message: RpcMessage
      try { message = JSON.parse(String(event.data)) as RpcMessage } catch { return }
      if (typeof message.id !== 'number') {
        trajectoryStream.handleNotification(message.method, message.params)
        return
      }
      const request = pending.current.get(message.id)
      if (!request) return
      pending.current.delete(message.id)
      window.clearTimeout(request.timer)
      message.error ? request.reject(new Error(message.error.message || 'Gateway request failed')) : request.resolve(message.result)
    }
    next.onclose = () => {
      if (socket.current !== next) return
      socket.current = null
      setConnected(false)
      setConnectedRole(null)
      setConnecting(false)
      setConnectionLabel(failureMessage || 'Gateway offline')
      trajectoryStream.handleDisconnect()
      for (const request of pending.current.values()) {
        window.clearTimeout(request.timer)
        request.reject(new Error('Gateway disconnected'))
      }
      pending.current.clear()
      if (networkFailure || handshakeComplete || !failureMessage) scheduleReconnect()
    }
    next.onerror = () => { networkFailure = true; failureMessage = 'Connection failed'; setConnectionLabel(failureMessage); setConnecting(false) }
  }, [call, connecting, refreshRoster, role, scheduleReconnect, token, trajectoryStream, url])
  connectRef.current = connect

  const openSettings = useCallback((section: SettingsSection) => {
    const activeElement = typeof document !== 'undefined' ? document.activeElement : null
    settingsReturnFocusRef.current = activeElement instanceof HTMLElement ? activeElement : settingsTriggerRef.current
    setSettingsSection(section)
    setSettingsOpen(true)
  }, [])

  const closeSettings = useCallback(() => {
    // Restore the invoking control before Radix unmounts the modal. The
    // dialog still owns the default restore hook, but doing this synchronously
    // keeps keyboard focus deterministic even when the close animation is
    // skipped or the browser is under load.
    settingsReturnFocusRef.current?.focus({ preventScroll: true })
    setSettingsOpen(false)
  }, [])

  const bootstrapTauriWorkspace = useCallback(async (workspacePath: string, silent = true) => {
    const invoke = window.__TAURI__?.core?.invoke
    if (typeof invoke !== 'function') return false
    const requestedRole = role
    setConnectionLabel('Starting the local Gateway...')
    try {
      const result = await invoke<TauriBootstrapResult>('bootstrap_workspace', {
        workspace: workspacePath,
        role: requestedRole,
        origin: window.location.origin,
      })
      if (!result?.workspace_root || !result.gateway_url || !result.token || !result.expires_at) throw new Error('Desktop bootstrap returned incomplete Gateway credentials')
      if (result.role !== 'observer' && result.role !== 'operator') throw new Error('Desktop bootstrap returned an unknown Gateway role')
      saveTauriWorkspace(result.workspace_root)
      setWorkspace(result.workspace_root)
      setUrl(result.gateway_url)
      setRole(result.role)
      setToken(result.token)
      if (!silent) setNotice('Workspace selected. Connecting to its local Gateway...')
      connect(true, { url: result.gateway_url, token: result.token, role: result.role })
      return true
    } catch (error) {
      setConnected(false)
      setConnectedRole(null)
      setConnectionLabel('Gateway offline')
      setNotice(`Desktop workspace bootstrap failed: ${error instanceof Error ? error.message : String(error)}`)
      return false
    }
  }, [connect, role])

  useEffect(() => {
    trajectoryStream.setHandlers({
      onEvent: ({ sessionId }) => scheduleTrajectoryRefresh(sessionId),
      onReady: ({ sessionId }) => scheduleTrajectoryRefresh(sessionId),
      onError: (error, sessionId) => {
        if (activeSessionIdRef.current === sessionId) setTraceError(`Live trajectory unavailable: ${error.message}`)
      },
    })
    return () => trajectoryStream.setHandlers({})
  }, [scheduleTrajectoryRefresh, trajectoryStream])
  useEffect(() => {
    if (connected) void trajectoryStream.select(activeSession?.session_id ?? null)
  }, [activeSession?.session_id, connected, trajectoryStream])
  useEffect(() => {
    appDisposing.current = false
    return () => {
      appDisposing.current = true
      if (reconnectTimer.current !== null) window.clearTimeout(reconnectTimer.current)
      reconnectTimer.current = null
      cancelTrajectoryRefresh()
      trajectoryStream.dispose()
      socket.current?.close()
    }
  }, [cancelTrajectoryRefresh, trajectoryStream])
  useEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const updateSystemTheme = (event: MediaQueryListEvent) => setSystemPrefersDark(event.matches)
    setSystemPrefersDark(media.matches)
    media.addEventListener?.('change', updateSystemTheme)
    return () => media.removeEventListener?.('change', updateSystemTheme)
  }, [])
  useEffect(() => { document.documentElement.dataset.theme = theme }, [theme])
  useEffect(() => {
    try { lastValidGatewayUrl.current = normalizeGatewayUrl(url) } catch { /* Preserve the last valid endpoint while the user edits. */ }
    saveHarnessPreferences({ autoConnect, filesOpen, gatewayUrl: lastValidGatewayUrl.current, role, terminalOpen, theme: themePreference })
  }, [autoConnect, filesOpen, role, terminalOpen, themePreference, url])
  useEffect(() => {
    if (!isTauriRuntime()) saveGatewaySessionToken(token)
  }, [token])
  useEffect(() => {
    if (autoConnect || reconnectTimer.current === null) return
    window.clearTimeout(reconnectTimer.current)
    reconnectTimer.current = null
    setConnectionLabel('Gateway offline')
  }, [autoConnect])
  useEffect(() => {
    if (!autoConnect || autoConnectStarted.current) return
    if (isTauriRuntime()) return
    const timer = window.setTimeout(() => {
      if (autoConnectStarted.current) return
      autoConnectStarted.current = true
      connect(true)
    }, 0)
    return () => window.clearTimeout(timer)
  }, [autoConnect, connect])
  useEffect(() => {
    if (!autoConnect || autoConnectStarted.current || !isTauriRuntime()) return
    autoConnectStarted.current = true
    const rememberedWorkspace = loadTauriWorkspace()
    if (rememberedWorkspace) void bootstrapTauriWorkspace(rememberedWorkspace)
    else setConnectionLabel('Choose a workspace to start the local Gateway')
  }, [autoConnect, bootstrapTauriWorkspace])
  useEffect(() => {
    const media = window.matchMedia(COMPACT_VIEWPORT_QUERY)
    const syncViewport = (event: MediaQueryListEvent) => {
      setCompactViewport(event.matches)
      if (!event.matches) setMobilePanelOpen(false)
    }
    setCompactViewport(media.matches)
    media.addEventListener?.('change', syncViewport)
    return () => media.removeEventListener?.('change', syncViewport)
  }, [])
  useEffect(() => {
    if (!connected || !activeSession?.session_id) {
      workspaceTreeGeneration.current += 1
      setWorkspaceFiles([])
      setWorkspaceFilesLoading(false)
      setWorkspaceFilesError('')
      setWorkspaceFilesTruncated(false)
      return
    }
    void loadWorkspaceTree(activeSession.session_id)
  }, [activeSession?.session_id, connected, loadWorkspaceTree])

  const startNewSession = useCallback(() => {
    activeSessionIdRef.current = null
    workspaceTreeGeneration.current += 1
    cancelTrajectoryRefresh()
    setActiveSession(null)
    setItems([])
    setWorkspaceFiles([])
    setWorkspaceFilesError('')
    setWorkspaceFilesTruncated(false)
    setConversationTab('chat')
    setView('chat')
    setPanelCollapsed(false)
    setMobilePanelOpen(false)
    setTraceLoading(false)
  }, [cancelTrajectoryRefresh])

  const selectSession = useCallback(async (session: Session | null) => {
    activeSessionIdRef.current = session?.session_id ?? null
    workspaceTreeGeneration.current += 1
    cancelTrajectoryRefresh()
    setActiveSession(session)
    setItems([])
    setSelected('')
    setQuery('')
    setCollapsedTurns(new Set())
    setView('chat')
    setConversationTab('chat')
    setPanelCollapsed(Boolean(session))
    setMobilePanelOpen(false)
    if (!session?.session_id) return
    if (session.workspace_root) setWorkspace(session.workspace_root)
    await loadTrajectory(session.session_id)
  }, [cancelTrajectoryRefresh, loadTrajectory])

  const addAttachments = useCallback((fileList: FileList | null) => {
    const files = Array.from(fileList || [])
    const accepted: Attachment[] = []
    const rejected: string[] = []
    for (const file of files) {
      if (!ACCEPTED_MEDIA.has(file.type)) rejected.push(`${file.name}: unsupported type`)
      else if (file.size < 1 || file.size > MAX_ATTACHMENT_BYTES) rejected.push(`${file.name}: over 4 MiB`)
      else if (attachments.some((entry) => entry.file.name === file.name && entry.file.size === file.size && entry.file.lastModified === file.lastModified) || accepted.some((entry) => entry.file.name === file.name && entry.file.size === file.size)) rejected.push(`${file.name}: already attached`)
      else accepted.push({ file, status: 'ready' })
    }
    const total = attachments.reduce((sum, entry) => sum + entry.file.size, 0) + accepted.reduce((sum, entry) => sum + entry.file.size, 0)
    if (attachments.length + accepted.length > MAX_ATTACHMENTS) rejected.push(`Choose up to ${MAX_ATTACHMENTS} images`)
    if (total > MAX_ATTACHMENT_BYTES) rejected.push('Attachments must total 4 MiB or less')
    const allowed = attachments.length + accepted.length <= MAX_ATTACHMENTS && total <= MAX_ATTACHMENT_BYTES
    if (allowed && accepted.length) setAttachments((current) => [...current, ...accepted])
    if (rejected.length) setNotice(rejected[0])
  }, [attachments])

  const pickWorkspace = useCallback(async () => {
    const invoke = window.__TAURI__?.core?.invoke
    if (typeof invoke !== 'function') {
      const knownWorkspace = sessions.find((session) => session.workspace_root)?.workspace_root
      if (knownWorkspace) {
        setWorkspace((current) => current || knownWorkspace)
        setNotice('Web uses Gateway workspaces from the session roster. Select a session to switch workspace.')
      } else {
        setNotice('No Gateway workspace is available yet. Use Carina Harness for desktop to choose a new local workspace.')
      }
      return
    }
    try {
      const selectedPath = await invoke<string | null>('pick_workspace')
      if (selectedPath) {
        activeSessionIdRef.current = null
        workspaceTreeGeneration.current += 1
        cancelTrajectoryRefresh()
        setWorkspace(selectedPath)
        saveTauriWorkspace(selectedPath)
        setActiveSession(null)
        setItems([])
        setWorkspaceFiles([])
        setWorkspaceFilesError('')
        setWorkspaceFilesTruncated(false)
        setConversationTab('chat')
        setView('chat')
        setPanelCollapsed(false)
        void bootstrapTauriWorkspace(selectedPath, false)
      }
    } catch (error) {
      setNotice(`Workspace picker failed: ${error instanceof Error ? error.message : String(error)}`)
    }
  }, [bootstrapTauriWorkspace, cancelTrajectoryRefresh, sessions])

  const submitPrompt = useCallback(async () => {
    if (submitting) return
    const value = prompt.trim()
    if (!value) return
    if (!connected) {
      setNotice('Connect the Gateway before running a prompt.')
      return
    }
    if (activeRole !== 'operator') {
      setNotice('Operator mode is required to run a prompt.')
      return
    }
    if (!workspace) {
      setNotice('Choose a workspace before running a prompt.')
      return
    }
    setSubmitting(true)
    try {
      setAttachments((current) => current.map((entry) => ({ ...entry, status: 'uploading' })))
      const inputMedia = await Promise.all(attachments.map(async (entry): Promise<HarnessMediaInput> => ({
        media_type: entry.file.type,
        content_base64: bytesToBase64(new Uint8Array(await entry.file.arrayBuffer())),
        origin: entry.file.name,
      })))
      const result = await call<HarnessSubmitResponse>('harness.submit', {
        ...(activeSession?.session_id ? { session_id: activeSession.session_id } : {}),
        workspace_root: workspace,
        profile: 'safe-edit',
        prompt: value,
        agent: 'default',
        locale: document.documentElement.lang || 'en',
        client_submission_id: `web:${crypto.randomUUID()}`,
        ...(inputMedia.length ? { input_media: inputMedia } : {}),
      })
      const session = result?.session
      if (!session?.session_id) throw new Error('Gateway did not return a session')
      activeSessionIdRef.current = session.session_id
      workspaceTreeGeneration.current += 1
      setActiveSession(session)
      setSessions((current) => [session, ...current.filter((entry) => entry.session_id !== session.session_id)])
      setRosterStatus('ready')
      setRosterError('')
      setPanelCollapsed(true)
      setPrompt('')
      setAttachments([])
      setNotice('Run accepted. The trajectory will update from authoritative session events.')
      await loadTrajectory(session.session_id)
    } catch (error) {
      setAttachments((current) => current.map((entry) => ({ ...entry, status: 'ready' })))
      setNotice(`Run was not accepted: ${error instanceof Error ? error.message : String(error)}`)
    } finally {
      setSubmitting(false)
    }
  }, [activeRole, activeSession, attachments, call, connected, loadTrajectory, prompt, submitting, workspace])

  const turns = useMemo(() => deriveTrajectoryRows(items), [items])
  const allRows = useMemo(() => turns.flatMap((turn) => turn.rows), [turns])
  const visibleTurns = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return turns
    return turns.map((turn) => ({ ...turn, rows: turn.rows.filter((row) => `${row.kind} ${row.status} ${row.summary} ${row.content} ${turn.id}`.toLowerCase().includes(needle)) })).filter((turn) => turn.rows.length || `${turn.id} ${turn.prompt}`.toLowerCase().includes(needle))
  }, [query, turns])

  const selectedRow = useMemo(() => allRows.find((row) => `${row.turnId}:${row.id}` === selected), [allRows, selected])

  const openConversationTab = useCallback((tab: ConversationTab) => {
    setView('chat')
    setConversationTab(tab)
    if (tab === 'trajectory' && activeSession && !items.length) void loadTrajectory(activeSession.session_id)
  }, [activeSession, items.length, loadTrajectory])

  const emptyHarness = view === 'chat' && conversationTab === 'chat' && !activeSession
  const roleStatusLabel = activeRole
    ? activeRole === 'operator' ? 'Creator mode' : 'Observer mode'
    : role === 'operator' ? 'Operator selected' : 'Observer selected'

  const focusWorkspacePanelControl = useCallback(() => {
    const target = compactViewport
      ? (emptyHarness ? workspaceBrandCollapseButtonRef.current : workspaceMobileCloseButtonRef.current)
      : (emptyHarness ? workspaceBrandCollapseButtonRef.current : workspacePanelCollapseButtonRef.current)
    target?.focus({ preventScroll: true })
  }, [compactViewport, emptyHarness])

  const openWorkspacePanel = useCallback(() => {
    setPanelCollapsed(false)
    window.requestAnimationFrame(focusWorkspacePanelControl)
  }, [focusWorkspacePanelControl])

  const collapseWorkspacePanel = useCallback(() => {
    setPanelCollapsed(true)
    setMobilePanelOpen(false)
    window.requestAnimationFrame(() => {
      const target = compactViewport ? mobileMenuButtonRef.current : sidebarOpenButtonRef.current
      target?.focus({ preventScroll: true })
    })
  }, [compactViewport])

  const openMobileWorkspacePanel = useCallback(() => {
    setMobilePanelOpen(true)
    window.requestAnimationFrame(focusWorkspacePanelControl)
  }, [focusWorkspacePanelControl])

  const closeMobileWorkspacePanel = useCallback(() => {
    setMobilePanelOpen(false)
    window.requestAnimationFrame(() => mobileMenuButtonRef.current?.focus({ preventScroll: true }))
  }, [])

  const handleWorkspacePanelKeyDown = useCallback((event: KeyboardEvent<HTMLElement>) => {
    if (!mobilePanelOpen) return
    if (event.key === 'Escape') {
      event.preventDefault()
      closeMobileWorkspacePanel()
      return
    }
    if (event.key !== 'Tab') return
    const panel = workspacePanelRef.current
    if (!panel) return
    const focusable = [...panel.querySelectorAll<HTMLElement>(WORKSPACE_FOCUSABLE_SELECTOR)].filter((element) => element.getClientRects().length > 0 && window.getComputedStyle(element).visibility !== 'hidden')
    if (!focusable.length) {
      event.preventDefault()
      panel.focus()
      return
    }
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }, [closeMobileWorkspacePanel, mobilePanelOpen])

  return <div className={cn('react-harness', theme, emptyHarness && 'empty-shell', panelCollapsed && 'panel-collapsed', mobilePanelOpen && 'mobile-panel-open', !filesOpen && 'files-collapsed')}>
    <div className="sr-only react-connection-announcer" role="status" aria-live="polite" aria-atomic="true">{connectionLabel}</div>
    <aside className="react-rail" aria-label="Carina navigation" aria-hidden={compactViewport || (emptyHarness && !panelCollapsed) ? true : undefined}>
      {panelCollapsed ? <Button ref={sidebarOpenButtonRef} className="react-sidebar-toggle" variant="ghost" size="icon" type="button" aria-label="Open sidebar" title="Open sidebar" aria-controls="workspace-panel" aria-expanded="false" onClick={openWorkspacePanel}><img className="react-sidebar-toggle-symbol" src={asset(`logo/${theme === 'carina-light' ? 'carina-symbol.svg' : 'carina-symbol-high-contrast.svg'}`)} alt="" aria-hidden="true" /><PanelLeftOpen className="react-sidebar-toggle-icon" aria-hidden="true" /></Button> : <Button className="react-brand" variant="ghost" size="icon" type="button" onClick={startNewSession} title="New session" aria-label="New session"><img src={asset(`logo/${theme === 'carina-light' ? 'carina-symbol.svg' : 'carina-symbol-high-contrast.svg'}`)} alt="Carina" /></Button>}
      <nav className="react-rail-group" aria-label="Operator views">
        {panelCollapsed && <RailButton active={false} label="New session" onClick={startNewSession}><Plus /></RailButton>}
        <RailButton active={view === 'chat'} label="Harness" onClick={() => setView('chat')}><Terminal /></RailButton>
        <RailButton active={view === 'inbox'} label="Inbox" onClick={() => setView('inbox')}><Inbox /></RailButton>
        <RailButton active={view === 'runs'} label="Runs" onClick={() => setView('runs')}><ListChecks /></RailButton>
        <RailButton active={view === 'graph'} label="Graph" onClick={() => setView('graph')}><CircleDot /></RailButton>
      </nav>
      <div className="react-rail-spacer" />
      <RailButton active={false} label="Settings" onClick={() => openSettings('general')}><Settings2 /></RailButton>
    </aside>

    <aside ref={workspacePanelRef} id="workspace-panel" className={cn('react-workspace', mobilePanelOpen && 'mobile-open')} role={mobilePanelOpen ? 'dialog' : undefined} aria-modal={mobilePanelOpen ? true : undefined} aria-label="Workspace and sessions" aria-hidden={!mobilePanelOpen && (panelCollapsed || compactViewport) ? true : undefined} tabIndex={mobilePanelOpen ? -1 : undefined} onKeyDown={handleWorkspacePanelKeyDown}>
      <div className="react-workspace-brand">{theme === 'carina-light' ? <img className="react-workspace-wordmark" src={asset('logo/carina-horizontal-brand.svg')} alt="Carina Harness" /> : <svg className="react-workspace-wordmark react-workspace-wordmark-svg" role="img" aria-label="Carina Harness" viewBox="0 0 547 240"><use href={`${asset('logo/carina-sprite.svg')}#carina-horizontal-monochrome`} /></svg>}<IconButton ref={workspaceBrandCollapseButtonRef} icon={PanelLeftClose} label="Collapse workspace panel" aria-controls="workspace-panel" aria-expanded="true" onClick={collapseWorkspacePanel} /></div>
      <Button className="react-new-session-button" variant="outline" type="button" onClick={startNewSession}><Plus /><span>New Session</span></Button>
      <nav className="react-workspace-nav" aria-label="Workspace tools"><Button variant="ghost" type="button"><ListChecks /><span>Task Board</span></Button><Button variant="ghost" type="button"><Folder /><span>Skill Center</span></Button></nav>
      <div className="react-workspaces-heading"><span>Workspaces</span><span><IconButton icon={Search} label="Search workspaces" /><IconButton icon={Settings2} label="Workspace settings" onClick={() => openSettings('interface')} /><IconButton icon={Plus} label="Add workspace" onClick={() => void pickWorkspace()} /></span></div>
      <div className="react-panel-heading"><div><span className="eyebrow">CARINA HARNESS</span><h1>Workspace</h1></div><span className="react-panel-actions"><Button ref={workspaceMobileCloseButtonRef} className="react-mobile-close" variant="ghost" size="icon" onClick={closeMobileWorkspacePanel} aria-label="Close workspace panel" title="Close workspace panel"><X /></Button><Button ref={workspacePanelCollapseButtonRef} variant="ghost" size="icon" onClick={collapseWorkspacePanel} aria-label="Collapse workspace panel" title="Collapse workspace panel" aria-controls="workspace-panel" aria-expanded="true"><PanelLeftClose /></Button></span></div>
      <Button className="react-workspace-picker" variant="outline" type="button" onClick={() => void pickWorkspace()}><Folder /><span><strong>{workspace ? workspace.split(/[\\/]/).filter(Boolean).pop() : 'Choose workspace'}</strong><small>{workspace || 'Connect a local gateway to begin'}</small></span><ChevronDown /></Button>
      <div className="react-session-heading"><span>Sessions</span><Button variant="ghost" size="icon-sm" onClick={startNewSession} aria-label="New session" title="New session"><Plus /></Button></div>
      <SessionList sessions={sessions} activeSession={activeSession} onSelect={selectSession} status={rosterStatus} error={rosterError} onRetry={() => void refreshRoster()} />
      <div className="react-panel-footer"><div className="connection-line"><span className={cn('status-dot', connected ? 'online' : 'busy')} aria-hidden="true" />{connectionLabel}</div><Button className="react-link" variant="ghost" type="button" onClick={() => openSettings('gateway')}>Gateway settings</Button></div>
    </aside>
    {mobilePanelOpen && <Button className="react-mobile-backdrop" variant="ghost" type="button" aria-label="Close workspace panel" onClick={closeMobileWorkspacePanel} />}

    <div className="react-main-column">
      <main className="react-main">
      <header className="react-topbar"><Button ref={mobileMenuButtonRef} className="react-mobile-menu" variant="ghost" size="icon" onClick={openMobileWorkspacePanel} aria-label="Open workspace panel" title="Open workspace panel" aria-expanded={mobilePanelOpen} aria-controls="workspace-panel"><Menu /></Button><div className="react-conversation-heading"><strong>{activeSession?.title || 'New Conversation'}</strong><Badge tone={activeRole === 'operator' ? 'accent' : 'neutral'}>{roleStatusLabel}</Badge></div><div className="react-topbar-actions"><Button className="react-session-log" variant="outline" size="compact" onClick={() => openConversationTab('trajectory')}><Icon icon={Download} /><span>Session log</span></Button><Button variant="ghost" size="icon" onClick={() => setThemePreference(theme === 'carina-light' ? 'dark' : 'light')} aria-label="Toggle theme" title="Toggle theme">{theme === 'carina-light' ? <Sun /> : <Sparkles />}</Button><Button className="react-files-toggle" variant="ghost" size="icon" onClick={() => setFilesOpen((open) => !open)} aria-label={filesOpen ? 'Collapse files panel' : 'Open files panel'} title={filesOpen ? 'Collapse files panel' : 'Open files panel'}><Folder /></Button><Button className="react-connect-button" variant="outline" onClick={() => openSettings('gateway')} aria-label={connected ? 'Connected' : connecting ? 'Connecting to gateway' : 'Connect gateway'} title={connectionLabel}><Plug /><span>{connected ? 'Connected' : connecting ? 'Connecting' : 'Connect gateway'}</span></Button></div></header>
      {view === 'chat' && <ConversationTabs value={conversationTab} onChange={openConversationTab} />}
      {view === 'chat' && conversationTab === 'trajectory' ? <TrajectoryView activeSession={activeSession} turns={visibleTurns} allRows={allRows} selectedRow={selectedRow} selected={selected} setSelected={setSelected} query={query} setQuery={setQuery} collapsedTurns={collapsedTurns} setCollapsedTurns={setCollapsedTurns} traceLoading={traceLoading} traceError={traceError} traceTruncated={traceTruncated} onRefresh={() => { if (activeSession) void loadTrajectory(activeSession.session_id) }} onOpenRuns={() => setView('runs')} /> : view === 'chat' && conversationTab === 'context' ? <ContextPanel workspace={workspace} role={activeRole} requestedRole={role} connected={connected} itemCount={items.length} /> : <Surface view={view === 'trajectory' ? 'chat' : view} activeSession={activeSession} items={items} sessions={sessions} rosterStatus={rosterStatus} rosterError={rosterError} role={activeRole} requestedRole={role} workspace={workspace} connected={connected} prompt={prompt} setPrompt={setPrompt} attachments={attachments} addAttachments={addAttachments} removeAttachment={(index) => setAttachments((current) => current.filter((_, entryIndex) => entryIndex !== index))} fileInputRef={fileInputRef} settingsTriggerRef={settingsTriggerRef} onSubmit={submitPrompt} onSelectSession={selectSession} onRefreshRoster={() => void refreshRoster()} onOpenConnection={() => openSettings('gateway')} onPickWorkspace={() => void pickWorkspace()} submitting={submitting} />}
      {notice && <div className="react-notice" role="status">{notice}</div>}
      </main>
      <TerminalPanel workspace={workspace} open={terminalOpen} onToggle={() => setTerminalOpen((open) => !open)} />
    </div>
    <FilesPanel workspace={workspace} sessionId={activeSession?.session_id || ''} files={workspaceFiles} loading={workspaceFilesLoading} error={workspaceFilesError} truncated={workspaceFilesTruncated} open={filesOpen} onChooseWorkspace={() => void pickWorkspace()} onRefresh={() => { if (activeSession?.session_id) void loadWorkspaceTree(activeSession.session_id) }} onClose={() => setFilesOpen(false)} onOpen={() => setFilesOpen(true)} />
    <SettingsDialog open={settingsOpen} initialSection={settingsSection} onSectionChange={setSettingsSection} onClose={closeSettings} restoreFocusRef={settingsReturnFocusRef} autoConnect={autoConnect} onAutoConnectChange={setAutoConnect} role={role} onRoleChange={setRole} theme={themePreference} onThemeChange={setThemePreference} filesOpen={filesOpen} onFilesOpenChange={setFilesOpen} terminalOpen={terminalOpen} onTerminalOpenChange={setTerminalOpen} gatewayUrl={url} onGatewayUrlChange={setUrl} token={token} onTokenChange={setToken} urlError={gatewayUrlError} connected={connected} connecting={connecting} connectionLabel={connectionLabel} onConnect={() => connect(false)} />
  </div>
}

function RailButton({ active, label, onClick, children }: RailButtonProps) {
  return <Button variant="ghost" size="icon" className={cn('react-rail-button', active && 'active')} type="button" onClick={onClick} aria-label={label} title={label} aria-current={active ? 'page' : undefined}>{children}</Button>
}

export default App
