export type GatewayRole = 'observer' | 'operator'
export type ThemePreference = 'light' | 'dark' | 'system'

export type HarnessPreferences = {
  autoConnect: boolean
  filesOpen: boolean
  gatewayUrl: string
  role: GatewayRole
  terminalOpen: boolean
  theme: ThemePreference
}

export const DEFAULT_GATEWAY_URL = 'ws://127.0.0.1:8777/gateway'
export const DEFAULT_HARNESS_PREFERENCES: HarnessPreferences = {
  autoConnect: true,
  filesOpen: true,
  gatewayUrl: DEFAULT_GATEWAY_URL,
  role: 'observer',
  terminalOpen: true,
  theme: 'system',
}

const PREFERENCES_KEY = 'carina.harness.preferences.v1'
const SESSION_TOKEN_KEY = 'carina.harness.gateway-token.v1'
const TAURI_WORKSPACE_KEY = 'carina.harness.tauri-workspace.v1'

type StorageReader = Pick<Storage, 'getItem'>
type StorageWriter = Pick<Storage, 'setItem'>

function browserStorage(kind: 'localStorage' | 'sessionStorage'): Storage | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    return window[kind]
  } catch {
    return undefined
  }
}

export function normalizeGatewayUrl(value: string): string {
  let parsed: URL
  try {
    parsed = new URL(value.trim())
  } catch {
    throw new Error('Enter a valid WebSocket URL')
  }
  if (parsed.username || parsed.password) throw new Error('Gateway URLs cannot contain credentials')
  const loopback = parsed.hostname === '127.0.0.1' || parsed.hostname === '[::1]' || /^127(?:\.\d{1,3}){3}$/.test(parsed.hostname)
  if (parsed.protocol !== 'wss:' && !(parsed.protocol === 'ws:' && loopback)) {
    throw new Error('Use wss://, or ws:// with an explicit loopback IP')
  }
  if (parsed.pathname === '/' || parsed.pathname === '') parsed.pathname = '/gateway'
  parsed.hash = ''
  return parsed.toString().replace(/\/$/, '')
}

export function loadHarnessPreferences(storage: StorageReader | undefined = browserStorage('localStorage')): HarnessPreferences {
  if (!storage) return DEFAULT_HARNESS_PREFERENCES
  try {
    const raw = storage.getItem(PREFERENCES_KEY)
    if (!raw) return DEFAULT_HARNESS_PREFERENCES
    const parsed = JSON.parse(raw) as Partial<HarnessPreferences>
    return {
      autoConnect: typeof parsed.autoConnect === 'boolean' ? parsed.autoConnect : DEFAULT_HARNESS_PREFERENCES.autoConnect,
      filesOpen: typeof parsed.filesOpen === 'boolean' ? parsed.filesOpen : DEFAULT_HARNESS_PREFERENCES.filesOpen,
      gatewayUrl: typeof parsed.gatewayUrl === 'string' ? normalizeGatewayUrl(parsed.gatewayUrl) : DEFAULT_HARNESS_PREFERENCES.gatewayUrl,
      role: parsed.role === 'operator' || parsed.role === 'observer' ? parsed.role : DEFAULT_HARNESS_PREFERENCES.role,
      terminalOpen: typeof parsed.terminalOpen === 'boolean' ? parsed.terminalOpen : DEFAULT_HARNESS_PREFERENCES.terminalOpen,
      theme: parsed.theme === 'light' || parsed.theme === 'dark' || parsed.theme === 'system' ? parsed.theme : DEFAULT_HARNESS_PREFERENCES.theme,
    }
  } catch {
    return DEFAULT_HARNESS_PREFERENCES
  }
}

export function saveHarnessPreferences(preferences: HarnessPreferences, storage: StorageWriter | undefined = browserStorage('localStorage')): void {
  try {
    storage?.setItem(PREFERENCES_KEY, JSON.stringify(preferences))
  } catch {
    // Hardened/private browsers can deny storage; runtime preferences still work.
  }
}

export function loadGatewaySessionToken(storage: StorageReader | undefined = browserStorage('sessionStorage')): string {
  try {
    return storage?.getItem(SESSION_TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

export function saveGatewaySessionToken(token: string, storage: StorageWriter | undefined = browserStorage('sessionStorage')): void {
  try {
    storage?.setItem(SESSION_TOKEN_KEY, token)
  } catch {
    // Keep the token in React memory when session storage is unavailable.
  }
}

export function loadTauriWorkspace(storage: StorageReader | undefined = browserStorage('localStorage')): string {
  try {
    return storage?.getItem(TAURI_WORKSPACE_KEY)?.trim() ?? ''
  } catch {
    return ''
  }
}

export function saveTauriWorkspace(workspace: string, storage: StorageWriter | undefined = browserStorage('localStorage')): void {
  try {
    const value = workspace.trim()
    if (value) storage?.setItem(TAURI_WORKSPACE_KEY, value)
  } catch {
    // Hardened/private browsers can deny storage; the current selection still works in memory.
  }
}

export function resolveTheme(preference: ThemePreference, systemPrefersDark: boolean): 'carina-light' | 'carina-dark' {
  if (preference === 'system') return systemPrefersDark ? 'carina-dark' : 'carina-light'
  return preference === 'dark' ? 'carina-dark' : 'carina-light'
}
