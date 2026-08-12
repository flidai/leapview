import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/settings-surfaces-test')

beforeAll(async () => {
  await Bun.$`mkdir -p ${root}`
  const result = await Bun.build({ entrypoints: [join(projectRoot, 'web/components/admin/settings-surfaces.ts')], outdir: root, naming: 'settings-surfaces.js', target: 'browser', minify: false })
  if (!result.success) throw new Error('settings surface bundle failed')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><main><lv-workspace-registry></lv-workspace-registry><lv-service-accounts></lv-service-accounts><lv-audit-log></lv-audit-log><script type="module" src="/settings-surfaces.js"></script></main>')
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) { response.writeHead(404); response.end(); return }
    try { response.setHeader('content-type', 'text/javascript'); response.end(await readFile(file)) } catch { response.writeHead(404); response.end() }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
})

test('settings surfaces render typed signals and emit commands', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminServiceAccounts: { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } }, getPath: (path: string) => (path === 'adminServiceAccounts' ? { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } : undefined), effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-service-accounts') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-service-account-command', (event: CustomEvent) => { detail = event.detail })
      ;(element.shadowRoot.querySelector('tbody button') as HTMLButtonElement).click()
      return { text: element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim(), detail }
    })
    expect(result.text).toContain('CI')
    expect(result.detail).toEqual({ action: 'select', accountId: 'svc-1' })
  } finally { await page.close() }
})

test('workspace registry uses the shared searchable entity list', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-registry') && customElements.get('lv-entity-list'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const registry = {
        items: [
          {
            id: 'sales', title: 'Sales', description: 'Revenue reporting', href: '/workspaces/sales',
            owner: { subjectType: 'principal', subjectId: 'owner-1', displayName: 'Ada Lovelace', email: 'ada@example.com' },
            administrators: [{ subjectType: 'principal', subjectId: 'admin-1', displayName: 'Grace Hopper', role: 'admin' }],
            environment: 'production', deploymentStatus: 'Active', updatedAt: '2026-08-11T08:00:00Z', links: { self: '/api/v1/workspaces/sales', workspace: '/workspaces/sales' },
          },
          {
            id: 'retail', title: 'Retail', description: 'Store operations', href: '/workspaces/retail',
            administrators: [], environment: 'development', servingStateStatus: 'Not deployed', updatedAt: '2026-08-10T08:00:00Z',
            links: { self: '/api/v1/workspaces/retail', workspace: '/workspaces/retail' },
          },
        ],
        loading: false,
        hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({
        root: { adminWorkspaces: registry },
        getPath: (path: string) => path === 'adminWorkspaces' ? registry : undefined,
        effect: (fn: () => void) => { fn(); return () => {} },
      })
      const element = document.querySelector('lv-workspace-registry') as any
      element.requestUpdate()
      await element.updateComplete
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      const rows = () => Array.from(element.shadowRoot.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.textContent?.replace(/\s+/g, ' ').trim())
      const initialRows = rows()
      const input = element.shadowRoot.querySelector('.entity-search input') as HTMLInputElement
      input.value = 'retail'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await list.updateComplete
      return {
        hasSharedList: Boolean(list),
        ownTableCount: element.shadowRoot.querySelectorAll(':scope > section > .table-wrap').length,
        headings: Array.from(element.shadowRoot.querySelectorAll('h2')).map((heading) => heading.textContent?.trim()),
        headers: Array.from(element.shadowRoot.querySelectorAll('.entity-list-sort-button > span:first-child')).map((header) => header.textContent?.trim()),
        initialRows,
        filteredRows: rows(),
        firstHref: element.shadowRoot.querySelector('.entity-list-identity')?.getAttribute('href'),
      }
    })

    expect(result.hasSharedList).toBe(true)
    expect(result.ownTableCount).toBe(0)
    expect(result.headings).toEqual([])
    expect(result.headers).toEqual(['Name', 'Owner', 'Administrators', 'Environment', 'Deployment', 'Updated'])
    expect(result.initialRows).toHaveLength(2)
    expect(result.initialRows[0]).toContain('Sales Revenue reporting')
    expect(result.initialRows[0]).toContain('Ada Lovelace')
    expect(result.initialRows[0]).toContain('Grace Hopper')
    expect(result.initialRows[0]).toContain('production')
    expect(result.initialRows[0]).toContain('Active')
    expect(result.filteredRows).toHaveLength(1)
    expect(result.filteredRows[0]).toContain('Retail Store operations')
    expect(result.firstHref).toBe('/workspaces/retail')
  } finally { await page.close() }
})
