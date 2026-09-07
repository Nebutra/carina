import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

type GatewayFixtureOptions = {
  closeBeforeHelloResponseAttempts?: number
  failConnectionAttempts?: number
  rejectHello?: boolean
  stallConnectionAttempts?: number
}

async function installGatewayFixture(page: Page, options: GatewayFixtureOptions = {}) {
  await page.addInitScript(({ closeBeforeHelloResponseAttempts, failConnectionAttempts, rejectHello, stallConnectionAttempts }) => {
    const rpcLog: Array<{ method: string; params: Record<string, unknown> }> = []
    let socketAttempts = 0
    Object.defineProperty(window, '__carinaRpcLog', { configurable: true, value: rpcLog })
    Object.defineProperty(window, '__carinaSocketAttempts', { configurable: true, get: () => socketAttempts })
    const sessions = [{
      session_id: 'session-smoke',
      title: 'Smoke session',
      status: 'completed',
      workspace_root: '/tmp/carina-smoke',
    }]

    class FakeWebSocket {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSING = 2
      static readonly CLOSED = 3

      readonly url: string
      readyState = FakeWebSocket.CONNECTING
      onopen: ((event: Event) => void) | null = null
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: ((event: CloseEvent) => void) | null = null
      onerror: ((event: Event) => void) | null = null

      constructor(url: string | URL) {
        this.url = String(url)
        socketAttempts += 1
        queueMicrotask(() => {
          if (socketAttempts <= (stallConnectionAttempts || 0)) return
          if (socketAttempts <= (failConnectionAttempts || 0)) {
            this.readyState = FakeWebSocket.CLOSED
            this.onerror?.(new Event('error'))
            this.onclose?.(new CloseEvent('close'))
            return
          }
          this.readyState = FakeWebSocket.OPEN
          this.onopen?.(new Event('open'))
        })
      }

      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        const request = JSON.parse(String(data)) as { id: number; method: string; params?: Record<string, unknown> }
        rpcLog.push({ method: request.method, params: request.params || {} })
        if (request.method === 'gateway.hello' && socketAttempts <= (closeBeforeHelloResponseAttempts || 0)) {
          this.readyState = FakeWebSocket.CLOSED
          queueMicrotask(() => this.onclose?.(new CloseEvent('close')))
          return
        }
        let result: unknown = {}
        let rpcError: { message: string } | undefined
        if (request.method === 'gateway.hello' && rejectHello) rpcError = { message: 'gateway authorization required' }
        else if (request.method === 'session.list') result = sessions
        else if (request.method === 'session.items') result = { data: [], next_cursor: null }
        else if (request.method === 'harness.workspace.tree') result = {
          files: [{ path: 'README.md', size: 512, binary: false, large: false, language: 'Markdown', mtime: 0 }],
          truncated: false,
        }
        else if (request.method === 'harness.submit') {
          if (request.params?.prompt === 'Reject this run') rpcError = { message: 'submission rejected' }
          else result = {
            session: {
              session_id: 'session-submitted',
              title: 'Submitted session',
              status: 'active',
              workspace_root: '/tmp/carina-smoke',
            },
            execution: { task_id: 'run-submitted', status: 'queued' },
          }
        }
        else if (request.method === 'session.events.stream') result = {
          subscription_id: 'stream-smoke', cursor: 0, replayed: 0, event_mode: 'canonical',
        }
        else if (request.method === 'session.events.unsubscribe') result = { unsubscribed: true }
        queueMicrotask(() => this.onmessage?.(new MessageEvent('message', {
          data: JSON.stringify({ jsonrpc: '2.0', id: request.id, ...(rpcError ? { error: rpcError } : { result }) }),
        })))
      }

      close() {
        if (this.readyState === FakeWebSocket.CLOSED) return
        this.readyState = FakeWebSocket.CLOSED
        queueMicrotask(() => this.onclose?.(new CloseEvent('close')))
      }

      addEventListener() {}
      removeEventListener() {}
      dispatchEvent() { return true }
    }

    Object.defineProperty(window, 'WebSocket', { configurable: true, value: FakeWebSocket })
  }, options)
}

