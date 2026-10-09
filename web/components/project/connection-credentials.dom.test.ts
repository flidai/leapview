import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
let commandScript = ''
let bridgeStatus = 200
let bridgeRequests: { body: any; headers: Record<string, string | string[] | undefined> }[] = []
const root = join(process.cwd(), '.tmp/project-page-test')
beforeAll(async () => {
  const built = await Bun.build({ entrypoints: ['web/components/shared/command.ts'], target: 'browser' })
  if (!built.success) throw new Error('command fixture bundle failed')
  commandScript = await built.outputs[0].text()
  server = createServer(async (request, response) => {
    const path = new URL(request.url ?? '/', 'http://localhost').pathname
    if (path === '/credential-command.js') { response.setHeader('content-type', 'text/javascript'); response.end(commandScript); return }
    if (path === '/connections/administration/credentials') {
      let body = ''; for await (const chunk of request) body += chunk
      bridgeRequests.push({ body: JSON.parse(body), headers: request.headers })
      response.writeHead(bridgeStatus, { 'content-type': 'text/event-stream' })
      response.end(bridgeStatus === 200 ? 'event: datastar-patch-signals\ndata: signals {}\n\n' : '')
      return
    }
    if (path === '/') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><script type="module" src="/project-page-under-test.js"></script><lv-connection-administration></lv-connection-administration>')
      return
    }
    const fileRoot = path.startsWith('/static/vendor/') ? process.cwd() : root
    const file = normalize(join(fileRoot, path))
    if (!file.startsWith(fileRoot + '/')) { response.writeHead(404).end(); return }
    try { response.setHeader('content-type', 'text/javascript'); response.end(await readFile(file)) }
    catch { response.writeHead(404).end() }
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})
afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
})

async function open(page: Page) {
  await page.goto(baseURL)
  await page.waitForFunction(() => customElements.get('lv-connection-administration'))
  await page.locator('lv-connection-administration').evaluate(async (element: any) => {
    element.lifecycles = [{ assetId: 'warehouse', logicalConnection: 'warehouse', bindingId: 'binding',
      exists: true, enabled: true, connectorKind: 'postgres', authenticationMode: 'external_bundle',
      canManage: true, credentialsAvailable: true, sourceIdentity: 'warehouse-user', revision: 4, actions: [], tone: '', statusLabel: 'Enabled' }]
    element.administration = { command: {}, status: { loading: false, error: '', message: '' }, credentials: {
      command: { logicalConnection: 'warehouse', assetId: 'warehouse' }, drafts: [{ versionId: 'version-one', createdAt: '2026-10-08' }], nextBeforeVersionId: 'older',
      receiptId: '', receiptExpiresAt: '', bindingRevision: 4, operationId: '', phase: '', runtimeReady: false,
      status: { loading: false, error: '', message: '' },
    } }
    ;(window as any).commands = []
    for (const type of ['lv-connection-credential-query', 'lv-connection-credential-command']) {
      element.addEventListener(type, (event: CustomEvent) => (window as any).commands.push({ type, ...event.detail }))
    }
    await element.updateComplete
  })
}
async function signal(page: Page, values: Record<string, unknown>) {
  await page.locator('lv-connection-administration').evaluate(async (element: any, value) => {
    element.administration = { ...element.administration, credentials: { ...element.administration.credentials, ...value } }
    await element.updateComplete
  }, values)
}

test('saved credential controls require configured, enabled managed Postgres authority', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    expect(await page.getByRole('button', { name: 'Credentials', exact: true }).count()).toBe(1)
    for (const denied of [{ credentialsAvailable: false }, { canManage: false }, { enabled: false }, { exists: false }, { connectorKind: 's3' }, { authenticationMode: 'none' }]) {
      await page.locator('lv-connection-administration').evaluate(async (element: any, change) => {
        element.lifecycles = [{ ...element.lifecycles[0], credentialsAvailable: true, canManage: true, enabled: true, exists: true, connectorKind: 'postgres', authenticationMode: 'external_bundle', ...change }]
        await element.updateComplete
      }, denied)
      expect(await page.getByRole('button', { name: 'Credentials', exact: true }).count()).toBe(0)
    }
  } finally { await page.close() }
})

