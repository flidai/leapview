import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const root = join(process.cwd(), '.tmp/datastar-inspector-test')

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
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('inspector shows live signal state without backend history', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 650 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('datastar-inspector'))
    const state = await page.locator('datastar-inspector').evaluate(async (element: any) => {
      const toggleStyle = getComputedStyle(element.shadowRoot.querySelector('.toggle'))
      const launcher = {
        bottom: toggleStyle.bottom,
        width: toggleStyle.width,
        height: toggleStyle.height,
        opacity: toggleStyle.opacity,
      }
      element.shadowRoot.querySelector<HTMLButtonElement>('.toggle')!.click()
      await element.updateComplete
      const branch = element.shadowRoot.querySelector<HTMLButtonElement>('[data-signal-branch="/status"]')!
      branch.click()
      await element.updateComplete
      const initial = element.shadowRoot.querySelector('[data-signal-path="/status/progressPercent"]')?.textContent
      const signals = element.querySelector<HTMLElement>('[data-json-signals]')!
      signals.textContent = '{"status":{"loading":false,"progressPercent":75}}'
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
      await element.updateComplete
      return {
        initial,
        updated: element.shadowRoot.querySelector('[data-signal-path="/status/progressPercent"]')?.textContent,
        historyPanels: element.shadowRoot.querySelectorAll('.signal-history-pane').length,
        historyChanges: element.shadowRoot.querySelectorAll('[data-signal-change]').length,
        launcher,
      }
    })

    expect(state.initial).toMatch(/50/)
    expect(state.updated).toMatch(/75/)
    expect(state.historyPanels).toBe(0)
    expect(state.historyChanges).toBe(0)
    expect(state.launcher).toEqual({ bottom: '16px', width: '38px', height: '38px', opacity: '1' })
  } finally {
    await page.close()
  }
})

test('inspector launcher and panel can be dragged and keep their positions', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 650 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('datastar-inspector'))
    const initialToggle = await page.locator('datastar-inspector').evaluate(async (element: any) => {
      await element.updateComplete
      const toggle = element.shadowRoot.querySelector('.toggle') as HTMLElement
      const rect = toggle.getBoundingClientRect()
      return { x: rect.x, y: rect.y, width: rect.width, height: rect.height }
    })

    await page.mouse.move(initialToggle.x + initialToggle.width / 2, initialToggle.y + initialToggle.height / 2)
    await page.mouse.down()
    await page.mouse.move(initialToggle.x - 180, initialToggle.y - 120, { steps: 5 })
    await page.mouse.up()

    const movedToggle = await page.locator('datastar-inspector').evaluate(async (element: any) => {
      await element.updateComplete
      const toggle = element.shadowRoot.querySelector('.toggle') as HTMLElement
      const rect = toggle.getBoundingClientRect()
      return {
        expanded: Boolean(element.shadowRoot.querySelector('.panel')),
        x: rect.x,
        y: rect.y,
        stylePosition: { x: Number.parseFloat(toggle.style.left), y: Number.parseFloat(toggle.style.top) },
        stored: JSON.parse(sessionStorage.getItem('ds-inspector') ?? '{}').togglePosition,
      }
    })
    expect(movedToggle.expanded).toBe(false)
    expect(movedToggle.x).toBeLessThan(initialToggle.x - 100)
    expect(movedToggle.y).toBeLessThan(initialToggle.y - 70)
    expect(movedToggle.stored).toEqual(movedToggle.stylePosition)

    await page.locator('datastar-inspector').evaluate(async (element: any) => {
      element.shadowRoot.querySelector<HTMLButtonElement>('.toggle')!.click()
      await element.updateComplete
    })
    const initialPanel = await page.locator('datastar-inspector').evaluate((element: any) => {
      const panel = element.shadowRoot.querySelector('.panel') as HTMLElement
      const handle = element.shadowRoot.querySelector('.drag-handle') as HTMLElement
      const panelRect = panel.getBoundingClientRect()
      const handleRect = handle.getBoundingClientRect()
      return {
        x: panelRect.x,
        y: panelRect.y,
        handleX: handleRect.x + handleRect.width / 2,
        handleY: handleRect.y + handleRect.height / 2,
      }
    })

    await page.mouse.move(initialPanel.handleX, initialPanel.handleY)
    await page.mouse.down()
    await page.mouse.move(initialPanel.handleX - 80, initialPanel.handleY + 45, { steps: 5 })
    await page.mouse.up()

    const movedPanel = await page.locator('datastar-inspector').evaluate(async (element: any) => {
      await element.updateComplete
      const rect = element.shadowRoot.querySelector('.panel').getBoundingClientRect()
      return {
        x: rect.x,
        y: rect.y,
        stored: JSON.parse(sessionStorage.getItem('ds-inspector') ?? '{}').panelPosition,
      }
    })
    expect(movedPanel.x).toBeLessThan(initialPanel.x - 50)
    expect(movedPanel.y).toBeGreaterThan(initialPanel.y + 5)
    expect(movedPanel.stored).toEqual({ x: movedPanel.x, y: movedPanel.y })

    await page.reload()
    await page.waitForFunction(() => customElements.get('datastar-inspector'))
    const restoredPanel = await page.locator('datastar-inspector').evaluate(async (element: any) => {
      await element.updateComplete
      const rect = element.shadowRoot.querySelector('.panel').getBoundingClientRect()
      return { x: rect.x, y: rect.y }
    })
    expect(restoredPanel.x).toBe(movedPanel.x)
    expect(restoredPanel.y).toBe(movedPanel.y)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <body>
        <datastar-inspector>
          <pre data-json-signals>{"status":{"loading":false,"progressPercent":50}}</pre>
        </datastar-inspector>
        <script type="module" src="/datastar-inspector-under-test.js"></script>
      </body>
    </html>
  `
}
