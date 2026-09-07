import assert from 'node:assert/strict'
import {
  DEFAULT_GATEWAY_URL,
  DEFAULT_HARNESS_PREFERENCES,
  loadGatewaySessionToken,
  loadHarnessPreferences,
  loadTauriWorkspace,
  normalizeGatewayUrl,
  resolveTheme,
  saveGatewaySessionToken,
  saveHarnessPreferences,
  saveTauriWorkspace,
} from './src-app/lib/harness-preferences.ts'

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial))
  return {
    values,
    getItem(key) { return values.get(key) ?? null },
    setItem(key, value) { values.set(key, String(value)) },
  }
}

assert.deepEqual(loadHarnessPreferences(), DEFAULT_HARNESS_PREFERENCES)
assert.equal(DEFAULT_GATEWAY_URL, 'ws://127.0.0.1:8777/gateway')

const malformed = memoryStorage({ 'carina.harness.preferences.v1': '{not-json' })
assert.deepEqual(loadHarnessPreferences(malformed), DEFAULT_HARNESS_PREFERENCES)
assert.deepEqual(loadHarnessPreferences({ getItem() { throw new Error('storage denied') } }), DEFAULT_HARNESS_PREFERENCES)

const partial = memoryStorage({
  'carina.harness.preferences.v1': JSON.stringify({
    autoConnect: false,
    filesOpen: 'invalid',
    gatewayUrl: '  wss://gateway.example.test  ',
    role: 'operator',
    terminalOpen: false,
    theme: 'invalid',
  }),
})
assert.deepEqual(loadHarnessPreferences(partial), {
  autoConnect: false,
  filesOpen: true,
  gatewayUrl: 'wss://gateway.example.test/gateway',
  role: 'operator',
  terminalOpen: false,
  theme: 'system',
})

const unsafePersisted = memoryStorage({
  'carina.harness.preferences.v1': JSON.stringify({ gatewayUrl: 'ws://gateway.example.test' }),
})
assert.deepEqual(loadHarnessPreferences(unsafePersisted), DEFAULT_HARNESS_PREFERENCES)

assert.equal(normalizeGatewayUrl(' ws://127.0.0.1:8777 '), DEFAULT_GATEWAY_URL)
assert.equal(normalizeGatewayUrl('ws://127.12.34.56:8765/custom#debug'), 'ws://127.12.34.56:8765/custom')
assert.equal(normalizeGatewayUrl('ws://[::1]:8765'), 'ws://[::1]:8765/gateway')
assert.equal(normalizeGatewayUrl('wss://gateway.example.test'), 'wss://gateway.example.test/gateway')
assert.equal(normalizeGatewayUrl('wss://gateway.example.test/rpc?tenant=demo#debug'), 'wss://gateway.example.test/rpc?tenant=demo')
assert.throws(() => normalizeGatewayUrl('not a URL'), /valid WebSocket URL/)
assert.throws(() => normalizeGatewayUrl('https://gateway.example.test'), /Use wss:\/\//)
assert.throws(() => normalizeGatewayUrl('ws://localhost:8765'), /explicit loopback IP/)
assert.throws(() => normalizeGatewayUrl('ws://192.168.1.5:8765'), /explicit loopback IP/)
assert.throws(() => normalizeGatewayUrl('wss://user:secret@gateway.example.test'), /cannot contain credentials/)

assert.equal(resolveTheme('light', true), 'carina-light')
assert.equal(resolveTheme('dark', false), 'carina-dark')
assert.equal(resolveTheme('system', false), 'carina-light')
assert.equal(resolveTheme('system', true), 'carina-dark')

const local = memoryStorage()
const session = memoryStorage()
const previousWindow = globalThis.window
globalThis.window = { localStorage: local, sessionStorage: session }
try {
  const preferences = { ...DEFAULT_HARNESS_PREFERENCES, autoConnect: false, role: 'operator' }
  saveHarnessPreferences(preferences)
  saveGatewaySessionToken('tab-scoped-secret')

  assert.deepEqual(loadHarnessPreferences(), preferences)
  assert.equal(loadGatewaySessionToken(), 'tab-scoped-secret')
  assert.equal(local.values.has('carina.harness.gateway-token.v1'), false)
  assert.equal(session.values.has('carina.harness.preferences.v1'), false)
  assert.doesNotMatch(local.values.get('carina.harness.preferences.v1') ?? '', /tab-scoped-secret/)
} finally {
  if (previousWindow === undefined) delete globalThis.window
  else globalThis.window = previousWindow
}

assert.equal(loadGatewaySessionToken({ getItem() { throw new Error('storage denied') } }), '')
assert.doesNotThrow(() => saveHarnessPreferences(DEFAULT_HARNESS_PREFERENCES, { setItem() { throw new Error('storage denied') } }))
assert.doesNotThrow(() => saveGatewaySessionToken('secret', { setItem() { throw new Error('storage denied') } }))

const workspaceStorage = memoryStorage({ 'carina.harness.tauri-workspace.v1': '  /tmp/carina-workspace  ' })
assert.equal(loadTauriWorkspace(workspaceStorage), '/tmp/carina-workspace')
saveTauriWorkspace('  /tmp/next-workspace  ', workspaceStorage)
assert.equal(loadTauriWorkspace(workspaceStorage), '/tmp/next-workspace')
assert.doesNotThrow(() => saveTauriWorkspace('/tmp/workspace', { setItem() { throw new Error('storage denied') } }))

console.log('Harness preference and session token boundaries: ok')
