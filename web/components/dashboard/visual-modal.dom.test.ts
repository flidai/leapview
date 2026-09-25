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

test('show-data dialog fits short viewports and keeps its close control reachable', async () => {
  const page = await setupPage()
  try {
    await page.setViewportSize({ width: 844, height: 390 })
    await page.addStyleTag({ content: ':root { --base-size-28: 28px; }' })
    await dispatchVisualAction(page, 'first', 'show-data')
    const bounds = await page.getByRole('dialog').boundingBox()
    expect(bounds).not.toBeNull()
    expect(bounds!.y).toBeGreaterThanOrEqual(28)
    expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(390 - 28)
    const close = page.getByRole('button', { name: 'Close visual modal' })
    const closeBounds = await close.boundingBox()
    expect(closeBounds).not.toBeNull()
    expect(closeBounds!.y).toBeGreaterThanOrEqual(0)
    expect(closeBounds!.y + closeBounds!.height).toBeLessThanOrEqual(390)
    await close.click()
    expect(await page.getByRole('dialog').count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('show-data balances small result tables and preserves scrolling for wide results', async () => {
  const page = await setupPage()
  try {
    await page.setViewportSize({ width: 1280, height: 640 })
    await page.addStyleTag({ content: ':root { --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-caption: 400 12px/1.25 system-ui; }' })
    await page.evaluate(() => {
      const source = document.getElementById('first')!
      source.dispatchEvent(new CustomEvent('lv-visual-action', {
        bubbles: true,
        composed: true,
        detail: {
          action: 'show-data',
          visualType: 'table',
          visualId: 'first',
          title: 'first',
          columns: [
            { key: 'date', label: 'Purchase date' },
            { key: 'revenue', label: 'Revenue', align: 'right' },
          ],
          rows: Array.from({ length: 40 }, (_, index) => ({ date: `2026-09-${String((index % 30) + 1).padStart(2, '0')}`, revenue: `${index * 1250}.50` })),
          selection: [],
        },
      }))
    })
    await page.locator('lv-visual-modal').evaluate(async (modal: any) => {
      await modal.updateComplete
      await modal.shadowRoot.querySelector('lv-record-table').updateComplete
    })

    const state = await page.locator('lv-visual-modal').evaluate((modal: any) => {
      const scroll = modal.shadowRoot.querySelector('.data-scroll') as HTMLElement
      const row = modal.shadowRoot.querySelector('lv-record-table tbody tr') as HTMLElement
      const table = modal.shadowRoot.querySelector('lv-record-table table') as HTMLTableElement
      const headers = Array.from(table.querySelectorAll('th')) as HTMLElement[]
      const revenueHeaderLabel = headers[1].querySelector('.record-table-sort > span:first-child') as HTMLElement
      const dialog = modal.shadowRoot.querySelector('[role="dialog"]') as HTMLElement
      const revenueHeaderStyle = getComputedStyle(headers[1])
      const tableBounds = table.getBoundingClientRect()
      const scrollBounds = scroll.getBoundingClientRect()
      return {
        rowHeight: row.getBoundingClientRect().height,
        scrollHeight: scroll.scrollHeight,
        clientHeight: scroll.clientHeight,
        tableWidth: table.getBoundingClientRect().width,
        availableWidth: scroll.getBoundingClientRect().width,
        tableLayout: getComputedStyle(table).tableLayout,
        tableLeftGap: Math.round(tableBounds.left - scrollBounds.left),
        tableRightGap: Math.round(scrollBounds.right - tableBounds.right),
        columnWidths: headers.map((header) => Math.round(header.getBoundingClientRect().width)),
        revenueAlignment: getComputedStyle(headers[1]).textAlign,
        revenueHeaderRightGap: Math.round(
          headers[1].getBoundingClientRect().right
          - Number.parseFloat(revenueHeaderStyle.paddingRight)
          - revenueHeaderLabel.getBoundingClientRect().right,
        ),
        dialogWidth: dialog.getBoundingClientRect().width,
        dialogBottom: dialog.getBoundingClientRect().bottom,
      }
    })
    expect(state.rowHeight).toBeLessThanOrEqual(32)
    expect(state.scrollHeight).toBeGreaterThan(state.clientHeight)
    expect(state.tableLayout).toBe('auto')
    expect(state.tableWidth).toBe(320)
    expect(state.tableWidth).toBeLessThan(state.availableWidth)
    expect(state.tableLeftGap).toBe(state.tableRightGap)
    expect(state.revenueAlignment).toBe('right')
    expect(state.revenueHeaderRightGap).toBe(0)
    expect(state.dialogWidth).toBe(480)
    expect(state.dialogBottom).toBeLessThanOrEqual(640 - 28)

    const scrollTop = await page.locator('lv-visual-modal').evaluate((modal: any) => {
      const scroll = modal.shadowRoot.querySelector('.data-scroll') as HTMLElement
      scroll.scrollTop = scroll.scrollHeight
      return scroll.scrollTop
    })
    expect(scrollTop).toBeGreaterThan(0)

    await page.evaluate(() => {
      const source = document.getElementById('first')!
      const columns = Array.from({ length: 8 }, (_, index) => ({ key: `column${index}`, label: `Column ${index + 1}` }))
      source.dispatchEvent(new CustomEvent('lv-visual-action', {
        bubbles: true,
        composed: true,
        detail: {
          action: 'show-data',
          visualType: 'table',
          visualId: 'first',
          title: 'wide result',
          columns,
          rows: [Object.fromEntries(columns.map((column, index) => [column.key, `Long value ${index + 1} requiring horizontal space`]))],
          selection: [],
        },
      }))
    })
    await page.locator('lv-visual-modal').evaluate(async (modal: any) => {
      await modal.updateComplete
      await modal.shadowRoot.querySelector('lv-record-table').updateComplete
    })
    const wide = await page.locator('lv-visual-modal').evaluate((modal: any) => {
      const wrapper = modal.shadowRoot.querySelector('lv-record-table .record-table-wrap') as HTMLElement
      const dialog = modal.shadowRoot.querySelector('[role="dialog"]') as HTMLElement
      return {
        dialogWidth: dialog.getBoundingClientRect().width,
        scrollWidth: wrapper.scrollWidth,
        clientWidth: wrapper.clientWidth,
      }
    })
    expect(wide.dialogWidth).toBe(1120)
    expect(wide.scrollWidth).toBeGreaterThan(wide.clientWidth)
  } finally {
    await page.close()
  }
})

test('a previous action timer cannot clear a newer repeated notice', async () => {
  const page = await setupPage()
  try {
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.flash('Copied visual data.'))
    await page.waitForTimeout(200)
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.flash('Downloaded CSV.'))
    await page.waitForTimeout(200)
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.flash('Copied visual data.'))
    await page.waitForTimeout(1_500)

    expect(await page.locator('lv-visual-modal').evaluate((modal: any) => modal.notice)).toBe('Copied visual data.')
    expect(await page.locator('lv-visual-modal [role="status"]').textContent()).toBe('Copied visual data.')
  } finally {
    await page.close()
  }
})