test('credential save is explicit, clears secret fields immediately and keeps read queries secret-free', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    expect(await page.getByLabel('Username', { exact: true }).count()).toBe(0)
    expect(await page.getByText('Uses the existing connection user: warehouse-user.', { exact: true }).count()).toBe(1)
    await page.getByLabel('Password', { exact: true }).fill('private-password')
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    expect(await page.getByLabel('Password', { exact: true }).inputValue()).toBe('')
    expect(await page.getByLabel('Username', { exact: true }).count()).toBe(0)
    const commands = await page.evaluate(() => (window as any).commands)
    expect(commands.filter((c: any) => c.action === 'save')).toHaveLength(1)
    expect(commands.find((c: any) => c.action === 'save').password).toBe('private-password')
    expect(commands.find((c: any) => c.action === 'save').username).toBe('')
    expect(commands.filter((c: any) => c.type.endsWith('-query')).every((c: any) => !c.password && !c.username)).toBe(true)
    expect(await page.locator('lv-connection-administration').textContent()).not.toContain('private-password')
  } finally { await page.close() }
})

test('activation keeps one operation identity after lost acknowledgement and requires server readiness', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    await page.getByLabel('Saved draft').selectOption('version-one')
    await signal(page, { receiptId: 'receipt-one', receiptExpiresAt: '2099-01-01T00:00:00Z', command: { versionId: 'version-one', logicalConnection: 'warehouse', assetId: 'warehouse' } })
    await page.getByRole('button', { name: 'Prepare activation', exact: true }).click()
    const prepared = await page.evaluate(() => (window as any).commands.find((c: any) => c.action === 'prepare'))
    expect(prepared.operationId).toMatch(/^[0-9a-f-]{36}$/)
    await page.locator('lv-connection-administration').evaluate(element => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 0 } } }))
    })
    await page.getByRole('button', { name: 'Check activation status', exact: true }).click()
    await signal(page, { operationId: prepared.operationId, phase: 'prepared', runtimeReady: false, receiptId: '', receiptExpiresAt: '' })
    expect(await page.getByRole('button', { name: 'Continue activation', exact: true }).isDisabled()).toBe(true)
    expect(await page.getByRole('button', { name: 'Test draft', exact: true }).isEnabled()).toBe(true)
    await signal(page, { receiptId: 'receipt-two', receiptExpiresAt: '2099-01-01T00:00:00Z' })
    await page.getByRole('button', { name: 'Continue activation', exact: true }).click()
    const commands = await page.evaluate(() => (window as any).commands)
    expect(commands.find((c: any) => c.action === 'status').operationId).toBe(prepared.operationId)
    expect(commands.find((c: any) => c.action === 'retry').operationId).toBe(prepared.operationId)
    await signal(page, { phase: 'committed', runtimeReady: false })
    expect(await page.getByText('Credentials are active and runtime is ready.', { exact: true }).count()).toBe(0)
    expect(await page.getByRole('button', { name: 'Cancel activation', exact: true }).count()).toBe(0)
    await signal(page, { phase: 'completed', runtimeReady: false })
    expect(await page.getByRole('button', { name: 'Prepare activation', exact: true }).count()).toBe(0)
    await page.getByRole('button', { name: 'Recover activation', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).operationId)).toBe(prepared.operationId)
    await signal(page, { phase: 'completed', runtimeReady: true })
    expect(await page.getByText('Credentials are active and runtime is ready.', { exact: true }).count()).toBe(1)
    await page.getByRole('button', { name: 'Prepare activation', exact: true }).click()
    expect(await page.getByText('Credentials are active and runtime is ready.', { exact: true }).count()).toBe(0)
    const latest = await page.evaluate(() => (window as any).commands.at(-1))
    expect(latest.operationId).not.toBe(prepared.operationId)
  } finally { await page.close() }
})


