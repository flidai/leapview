import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

beforeAll(async () => {
  const root = join(process.cwd(), '.tmp')
  server = createServer(async (request, response) => {
    const url = request.url ?? '/'
    if (url === '/visual-modal-under-test.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(join(root, 'visual-modal-under-test.js'), 'utf8'))
      return
    }
    response.setHeader('content-type', 'text/html')
    response.end(`
      <!doctype html>
      <html>
        <body>
          <button id="trigger">Expand</button>
          <section id="parent">
            <lv-visualization-host id="first"></lv-visualization-host>
            <lv-visualization-host id="second"></lv-visualization-host>
          </section>
          <lv-visual-modal id="modal"></lv-visual-modal>
          <script type="module" src="/visual-modal-under-test.js"></script>
        </body>
      </html>
    `)
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

async function setupPage() {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.waitForFunction(() => customElements.get('lv-visual-modal'))
  return page
}

async function dispatchVisualAction(page: Awaited<ReturnType<typeof setupPage>>, sourceId: string, action: string): Promise<void> {
  await page.evaluate(({ sourceId, action }) => {
    const source = document.getElementById(sourceId)
    if (!source) throw new Error(`missing source ${sourceId}`)
    source.dispatchEvent(new CustomEvent('lv-visual-action', {
      bubbles: true,
      composed: true,
      detail: {
        action,
        visualType: source.id === 'second' ? 'table' : 'chart',
        visualId: sourceId,
        title: sourceId,
        columns: [{ key: 'label', label: 'Label' }],
        rows: [{ label: 'A' }],
        selection: [],
      },
    }))
  }, { sourceId, action })
  await page.locator('lv-visual-modal').evaluate((modal: any) => modal.updateComplete)
}

test('focus action moves the live visual into the modal and restores it in place', async () => {
  const page = await setupPage()
  try {
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'focus')

    const focusedState = await page.evaluate(() => {
      const modal = document.querySelector('lv-visual-modal')!
      const first = document.getElementById('first')!
      const parent = document.getElementById('parent')!
      return {
        sourceParent: first.parentElement?.localName,
        slot: first.getAttribute('slot'),
        sourcePosition: parent.children[0] === first,
        sourceInModal: modal.querySelector('[slot="focus-visual"]') === first,
        activeInModal: (modal.shadowRoot as ShadowRoot)?.activeElement?.classList.contains('focus-close') ?? false,
      }
    })

    expect(focusedState).toEqual({
      sourceParent: 'lv-visual-modal',
      slot: 'focus-visual',
      sourcePosition: false,
      sourceInModal: true,
      activeInModal: true,
    })

    await page.keyboard.press('Tab')
    expect(await page.locator('lv-visual-modal').evaluate((modal: any) => (
      (modal.shadowRoot as ShadowRoot).activeElement?.classList.contains('focus-close') ?? false
    ))).toBe(true)

    await page.locator('lv-visual-modal').evaluate((modal: any) => (modal.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.focus-close')!.click())
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.updateComplete)

    const restoredState = await page.evaluate(() => {
      const first = document.getElementById('first')!
      const parent = document.getElementById('parent')!
      return {
        sourceParent: first.parentElement?.id,
        slot: first.getAttribute('slot'),
        restoredPosition: parent.children[0] === first,
        focusSlotEmpty: !document.querySelector('lv-visual-modal')?.querySelector('[slot="focus-visual"]'),
        activeId: document.activeElement?.id,
      }
    })

    expect(restoredState).toEqual({
      sourceParent: 'parent',
      slot: null,
      restoredPosition: true,
      focusSlotEmpty: true,
      activeId: 'trigger',
    })
  } finally {
    await page.close()
  }
})

test('opening another focused source restores the previous element first', async () => {
  const page = await setupPage()
  try {
    await dispatchVisualAction(page, 'first', 'focus')
    await dispatchVisualAction(page, 'second', 'focus')

    const state = await page.evaluate(() => {
      const parent = document.getElementById('parent')!
      const first = document.getElementById('first')!
      const second = document.getElementById('second')!
      const modal = document.querySelector('lv-visual-modal')!
      return {
        firstParent: first.parentElement?.id,
        firstPosition: parent.children[0] === first,
        secondParent: second.parentElement?.localName,
        secondSlot: second.getAttribute('slot'),
        secondInModal: modal.querySelector('[slot="focus-visual"]') === second,
      }
    })

    expect(state).toEqual({
      firstParent: 'parent',
      firstPosition: true,
      secondParent: 'lv-visual-modal',
      secondSlot: 'focus-visual',
      secondInModal: true,
    })
  } finally {
    await page.close()
  }
})

test('non-focus visual actions do not move the source element', async () => {
  const page = await setupPage()
  try {
    await dispatchVisualAction(page, 'first', 'show-data')

    const state = await page.evaluate(() => {
      const first = document.getElementById('first')!
      const modal = document.querySelector('lv-visual-modal')!
      return {
        sourceParent: first.parentElement?.id,
        slot: first.getAttribute('slot'),
        hasFocusSlot: Boolean((modal.shadowRoot as ShadowRoot)?.querySelector('slot[name="focus-visual"]')),
        hasFocusedVisual: Boolean(modal.querySelector('[slot="focus-visual"]')),
      }
    })

    expect(state).toEqual({
      sourceParent: 'parent',
      slot: null,
      hasFocusSlot: false,
      hasFocusedVisual: false,
    })
  } finally {
    await page.close()
  }
})

test('show-data mode captures, traps, and restores focus', async () => {
  const page = await setupPage()
  try {
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'show-data')
    const opened = await page.locator('lv-visual-modal').evaluate((modal: any) => ({
      active: (modal.shadowRoot as ShadowRoot).activeElement?.getAttribute('aria-label'),
      dialog: (modal.shadowRoot as ShadowRoot).querySelector('[role="dialog"]')?.getAttribute('aria-modal'),
    }))
    expect(opened).toEqual({ active: 'Close visual modal', dialog: 'true' })

    await page.keyboard.press('Tab')
    const afterTab = await page.locator('lv-visual-modal').evaluate((modal: any) => {
      const active = modal.deepActiveElement()
      return {
        movedPastClose: active?.getAttribute('aria-label') !== 'Close visual modal',
        remainsInDialog: Boolean(active && modal.focusableElements().includes(active)),
      }
    })
    expect(afterTab).toEqual({ movedPastClose: true, remainsInDialog: true })
    await page.keyboard.press('Shift+Tab')
    const afterReverseTab = await page.locator('lv-visual-modal').evaluate((modal: any) => (modal.shadowRoot as ShadowRoot).activeElement?.getAttribute('aria-label'))
    expect(afterReverseTab).toBe('Close visual modal')

    await page.keyboard.press('Escape')
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.updateComplete)
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('trigger')
  } finally {
    await page.close()
  }
})
