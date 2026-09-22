import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server, browser: Browser, baseURL: string
const root = join(process.cwd(), '.tmp/settings-layout-test')
beforeAll(async () => {
  const built = await Bun.build({ entrypoints: ['web/components/shared/settings-layout.test-fixture.ts'], outdir: root, target: 'browser' })
  if (!built.success) throw new Error('settings fixture build failed')
  server = createServer(async (request, response) => {
    if (request.url === '/') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><lv-settings-fixture></lv-settings-fixture><script type="module" src="/fixture.js"></script>')
    } else if (request.url === '/fixture.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(join(root, 'settings-layout.test-fixture.js')))
    } else {
      response.writeHead(404).end()
    }
  })
  await new Promise<void>(resolve => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fixture server unavailable')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})
afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
})

test('shared settings retain native labels, keyboard submission and disabled controls', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.getByText('Display name', { exact: true }).click()
    const input = page.getByRole('textbox', { name: 'Display name' })
    expect(await input.evaluate(element => element.getRootNode() instanceof ShadowRoot && (element.getRootNode() as ShadowRoot).activeElement === element)).toBe(true)
    await input.fill('Ada Example')
    await input.press('Enter')
    await page.getByLabel('Submitted name').filter({ hasText: 'Ada Example' }).waitFor()
    expect(await input.inputValue()).toBe('Ada Example')
    expect(await page.getByRole('button', { name: 'Unavailable' }).isDisabled()).toBe(true)
    await page.getByRole('button', { name: 'Unavailable' }).evaluate((element: HTMLButtonElement) => element.click())
    expect(await page.getByLabel('Action count').textContent()).toBe('0')
    await input.focus()
    await page.keyboard.press('Tab')
    expect(await page.getByRole('button', { name: 'Save' }).evaluate(element => (element.getRootNode() as ShadowRoot).activeElement === element)).toBe(true)
  } finally { await page.close() }
})

test('all row variants fit narrow containers independently of the viewport', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.getByRole('textbox', { name: 'Display name' }).waitFor()
    for (const width of [720, 320]) {
      await page.locator('lv-settings-fixture').evaluate((element: HTMLElement, width) => { element.style.width = `${width}px` }, width)
      const geometry = await page.locator('lv-settings-fixture').evaluate(element => {
        const root = element.shadowRoot!
        const input = root.querySelector<HTMLInputElement>('#name')!.getBoundingClientRect()
        const label = root.querySelector('label[for="name"]')!.getBoundingClientRect()
        const rows = Array.from(root.querySelectorAll<HTMLElement>('.settings-row'))
        return { actionLabelWidth: root.querySelector('.settings-row[data-layout="action"] .settings-field')!.getBoundingClientRect().width, inputTop: input.top, labelBottom: label.bottom, overflow: rows.some(row => row.scrollWidth > row.clientWidth + 1), hostOverflow: element.scrollWidth > element.clientWidth + 1 }
      })
      expect(geometry.overflow).toBe(false)
      expect(geometry.hostOverflow).toBe(false)
      if (width === 720) expect(geometry.actionLabelWidth).toBeGreaterThanOrEqual(120)
      if (width === 320) expect(geometry.inputTop).toBeGreaterThanOrEqual(geometry.labelBottom)
    }
  } finally { await page.close() }
})