async function waitForRpc(page: Page, method: string) {
  await expect.poll(() => page.evaluate((expectedMethod) => (
    window as Window & { __carinaRpcLog: Array<{ method: string }> }
  ).__carinaRpcLog.some((entry) => entry.method === expectedMethod), method)).toBe(true)
}

async function openHarness(page: Page, options: GatewayFixtureOptions & { expectConnected?: boolean } = {}) {
  await installGatewayFixture(page, options)
  await page.goto('./', { waitUntil: 'networkidle' })
  await expect(page.locator('#root')).not.toBeEmpty()
  if (options.expectConnected !== false) await waitForRpc(page, 'runtime.initialize')
}

async function reconnectAsOperator(page: Page) {
  await page.getByRole('button', { name: 'Observer mode', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('heading', { name: 'Gateway', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Operator', exact: true }).click()
  await dialog.getByRole('button', { name: 'Reconnect as Operator', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(page.locator('.connection-line').getByText('Operator · connected', { exact: true })).toBeVisible()
}

async function selectFixtureSession(page: Page) {
  await reconnectAsOperator(page)
  await page.getByRole('button', { name: 'Smoke session', exact: true }).click()
  await expect(page.locator('.react-harness')).not.toHaveClass(/empty-shell/)
}

test('serves the extracted build at archive root with contained relative assets', async ({ page }) => {
  const failures: string[] = []
  page.on('requestfailed', (request) => failures.push(`${request.method()} ${request.url()}: ${request.failure()?.errorText || 'failed'}`))
  page.on('response', (response) => {
    if (response.status() >= 400) failures.push(`${response.status()} ${response.url()}`)
  })

  await openHarness(page)
  await expect(page).toHaveURL(/^http:\/\/127\.0\.0\.1:\d+\/$/)
  const resources = await page.locator('script[src], link[href], img[src], use[href]').evaluateAll((nodes) => nodes.flatMap((node) => {
    const value = node.getAttribute('src') || node.getAttribute('href')
    if (!value || value.startsWith('#')) return []
    const resolved = new URL(value, document.baseURI)
    return [{ raw: value, origin: resolved.origin, pathname: resolved.pathname }]
  }))

  expect(resources.length).toBeGreaterThan(0)
  expect(resources.every(({ raw }) => raw.startsWith('./') && !raw.includes('../'))).toBe(true)
  expect(resources.every(({ origin }) => origin === new URL(page.url()).origin)).toBe(true)
  expect(resources.every(({ pathname }) => pathname.startsWith('/') && !pathname.startsWith('/integrations/web/'))).toBe(true)
  expect(failures).toEqual([])
})

test('launch silently connects through the ordered Gateway handshake', async ({ page }) => {
  await openHarness(page)

  await expect(page.getByRole('dialog')).toHaveCount(0)
  const log = await page.evaluate(() => (window as Window & { __carinaRpcLog: Array<{ method: string; params: Record<string, unknown> }> }).__carinaRpcLog)
  expect(log.slice(0, 2).map((entry) => entry.method)).toEqual(['gateway.hello', 'runtime.initialize'])
  expect(log[0]?.params).toMatchObject({ role: 'observer', scopes: ['read', 'stream'] })
})

test('transient network failure reconnects without opening Settings', async ({ page }) => {
  await openHarness(page, { failConnectionAttempts: 1 })

  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(await page.evaluate(() => (window as Window & { __carinaSocketAttempts: number }).__carinaSocketAttempts)).toBe(2)
})

test('clean close before the Gateway handshake completes reconnects automatically', async ({ page }) => {
  await openHarness(page, { closeBeforeHelloResponseAttempts: 1 })

  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(await page.evaluate(() => (window as Window & { __carinaSocketAttempts: number }).__carinaSocketAttempts)).toBe(2)
})

test('disconnected role copy distinguishes the requested role from active authorization', async ({ page }) => {
  await page.addInitScript(() => window.localStorage.setItem('carina.harness.preferences.v1', JSON.stringify({
    autoConnect: true,
    filesOpen: true,
    gatewayUrl: 'ws://127.0.0.1:8765/gateway',
    role: 'operator',
    terminalOpen: true,
    theme: 'system',
  })))
  await openHarness(page, { expectConnected: false, stallConnectionAttempts: 1 })

  await expect(page.getByRole('button', { name: 'Operator selected', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect the Gateway before running', exact: true })).toBeVisible()
  await expect(page.getByText('Switch to Operator to run', { exact: true })).toHaveCount(0)
})

test('disabling auto-connect cancels a queued retry and resets its status', async ({ page }) => {
  await openHarness(page, { expectConnected: false, failConnectionAttempts: 100 })
  await expect(page.locator('.connection-line')).toContainText('retrying in')
  await page.getByRole('button', { name: 'Observer selected', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await dialog.getByRole('button', { name: 'General', exact: true }).click()
  await dialog.getByRole('switch', { name: 'Connect on launch', exact: true }).uncheck()
  await dialog.getByRole('button', { name: 'Done', exact: true }).click()

  const attemptsAtCancel = await page.evaluate(() => (window as Window & { __carinaSocketAttempts: number }).__carinaSocketAttempts)
  const pendingDelay = Math.min(750 * 2 ** Math.max(0, attemptsAtCancel - 1), 12000)
  await expect(page.locator('.react-connection-announcer')).toHaveText('Gateway offline')
  await page.waitForTimeout(pendingDelay + 250)
  expect(await page.evaluate(() => (window as Window & { __carinaSocketAttempts: number }).__carinaSocketAttempts)).toBe(attemptsAtCancel)
})

test('failed automatic authorization stays quiet until the user opens Gateway settings', async ({ page }) => {
  await openHarness(page, { expectConnected: false, rejectHello: true })
  await waitForRpc(page, 'gateway.hello')

  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.locator('.connection-line').getByText('gateway authorization required', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Gateway settings', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await expect(dialog.getByRole('heading', { name: 'Gateway', exact: true })).toBeVisible()
})

test('desktop workspace rail reveals its single expand control from the Carina mark', async ({ page }) => {
  await openHarness(page)
  const collapse = page.getByRole('button', { name: 'Collapse workspace panel', exact: true })
  await expect(collapse).toBeVisible()
  await collapse.click()

  const visibleOpenControls = page.locator('button[aria-label="Open sidebar"]:visible')
  await expect(visibleOpenControls).toHaveCount(1)
  await expect(visibleOpenControls).toBeFocused()
  let previousBounds = ''
  await expect.poll(async () => {
    const bounds = await visibleOpenControls.boundingBox()
    const currentBounds = bounds ? `${bounds.x}:${bounds.y}:${bounds.width}:${bounds.height}` : ''
    const stable = currentBounds !== '' && currentBounds === previousBounds
    previousBounds = currentBounds
    return stable
  }).toBe(true)
  const symbol = visibleOpenControls.locator('.react-sidebar-toggle-symbol')
  const expandIcon = visibleOpenControls.locator('.react-sidebar-toggle-icon')
  await expect(symbol).toBeVisible()
  await expect(symbol).toHaveCSS('opacity', '1')
  await expect(expandIcon).toHaveCSS('opacity', '0')

  await visibleOpenControls.hover()
  await expect(symbol).toHaveCSS('opacity', '0')
  await expect(expandIcon).toHaveCSS('opacity', '1')

  await page.mouse.move(200, 200)
  await visibleOpenControls.focus()
  await page.keyboard.press('Tab')
  await page.keyboard.press('Shift+Tab')
  await expect(visibleOpenControls).toBeFocused()
  await expect(symbol).toHaveCSS('opacity', '0')
  await expect(expandIcon).toHaveCSS('opacity', '1')

  await visibleOpenControls.click()
  await expect(page.locator('button[aria-label="Collapse workspace panel"]:visible')).toHaveCount(1)
  await expect(page.locator('button[aria-label="Collapse workspace panel"]:visible')).toBeFocused()
  await expect(visibleOpenControls).toHaveCount(0)
})

test('mobile starts on the conversation and opens the workspace as an off-canvas panel', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openHarness(page)
  const workspacePanel = page.locator('#workspace-panel')
  await expect(workspacePanel).toBeHidden()
  await expect(workspacePanel).toHaveAttribute('aria-hidden', 'true')
  await expect(page.getByRole('textbox', { name: 'Prompt', exact: true })).toBeVisible()

  const openPanel = page.getByRole('button', { name: 'Open workspace panel', exact: true })
  await openPanel.click()
  await expect(workspacePanel).toBeVisible()
  await expect(workspacePanel).toHaveAttribute('role', 'dialog')
  await expect(workspacePanel.getByRole('button', { name: 'Collapse workspace panel', exact: true })).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  expect(await page.evaluate(() => document.querySelector('#workspace-panel')?.contains(document.activeElement))).toBe(true)
  await page.keyboard.press('Escape')
  await expect(workspacePanel).toBeHidden()
  await expect(openPanel).toBeFocused()
  await expect(page.getByRole('textbox', { name: 'Prompt', exact: true })).toBeVisible()
})

test('new runs use the bounded Harness composition and collapse to the persistent rail', async ({ page }) => {
  await openHarness(page)
  await reconnectAsOperator(page)

  await page.getByRole('textbox', { name: 'Prompt', exact: true }).fill('Inspect the workspace')
  await page.getByRole('button', { name: 'Run prompt', exact: true }).click()
  await expect(page.getByText('Run accepted. The trajectory will update from authoritative session events.')).toBeVisible()
  await expect(page.locator('.react-harness')).toHaveClass(/panel-collapsed/)
  await expect(page.locator('button[aria-label="Open sidebar"]:visible')).toHaveCount(1)

  const log = await page.evaluate(() => (window as Window & { __carinaRpcLog: Array<{ method: string; params: Record<string, unknown> }> }).__carinaRpcLog)
  const submission = log.find((entry) => entry.method === 'harness.submit')
  const hello = log.filter((entry) => entry.method === 'gateway.hello').at(-1)
  const initialization = log.filter((entry) => entry.method === 'runtime.initialize').at(-1)
  expect(submission?.params).toMatchObject({
    workspace_root: '/tmp/carina-smoke',
    profile: 'safe-edit',
    prompt: 'Inspect the workspace',
    agent: 'default',
  })
  expect(String(submission?.params.client_submission_id)).toMatch(/^web:/)
  expect(hello?.params.scopes).toEqual(['read', 'stream', 'write'])
  expect(initialization?.params.protocol_version).toBe('1.3.0')
  expect(log.map((entry) => entry.method)).not.toEqual(expect.arrayContaining(['session.create', 'artifact.upload', 'execution.start']))
})

test('a rejected run preserves the draft and returns attachments to a retryable state', async ({ page }) => {
  await openHarness(page)
  await reconnectAsOperator(page)

  await page.locator('input[type="file"]').setInputFiles({
    name: 'context.png',
    mimeType: 'image/png',
    buffer: Buffer.from([0x89, 0x50, 0x4e, 0x47]),
  })
  const prompt = page.getByRole('textbox', { name: 'Prompt', exact: true })
  await prompt.fill('Reject this run')
  const submit = page.getByRole('button', { name: 'Run prompt', exact: true })
  await submit.click()

  await expect(page.getByText('Run was not accepted: submission rejected')).toBeVisible()
  await expect(prompt).toHaveValue('Reject this run')
  await expect(page.locator('.react-attachment small')).toContainText('ready')
  await expect(submit).toBeEnabled()
})

test('Settings retains field focus and restores its trigger after Done or Escape', async ({ page }) => {
  await openHarness(page)
  const trigger = page.getByRole('button', { name: 'Observer mode', exact: true })
  await trigger.click()

  const dialog = page.getByRole('dialog', { name: 'Settings' })
  const token = dialog.getByLabel('Observer token')
  await expect(dialog.getByRole('button', { name: 'Close settings', exact: true })).toBeFocused()
  const gatewaySection = dialog.getByRole('button', { name: 'Gateway', exact: true })
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  await expect(gatewaySection).toBeFocused()
  await expect(gatewaySection).toHaveCSS('outline-style', 'solid')
  await token.fill('smoke-token')
  await expect(token).toBeFocused()
  await dialog.getByRole('button', { name: 'Done', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(trigger).toBeFocused()

  await trigger.click()
  await expect(dialog).toBeVisible()
  await expect(dialog.getByLabel('Observer token')).toHaveValue('smoke-token')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(trigger).toBeFocused()
})

test('Interface settings apply immediately and persist non-sensitive preferences', async ({ page }) => {
  await openHarness(page)
  const trigger = page.getByRole('button', { name: 'Workspace settings', exact: true })
  await trigger.click()

  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await expect(dialog.getByRole('heading', { name: 'Interface', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Dark', exact: true }).click()
  await dialog.getByRole('switch', { name: 'Open files panel on launch', exact: true }).uncheck()
  await dialog.getByRole('switch', { name: 'Open terminal panel on launch', exact: true }).uncheck()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'carina-dark')
  await expect(page.locator('.react-files-panel')).toHaveClass(/is-collapsed/)
  await expect(page.locator('.react-terminal-panel')).toHaveClass(/is-collapsed/)
  await dialog.getByRole('button', { name: 'Done', exact: true }).click()

  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('.connection-line').getByText('Observer · connected', { exact: true })).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'carina-dark')
  await page.getByRole('button', { name: 'Workspace settings', exact: true }).click()
  await expect(dialog.getByRole('button', { name: 'Dark', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.getByRole('switch', { name: 'Open files panel on launch', exact: true })).not.toBeChecked()
  await expect(dialog.getByRole('switch', { name: 'Open terminal panel on launch', exact: true })).not.toBeChecked()
})

test('Files and Terminal preserve mounted state and transfer focus across collapse', async ({ page }) => {
  await openHarness(page)
  await selectFixtureSession(page)

  const files = page.locator('.react-files-panel')
  const collapseFiles = files.getByRole('button', { name: 'Collapse files panel', exact: true })
  await collapseFiles.click()
  await expect(files).toHaveClass(/is-collapsed/)
  await expect(files.locator('.react-files-expanded')).toHaveAttribute('aria-hidden', 'true')
  const openFiles = files.getByRole('button', { name: 'Open files panel', exact: true })
  await expect(openFiles).toBeFocused()
  await openFiles.click()
  await expect(files).not.toHaveClass(/is-collapsed/)
  await expect(collapseFiles).toBeFocused()

  const terminal = page.locator('.react-terminal-panel')
  const collapseTerminal = terminal.getByRole('button', { name: 'Collapse terminal', exact: true })
  await collapseTerminal.click()
  await expect(terminal).toHaveClass(/is-collapsed/)
  await expect(terminal.locator('.react-terminal-expanded')).toHaveAttribute('aria-hidden', 'true')
  const openTerminal = terminal.getByRole('button', { name: 'Open terminal', exact: true })
  await expect(openTerminal).toBeFocused()
  await openTerminal.click()
  await expect(terminal).not.toHaveClass(/is-collapsed/)
  await expect(collapseTerminal).toBeFocused()
})
