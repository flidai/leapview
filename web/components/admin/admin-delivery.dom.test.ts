import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let browser: Browser
let baseURL = ''
const projectRoot = process.cwd()
const bundleRoot = join(projectRoot, '.tmp/admin-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(`<!doctype html><html><head><style>body { ${typographyTestTokens} }</style></head><body><lv-admin-page></lv-admin-page><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/admin-page-under-test.js"></script></body></html>`)
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : bundleRoot
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) { response.writeHead(404).end('not found'); return }
    try { response.setHeader('content-type', 'text/javascript'); response.end(await readFile(file)) } catch { response.writeHead(404).end('not found') }
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
}, 15_000)

test('delivery makes degraded evidence and rollback consequences explicit', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Delivery', active: 'delivery', headerTitle: 'Delivery',
        headerDetail: 'Inspect the server-bound serving state and recover retained generations.',
        empty: 'Delivery is degraded. Some target-owned evidence is unavailable; do not treat missing rows as proof that no delivery occurred.',
        sections: [
          { title: 'Operator snapshot', facts: [{ label: 'Status', value: 'Degraded' }, { label: 'Evidence', value: 'Partial evidence' }] },
          { title: 'Retained generations', table: {
            columns: [{ id: 'status', header: 'Status', kind: 'status' }, { id: 'generation', header: 'Serving state', kind: 'text' }, { id: 'actions', header: 'Actions', kind: 'actions' }],
            rows: [{ id: 'generation-row', status: 'Retired', generation: 'Retained rollback generation', generationId: 'generation-2', actions: [{ label: 'Rollback', action: 'rollback', icon: 'undo-2' }] }],
            empty: 'No retained generations are available.',
          } },
        ],
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      window.confirm = () => true
      let detail: unknown = null
      element.addEventListener('lv-delivery-rollback', (event: CustomEvent) => { detail = event.detail })
      const root = element.shadowRoot as ShadowRoot
      const action = root.querySelector('.record-icon-action[aria-label="Rollback"]') as HTMLButtonElement
      const beforeText = root.textContent?.replace(/\s+/g, ' ').trim() ?? ''
      action.click()
      await element.updateComplete
      const lockedWhilePending = (root.querySelector('.record-icon-action[aria-label="Rollback"]') as HTMLButtonElement).disabled
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      return { beforeText, detail, lockedWhilePending, afterText: root.textContent?.replace(/\s+/g, ' ').trim() ?? '', unlockedAfterCompletion: !(root.querySelector('.record-icon-action[aria-label="Rollback"]') as HTMLButtonElement).disabled }
    })
    expect(state.beforeText).toContain('Delivery is degraded')
    expect(state.beforeText).toContain('Partial evidence')
    expect(state.beforeText).not.toContain('generation-2')
    expect(state.detail).toEqual({ generation: 'generation-2' })
    expect(state.lockedWhilePending).toBe(true)
    expect(state.afterText).toContain('Rollback request completed')
    expect(state.unlockedAfterCompletion).toBe(true)
  } finally {
    await page.close()
  }
})
