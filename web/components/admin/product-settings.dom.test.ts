import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { datastarRuntimeURL } from '../shared/datastar-runtime'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/product-settings-test')
const bundle = join(root, 'product-settings-under-test.js')

beforeAll(async () => {
  await Bun.$`rm -rf ${root}`.quiet()
  const built = await Bun.build({
    entrypoints: ['web/components/admin/product-settings.ts'],
    target: 'browser',
    format: 'esm',
    external: [datastarRuntimeURL],
    outdir: root,
    naming: { entry: 'product-settings-under-test.js' },
  })
  if (!built.success) throw new Error('failed to build product settings test bundle')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('product settings renders redacted sections and emits typed identity commands', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-product-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ productSettings: {
        active: 'general', canManage: true,
        general: { displayName: 'Acme Analytics', revision: 7, updatedAt: '2026-08-11T00:00:00Z', instanceId: 'lvinst_test', canonicalOrigin: 'https://example.test', environment: 'production', logo: { url: '/logo.png', sha256: 'abc', mediaType: 'image/png', sizeBytes: 3, width: 16, height: 8 } },
        authentication: { browserEnabled: true, apiTokenOnly: false, local: { available: true, enabled: true }, oidc: { available: true, enabled: true, provider: 'corporate' }, azure: { available: true, enabled: false }, scim: { available: true, enabled: true }, managedBy: 'deployment', platformAdministrators: [{ bindingId: 'binding-1', principalId: 'principal-1', email: 'admin@example.test', displayName: 'Admin', role: 'admin', grantedAt: '2026-08-10T00:00:00Z' }, { bindingId: 'binding-2', principalId: 'principal-2', email: 'revoked@example.test', displayName: 'Revoked', role: 'admin', grantedAt: '2026-08-09T00:00:00Z', revokedAt: '2026-08-11T00:00:00Z' }], platformAdministrationRevision: 'revision-1', platformAdministrationAvailable: true },
        api: { bearerCredentials: { available: true, enabled: true }, servicePrincipals: { available: true, enabled: true }, oauth: { available: true, enabled: true }, mcp: { available: true, enabled: true }, externalMcpIssuer: false },
        system: { instanceId: 'lvinst_test', canonicalOrigin: 'https://example.test', environment: 'production', build: { version: '1.2.3', revision: 'abcdef', buildTime: 'now', dirty: false, development: false }, storageBackend: 'local', agent: { available: true, configured: true, provider: 'openai-compatible', modelConfigured: true }, limits: { queryResultMaxRows: 10, queryResultMaxBytes: 1024, managedDataMaxFiles: 2, managedDataMaxFileBytes: 3, managedDataMaxRevisionBytes: 4 }, runtime: { health: 'healthy', controlPlane: 'available', environment: 'production' } },
      } })
      const element = document.querySelector('lv-product-settings') as any
      await element.updateComplete
      let command: unknown = null
      let platformCommand: unknown = null
      element.addEventListener('lv-product-settings-command', (event: CustomEvent) => { command = event.detail })
      element.addEventListener('lv-platform-administrator-command', (event: CustomEvent) => { platformCommand = event.detail })
      const input = element.shadowRoot.querySelector('input[type="text"]') as HTMLInputElement
      const logoLabel = element.shadowRoot.querySelector('input[type="file"]')?.getAttribute('aria-label')
      input.value = 'Acme BI'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]).find((button) => button.textContent?.trim() === 'Save')?.click()
      await element.updateComplete
      const saveCommand = command
      ;(Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]).find((button) => button.textContent?.trim() === 'Reset to LeapView')?.click()
      const resetCommand = command
      const generalText = element.shadowRoot.textContent.replace(/\s+/g, ' ').trim()
      const fieldLabelFontSize = getComputedStyle(element.shadowRoot.querySelector('.settings-label')!).fontSize
      mergePatch({ productSettings: { active: 'authentication' } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      ;(element.shadowRoot.querySelector('#platform-administrator-principal') as HTMLInputElement).value = 'principal-3'
      ;(element.shadowRoot.querySelector('#platform-administrator-principal') as HTMLInputElement).dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      ;(Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]).find((button) => button.textContent?.trim() === 'Grant platform administrator')?.click()
      return {
        generalText,
        inputValue: input.value,
        inputLabel: input.getAttribute('aria-label'),
        logoLabel,
        authText: element.shadowRoot.textContent.replace(/\s+/g, ' ').trim(),
        saveCommand,
        resetCommand,
        platformCommand,
        fieldLabelFontSize,
      }
    })
    expect(state.generalText).toContain('Instance identity')
    expect(state.generalText).toContain('Powered by LeapView')
    expect(state.inputValue).toBe('Acme BI')
    expect(state.inputLabel).toBe('Instance name')
    expect(state.logoLabel).toBe('Change logo')
    expect(state.generalText).toContain('Instance ID')
    expect(state.authText).toContain('Managed by deployment')
    expect(state.authText).toContain('API and protocol availability')
    expect(state.authText).toContain('Platform authority')
    expect(state.saveCommand).toEqual({ action: 'save_display_name', displayName: 'Acme BI', revision: 7 })
    expect(state.resetCommand).toEqual({ action: 'reset_identity', revision: 7 })
    expect(state.platformCommand).toEqual({ action: 'grant_platform_administrator', principalId: 'principal-3', expectedRevision: 'revision-1', revision: 0 })
    expect(state.fieldLabelFontSize).toBe('14px')
  } finally {
    await page.close()
  }
})