test('pagination and explicit recovery preserve identifiers without exposing stale connection state', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    await page.getByRole('button', { name: 'Older drafts', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).beforeVersionId)).toBe('older')
    const id = 'c7f5d1e8-e7a3-4fcb-abcf-111111111111'
    await page.getByLabel('Operation ID', { exact: true }).fill(id)
    await page.getByRole('button', { name: 'Recover operation', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).operationId)).toBe(id)
    await signal(page, { operationId: id, phase: 'committed', runtimeReady: false, status: { loading: false, error: 'PRIVATE_BACKEND_DETAIL', message: '' } })
    expect(await page.getByRole('alert').textContent()).not.toContain('PRIVATE_BACKEND_DETAIL')
    await signal(page, { command: { logicalConnection: 'another', assetId: 'another' }, phase: 'completed', runtimeReady: true })
    expect(await page.getByText('Credentials are active and runtime is ready.', { exact: true }).count()).toBe(0)
    await page.locator('lv-connection-administration').evaluate(async (element: any) => {
      element.lifecycles = [{ ...element.lifecycles[0], logicalConnection: 'another', assetId: 'another' }]
      await element.updateComplete
    })
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    expect(await page.getByLabel('Current operation ID', { exact: true }).count()).toBe(0)
  } finally { await page.close() }
})


for (const status of [200, 503]) {
  test(`real Datastar credential bridge serializes once and clears secrets after HTTP ${status}`, async () => {
    const page = await browser.newPage()
    try {
      bridgeStatus = status; bridgeRequests = []
      await open(page)
      await page.evaluate(async () => {
        const runtime = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
        await import('/credential-command.js' as string)
        ;(window as any).credentialRuntime = runtime
        runtime.mergePatch({ connectionAdmin: { credentials: { command: {} } } })
        const host = document.querySelector('lv-connection-administration')!
        const initialize = "$connectionAdmin.credentials = {command: evt.detail, drafts: [], nextBeforeVersionId: '', receiptId: '', receiptExpiresAt: '', bindingRevision: 0, operationId: evt.detail.operationId, phase: '', runtimeReady: false, status: {loading: true, error: '', message: ''}}; "
        const expression = initialize + "(async () => { try { switch ($connectionAdmin.credentials.command.action) {case 'save': await @post('/connections/administration/credentials', {retry: 'never', retryMaxCount: 0, openWhenHidden: true, filterSignals: {include: /^(?:connectionAdmin[.]credentials)(?:[.]|$)/}, headers: window.LeapViewCommand.nonReplayableHeaders('saveCredentialDraft')}); break; } } finally { $connectionAdmin.credentials.command.username = ''; $connectionAdmin.credentials.command.password = ''; evt.detail.username = ''; evt.detail.password = ''; } })()"
        host.setAttribute('data-on:lv-connection-credential-command', expression)
        ;(window as any).bridgeEvents = []
        document.addEventListener('datastar-fetch', (event: Event) => (window as any).bridgeEvents.push((event as CustomEvent).detail.type))
        host.addEventListener('lv-connection-credential-command', (event: Event) => { (window as any).bridgeDetail = (event as CustomEvent).detail })
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
      })
      await page.getByRole('button', { name: 'Credentials', exact: true }).click()
      await page.getByLabel('Password', { exact: true }).fill('transient-secret')
      await page.getByRole('button', { name: 'Save draft', exact: true }).click()
      await page.waitForFunction(() => (window as any).bridgeEvents.includes('finished'))
      const result = await page.evaluate(() => ({
        password: (window as any).credentialRuntime.getPath('connectionAdmin.credentials.command.password'),
        username: (window as any).credentialRuntime.getPath('connectionAdmin.credentials.command.username'),
        detail: (window as any).bridgeDetail, events: (window as any).bridgeEvents,
      }))
      expect(bridgeRequests).toHaveLength(1)
      expect(bridgeRequests[0].body.connectionAdmin.credentials.command.password).toBe('transient-secret')
      expect(bridgeRequests[0].headers['idempotency-key']).toBeUndefined()
      expect(bridgeRequests[0].headers['x-leapview-operation-id']).toBe('saveCredentialDraft')
      expect(result.password).toBe('')
      expect(result.username).toBe('')
      expect(result.detail.password).toBe('')
      expect(result.events).not.toContain('retrying')
      expect(await page.getByLabel('Password', { exact: true }).inputValue()).toBe('')
    } finally { await page.close() }
  })
}

