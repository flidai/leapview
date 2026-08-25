import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/filter-menu-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
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

test('filter menu renders backend-owned options and emits search/toggle/clear commands', async () => {
  const page = await browser.newPage({ viewport: { width: 640, height: 520 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-menu'))
    const state = await page.locator('lv-filter-menu').evaluate(async (element: any) => {
      element.menu = {
        id: 'principal',
        label: 'User',
        summaryLabel: 'User',
        mode: 'multi',
        search: '',
        selected: ['dev'],
        loading: false,
        error: '',
        placeholder: 'Search users',
        emptyLabel: 'No users found.',
        options: [
          { value: 'dev', label: 'Me (dev@example.com)', description: 'Current user', icon: 'user', countLabel: '3', selected: true, disabled: false },
          { value: 'agent', label: 'agent', description: '', icon: 'user', countLabel: '2', selected: false, disabled: false },
        ],
      }
      const commands: unknown[] = []
      element.addEventListener('lv-filter-menu-command', (event: CustomEvent) => commands.push(event.detail))
      await element.updateComplete
      const root = element.shadowRoot
      const trigger = root.querySelector<HTMLButtonElement>('.trigger')!
      const initialTriggerText = trigger.textContent ?? ''
      trigger.click()
      await element.updateComplete
      const openText = root.textContent ?? ''
      const search = root.querySelector<HTMLInputElement>('.search input')!
      const searchLabel = search.getAttribute('aria-label')
      search.value = 'age'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 250))
      const searchCommand = commands.at(-1)
      const secondOption = root.querySelectorAll<HTMLInputElement>('.option input')[1]!
      secondOption.click()
      await element.updateComplete
      const toggleCommand = commands.at(-1)
      const clear = root.querySelector<HTMLButtonElement>('.clear')!
      clear.click()
      await element.updateComplete
      const clearCommand = commands.at(-1)
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await element.updateComplete
      const closedAfterEscape = !root.querySelector('.menu')
      element.menu = { ...element.menu, selected: [], options: [], emptyLabel: 'No users found.' }
      await element.updateComplete
      trigger.click()
      await element.updateComplete
      const emptyText = root.textContent ?? ''
      element.menu = { ...element.menu, loading: true, options: [] }
      await element.updateComplete
      const loadingText = root.textContent ?? ''
      element.menu = { ...element.menu, loading: false, error: 'Filter options failed.' }
      await element.updateComplete
      const errorText = root.textContent ?? ''
      return { initialTriggerText, openText, searchLabel, searchCommand, toggleCommand, clearCommand, closedAfterEscape, emptyText, loadingText, errorText }
    })

    expect(state.initialTriggerText).toMatch(/User/)
    expect(state.openText).toMatch(/Me \(dev@example\.com\)/)
    expect(state.openText).toMatch(/agent/)
    expect(state.searchLabel).toBe('Search User')
    expect(state.searchCommand).toMatchObject({ menuId: 'principal', action: 'search', search: 'age', selected: ['dev'] })
    expect(state.toggleCommand).toMatchObject({ menuId: 'principal', action: 'toggle', value: 'agent', selected: ['dev'] })
    expect(state.clearCommand).toMatchObject({ menuId: 'principal', action: 'clear', selected: ['dev'] })
    expect(state.closedAfterEscape).toBe(true)
    expect(state.emptyText).toMatch(/No users found/)
    expect(state.loadingText).toMatch(/Loading/)
    expect(state.errorText).toMatch(/Filter options failed/)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          body { ${typographyTestTokens} --lv-bg-panel: #fff; --lv-bg-app: #fff; --lv-bg-input: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-danger: #d1242f; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-muted: 1px solid #d8dee4; --lv-border-accent: #0969da; --lv-radius-default: 6px; --lv-radius-small: 6px; --base-size-4: 4px; --base-size-6: 6px; --base-size-7: 7px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-24: 24px; --base-size-32: 32px; --control-medium-size: 32px; --control-small-size: 28px; }
        </style>
      </head>
      <body>
        <lv-filter-menu></lv-filter-menu>
        <script type="module" src="/filter-menu-under-test.js"></script>
      </body>
    </html>
  `
}
