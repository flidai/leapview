import { afterAll, beforeAll, expect, test } from 'bun:test'
import { chromium, type Browser } from 'playwright'
import { createServer, type Server } from 'node:http'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const projectRoot = process.cwd()
const fixtureRoot = join(projectRoot, '.tmp/entity-multi-select-test')
let browser: Browser
let server: Server
let baseURL = ''

beforeAll(async () => {
  server = createServer((request, response) => {
    if (request.url === '/entity-multi-select-under-test.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(readFileSync(join(fixtureRoot, 'entity-multi-select-under-test.js')))
      return
    }
    response.setHeader('content-type', 'text/html')
    response.end('<!doctype html><lv-entity-multi-select></lv-entity-multi-select><script type="module" src="/entity-multi-select-under-test.js"></script>')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch({ headless: true })
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve) => server?.close(() => resolve()))
})

test('filters entities and emits ordered multi-selection changes', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-entity-multi-select'))
    const result = await page.evaluate(async () => {
      const picker = document.querySelector('lv-entity-multi-select') as any
      picker.label = 'Users'
      picker.searchPlaceholder = 'Search users...'
      picker.items = [
        { id: 'ana', label: 'Ana Analyst', detail: 'ana@example.com', kind: 'principal' },
        { id: 'group', label: 'Analytics', detail: 'Group', kind: 'group' },
        { id: 'sam', label: 'Sam Seller', detail: 'sam@example.com', kind: 'principal' },
      ]
      await picker.updateComplete
      const events: string[][] = []
      picker.addEventListener('lv-entity-selection-change', (event: CustomEvent) => events.push(event.detail.selectedIds))
      const search = picker.shadowRoot.querySelector('input[type="search"]') as HTMLInputElement
      search.value = 'ana'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await picker.updateComplete
      const visibleLabels = Array.from(picker.shadowRoot.querySelectorAll('.item-label')).map((node: Element) => node.textContent?.trim())
      const checkbox = picker.shadowRoot.querySelector('input[type="checkbox"]') as HTMLInputElement
      checkbox.click()
      await picker.updateComplete
      search.value = ''
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await picker.updateComplete
      const sam = picker.shadowRoot.querySelector('input[value="sam"]') as HTMLInputElement
      sam.click()
      await picker.updateComplete
      return {
        visibleLabels,
        events,
        summary: picker.shadowRoot.querySelector('.selection-count')?.textContent?.trim(),
        overflow: picker.scrollWidth > picker.clientWidth,
        multiselect: picker.shadowRoot.querySelector('[role="listbox"]')?.getAttribute('aria-multiselectable'),
      }
    })
    expect(result).toEqual({
      visibleLabels: ['Ana Analyst', 'Analytics'],
      events: [['ana'], ['ana', 'sam']],
      summary: '2 selected',
      overflow: false,
      multiselect: 'true',
    })
  } finally {
    await page.close()
  }
})