test('saving after a ready activation discards its operation and receipt identities', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await signal(page, { operationId: 'c7f5d1e8-e7a3-4fcb-abcf-111111111111', phase: 'completed', runtimeReady: true,
      receiptId: 'old-receipt', command: { logicalConnection: 'warehouse', assetId: 'warehouse', versionId: 'version-one' } })
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    await page.getByLabel('Password', { exact: true }).fill('next-password')
    await page.getByRole('button', { name: 'Save draft', exact: true }).click()
    const saved = await page.evaluate(() => (window as any).commands.at(-1))
    expect(saved.action).toBe('save')
    expect(saved.operationId).toBe('')
    expect(saved.receiptId).toBe('')
    expect(saved.versionId).toBe('')
    expect(await page.getByText('Credentials are active and runtime is ready.', { exact: true }).count()).toBe(0)
  } finally { await page.close() }
})


test('only exact server-confirmed absence permits resetting an interrupted preparation', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    await page.getByLabel('Saved draft').selectOption('version-one')
    await signal(page, { receiptId: 'receipt', receiptExpiresAt: '2099-01-01T00:00:00Z', command: { logicalConnection: 'warehouse', assetId: 'warehouse', versionId: 'version-one' } })
    await page.getByRole('button', { name: 'Prepare activation', exact: true }).click()
    const id = await page.evaluate(() => (window as any).commands.at(-1).operationId)
    await page.locator('lv-connection-administration').evaluate(element => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 0 } } }))
    })
    expect(await page.getByRole('button', { name: 'Start again', exact: true }).count()).toBe(0)
    expect(await page.getByLabel('Saved draft').isDisabled()).toBe(true)
    await signal(page, { operationId: 'another-operation', phase: 'not_found', runtimeReady: false })
    expect(await page.getByRole('button', { name: 'Start again', exact: true }).count()).toBe(0)
    await signal(page, { operationId: id, phase: 'not_found', runtimeReady: false, receiptId: '', receiptExpiresAt: '' })
    await page.getByRole('button', { name: 'Start again', exact: true }).click()
    expect(await page.getByLabel('Saved draft').isEnabled()).toBe(true)
    await page.getByRole('button', { name: 'Test draft', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).operationId)).toBe('')
    await signal(page, { operationId: '', phase: '', receiptId: 'fresh-receipt', receiptExpiresAt: '2099-01-01T00:00:00Z' })
    await page.getByRole('button', { name: 'Prepare activation', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).operationId)).not.toBe(id)
  } finally { await page.close() }
})

test('retirement requires inspecting exact selected version and empty durable dependencies', async () => {
  const page = await browser.newPage()
  try {
    await open(page)
    await page.getByRole('button', { name: 'Credentials', exact: true }).click()
    await page.getByLabel('Saved draft').selectOption('version-one')
    expect(await page.getByRole('button', { name: 'Retire version locally', exact: true }).count()).toBe(0)
    await page.getByRole('button', { name: 'Inspect version dependencies', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).action)).toBe('version_status')
    await signal(page, { versionStatus: { versionId: 'version-one', state: 'available', retiredAt: '', dependencies: [{ kind: 'release', id: 'release-previous' }], moreDependencies: false } })
    expect(await page.getByText('release · release-previous', { exact: true }).count()).toBe(1)
    expect(await page.getByRole('button', { name: 'Retire version locally', exact: true }).count()).toBe(0)
    await signal(page, { versionStatus: { versionId: 'version-other', state: 'available', retiredAt: '', dependencies: [], moreDependencies: false } })
    expect(await page.getByRole('button', { name: 'Retire version locally', exact: true }).count()).toBe(0)
    await signal(page, { versionStatus: { versionId: 'version-one', state: 'available', retiredAt: '', dependencies: [], moreDependencies: false } })
    await page.getByRole('button', { name: 'Retire version locally', exact: true }).click()
    expect(await page.evaluate(() => (window as any).commands.at(-1).action)).toBe('retire')
    await signal(page, { versionStatus: { versionId: 'version-one', state: 'retired_local', retiredAt: '2026-10-09T09:00:00Z', dependencies: [], moreDependencies: false } })
    expect(await page.getByRole('button', { name: 'Retire version locally', exact: true }).count()).toBe(0)
    expect(await page.getByRole('button', { name: 'Test draft', exact: true }).isDisabled()).toBe(true)
  } finally { await page.close() }
})