test('logo upload preserves product ETag and CSRF token', async () => {
  const page = await browser.newPage()
  try {
    await page.route('**/admin/product-logo', async (route) => {
      await route.fulfill({ status: 200, headers: { ETag: '"product-8"', 'content-type': 'application/json' }, body: '{}' })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-product-settings'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ productSettings: {
        active: 'general', canManage: true,
        general: { displayName: 'Acme', revision: 7, updatedAt: '', instanceId: '', canonicalOrigin: '', environment: '', logo: { url: '/logo.png', sha256: 'abc', mediaType: 'image/png', sizeBytes: 3, width: 16, height: 8 } },
        authentication: { browserEnabled: false, apiTokenOnly: false, local: { available: false, enabled: false }, oidc: { available: false, enabled: false }, azure: { available: false, enabled: false }, scim: { available: false, enabled: false }, managedBy: 'deployment' },
        api: { bearerCredentials: { available: false, enabled: false }, servicePrincipals: { available: false, enabled: false }, oauth: { available: false, enabled: false }, mcp: { available: false, enabled: false }, externalMcpIssuer: false },
        system: { instanceId: '', canonicalOrigin: '', environment: '', build: { version: '', revision: '', buildTime: '', dirty: false, development: false }, storageBackend: '', agent: { available: false, configured: false, modelConfigured: false }, limits: { queryResultMaxRows: 0, queryResultMaxBytes: 0, managedDataMaxFiles: 0, managedDataMaxFileBytes: 0, managedDataMaxRevisionBytes: 0 }, runtime: { health: '', controlPlane: '', environment: '' } },
      } })
      await (document.querySelector('lv-product-settings') as any).updateComplete
    })
    const input = page.locator('lv-product-settings').locator('input[type="file"]')
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-product-settings') as any
      return element?.shadowRoot?.querySelector('input[type="file"]')?.disabled === false
    })
    await page.waitForFunction(() => typeof window.LeapViewCommand?.headers === 'function')
    const requestPromise = page.waitForRequest('**/admin/product-logo')
    await input.setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from('png') })
    const request = await requestPromise
    expect(request.method()).toBe('PUT')
    expect(request.headers()['if-match']).toBe('"product-7"')
    expect(request.headers()['x-csrf-token']).toBe('test-csrf')
  } finally {
    await page.close()
  }
})

test('platform administrator mutation shows busy state and typed transport errors', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-product-settings'))
    const result = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ productSettings: {
        active: 'authentication', canManage: true,
        general: { displayName: '', revision: 1, updatedAt: '', instanceId: '', canonicalOrigin: '', environment: '' },
        authentication: {
          browserEnabled: true, apiTokenOnly: false,
          local: { available: false, enabled: false }, oidc: { available: false, enabled: false },
          azure: { available: false, enabled: false }, scim: { available: false, enabled: false }, managedBy: 'deployment',
          platformAdministrators: [], platformAdministrationRevision: 'revision-1', platformAdministrationAvailable: true,
        },
        api: { bearerCredentials: { available: false, enabled: false }, servicePrincipals: { available: false, enabled: false }, oauth: { available: false, enabled: false }, mcp: { available: false, enabled: false }, externalMcpIssuer: false },
        system: { instanceId: '', canonicalOrigin: '', environment: '', build: { version: '', revision: '', buildTime: '', dirty: false, development: false }, storageBackend: '', agent: { available: false, configured: false, modelConfigured: false }, limits: { queryResultMaxRows: 0, queryResultMaxBytes: 0, managedDataMaxFiles: 0, managedDataMaxFileBytes: 0, managedDataMaxRevisionBytes: 0 }, runtime: { health: '', controlPlane: '', environment: '' } },
      } })
      const element = document.querySelector('lv-product-settings') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      const input = element.shadowRoot.querySelector('#platform-administrator-principal') as HTMLInputElement
      input.value = 'principal-3'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const buttons = Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]
      const button = buttons.find((candidate) => candidate.textContent?.includes('Grant platform administrator')) as HTMLButtonElement
      button.click()
      await element.updateComplete
      const busyButton = buttons.find((candidate) => candidate.textContent?.includes('Granting')) as HTMLButtonElement
      const busy = busyButton?.disabled === true && element.shadowRoot.textContent.includes('Updating platform authority')
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: 409 }, el: element } }))
      await element.updateComplete
      return { busy, error: element.shadowRoot.querySelector('[role="alert"]')?.textContent ?? '' }
    })
    expect(result.busy).toBe(true)
    expect(result.error).toContain('Platform authority update conflicted')
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `<!doctype html><html><head><meta name="csrf-token" content="test-csrf"><style>body { ${typographyTestTokens} }</style></head><body><lv-product-settings></lv-product-settings><script type="module" src="/static/command.js"></script><script type="module" src="/product-settings-under-test.js"></script></body></html>`
}
