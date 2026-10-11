import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { testVisualizationEnvelopes } from './dashboard-page-test-fixtures'
import { typographyTestTokens } from '../test-typography-tokens'
import { chromium, expect as browserExpect, type Browser } from '@playwright/test'

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
    if (url === '/motor.svg') {
      response.setHeader('content-type', 'image/svg+xml')
      response.end('<svg xmlns="http://www.w3.org/2000/svg" width="160" height="100"><rect width="160" height="100" fill="steelblue"/></svg>')
      return
    }
    if (url.startsWith('/host/')) {
      const hostRoot = join(root, 'visualization-host-test')
      const file = normalize(join(hostRoot, url.slice('/host/'.length)))
      if (!file.startsWith(hostRoot + '/')) { response.writeHead(404); response.end(); return }
      try {
        response.setHeader('content-type', 'text/javascript')
        response.end(await readFile(file))
      } catch { response.writeHead(404); response.end() }
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
  if (action === 'focus' || action === 'show-data') await browserExpect(page.getByRole('dialog')).toBeVisible()
}

test('focus action moves the live visual into the modal and restores it in place', async () => {
  const page = await setupPage()
  try {
    await page.addStyleTag({ content: ':root { --lv-bg-page: #010409; --lv-modal-backdrop: rgba(0, 0, 0, 0.35); }' })
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'focus')
    const close = page.getByRole('button', { name: 'Close visual modal' })
    await browserExpect(close).toBeFocused()

    const focusedState = await page.evaluate(() => {
      const modal = document.querySelector('lv-visual-modal')! as any
      const first = document.getElementById('first')!
      const parent = document.getElementById('parent')!
      return {
        sourceParent: first.parentElement?.localName,
        slot: first.getAttribute('slot'),
        sourcePosition: parent.children[0] === first,
        sourceInModal: modal.querySelector('[slot="focus-visual"]') === first,
        activeInModal: modal.deepActiveElement() === first.querySelector('[slot="focus-action"]'),
        nativeModal: modal.shadowRoot.querySelector('dialog')?.matches(':modal') ?? false,
      }
    })

    expect(focusedState).toEqual({
      sourceParent: 'lv-visual-modal',
      slot: 'focus-visual',
      sourcePosition: false,
      sourceInModal: true,
      activeInModal: true,
      nativeModal: true,
    })

    expect(await page.getByRole('dialog').evaluate((dialog) => getComputedStyle(dialog, '::backdrop').backgroundColor))
      .toBe('rgba(0, 0, 0, 0.35)')

    // Chromium may move forward focus to browser chrome when the native dialog
    // has only one control; reverse traversal must return to that real control.
    await page.keyboard.press('Tab')
    await browserExpect(page.locator('#trigger')).not.toBeFocused()
    await page.keyboard.press('Shift+Tab')
    await browserExpect(close).toBeFocused()

    await close.click()
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

test('focused tables fit a few rows and cap tall tables at the viewport', async () => {
  const page = await setupPage()
  const renderSourceRows = async (rowCount: number) => {
    await page.evaluate((rowCount) => {
      const source = document.getElementById('second')!
      const table = document.createElement('table')
      table.style.borderCollapse = 'collapse'
      const body = document.createElement('tbody')
      for (let index = 0; index < rowCount; index++) {
        const row = document.createElement('tr')
        row.style.height = '34px'
        const cell = document.createElement('td')
        cell.textContent = `Row ${index + 1}`
        row.append(cell)
        body.append(row)
      }
      table.append(body)
      source.replaceChildren(table)
    }, rowCount)
  }
  try {
    // Focus measures the moved live source, rather than the action's row metadata.
    await renderSourceRows(2)
    await dispatchVisualAction(page, 'second', 'focus')
    const compactHeight = await page.locator('lv-visual-modal').evaluate((modal: any) => (
      (modal.shadowRoot as ShadowRoot).querySelector('.focus-dialog')!.getBoundingClientRect().height
    ))
    expect(compactHeight).toBeLessThan(500)

    await page.getByRole('button', { name: 'Close visual modal' }).click()
    await renderSourceRows(100)
    await page.evaluate(() => {
      document.getElementById('second')!.dispatchEvent(new CustomEvent('lv-visual-action', {
        bubbles: true,
        composed: true,
        detail: {
          action: 'focus', visualType: 'table', visualId: 'second', title: 'Large table',
          columns: [{ key: 'label', label: 'Label' }], rows: [], selection: [],
          table: { availableRows: 100, rowHeight: 34 },
        },
      }))
    })
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.updateComplete)
    const tallHeight = await page.locator('lv-visual-modal').evaluate((modal: any) => (
      (modal.shadowRoot as ShadowRoot).querySelector('.focus-dialog')!.getBoundingClientRect().height
    ))
    expect(tallHeight).toBeGreaterThan(compactHeight)
    expect(tallHeight).toBeLessThanOrEqual(Math.min(920, await page.evaluate(() => window.innerHeight - 56)))
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
    const close = page.getByRole('button', { name: 'Close visual modal' })
    await browserExpect(close).toBeFocused()
    const opened = await page.locator('lv-visual-modal').evaluate((modal: any) => ({
      active: modal.deepActiveElement()?.getAttribute('aria-label'),
      dialog: (modal.shadowRoot as ShadowRoot).querySelector('[role="dialog"]')?.getAttribute('aria-modal'),
      nativeModal: (modal.shadowRoot as ShadowRoot).querySelector('dialog')?.matches(':modal'),
    }))
    expect(opened).toEqual({ active: 'Close visual modal', dialog: 'true', nativeModal: true })

    await page.keyboard.press('Tab')
    const afterTab = await page.locator('lv-visual-modal').evaluate((modal: any) => {
      const active = modal.deepActiveElement()
      return {
        movedPastClose: active?.getAttribute('aria-label') !== 'Close visual modal',
        remainsInDialog: Boolean(active && modal.shadowRoot.querySelector('dialog')?.contains(active)),
      }
    })
    expect(afterTab).toEqual({ movedPastClose: true, remainsInDialog: true })
    await page.keyboard.press('Shift+Tab')
    await browserExpect(close).toBeFocused()

    await page.keyboard.press('Escape')
    await page.locator('lv-visual-modal').evaluate((modal: any) => modal.updateComplete)
    await browserExpect(page.getByRole('dialog')).toHaveCount(0)
    await browserExpect(page.locator('#trigger')).toBeFocused()
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

test('native modality blocks high-z-index background interaction and Escape restores the trigger', async () => {
  const page = await setupPage()
  try {
    await page.addStyleTag({ content: `
      #trigger { position: fixed; left: 50%; top: 50%; width: 120px; height: 40px;
        transform: translate(-50%, -50%); z-index: 2147483647; }
    ` })
    await page.locator('#trigger').evaluate((trigger) => {
      trigger.setAttribute('data-clicks', '0')
      trigger.addEventListener('click', () => {
        trigger.setAttribute('data-clicks', String(Number(trigger.getAttribute('data-clicks')) + 1))
      })
    })
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'focus')
    const dialog = page.getByRole('dialog')
    expect(await dialog.evaluate((element) => element.matches(':modal'))).toBe(true)
    await dialog.evaluate((element) => {
      element.setAttribute('data-cancels', '0')
      element.addEventListener('cancel', () => element.setAttribute('data-cancels', '1'))
    })
    await page.locator('#trigger').evaluate((trigger) => trigger.focus())
    await browserExpect(page.getByRole('button', { name: 'Close visual modal' })).toBeFocused()

    const bounds = await page.locator('#trigger').boundingBox()
    expect(bounds).not.toBeNull()
    await page.mouse.click(bounds!.x + bounds!.width / 2, bounds!.y + bounds!.height / 2)
    await browserExpect(page.locator('#trigger')).toHaveAttribute('data-clicks', '0')
    await browserExpect(page.locator('#trigger')).not.toBeFocused()
    await browserExpect(dialog).toBeVisible()

    // Keep a handle so the native cancel event can be inspected after removal.
    const dialogHandle = await dialog.elementHandle()
    await page.keyboard.press('Escape')
    await browserExpect(dialog).toHaveCount(0)
    expect(await dialogHandle!.getAttribute('data-cancels')).toBe('1')
    await browserExpect(page.locator('#trigger')).toBeFocused()
    await browserExpect(page.locator('#parent > #first')).toHaveCount(1)
  } finally {
    await page.close()
  }
})

for (const action of ['focus', 'show-data']) {
  test(`closing ${action} before its asynchronous update does not reopen the dialog`, async () => {
    const page = await setupPage()
    try {
      await page.locator('#trigger').focus()
      const state = await page.evaluate(async (action) => {
        const modal = document.querySelector('lv-visual-modal')! as any
        const source = document.getElementById('first')!
        source.dispatchEvent(new CustomEvent('lv-visual-action', {
          bubbles: true,
          composed: true,
          detail: {
            action, visualType: 'chart', visualId: 'first', title: 'first',
            columns: [{ key: 'label', label: 'Label' }], rows: [{ label: 'A' }], selection: [],
          },
        }))
        // Close in the same task, before Lit resolves the scheduled open render.
        modal.close()
        await modal.updateComplete
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
        return {
          dialogCount: modal.shadowRoot.querySelectorAll('dialog').length,
          sourceParent: source.parentElement?.id,
          sourceSlot: source.getAttribute('slot'),
          hasClose: Boolean(source.querySelector('[slot="focus-action"]')),
          active: modal.deepActiveElement()?.id,
        }
      }, action)
      expect(state).toEqual({
        dialogCount: 0, sourceParent: 'parent', sourceSlot: null, hasClose: false, active: 'trigger',
      })
    } finally {
      await page.close()
    }
  })
}

test('switching a focused visual to show-data restores the original trigger on close', async () => {
  const page = await setupPage()
  try {
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'focus')
    await browserExpect(page.getByRole('button', { name: 'Close visual modal' })).toBeFocused()
    await dispatchVisualAction(page, 'first', 'show-data')

    await browserExpect(page.getByRole('button', { name: 'Copy', exact: true })).toBeVisible()
    await browserExpect(page.getByRole('button', { name: 'Close visual modal' })).toBeFocused()
    await browserExpect(page.locator('#parent > #first')).toHaveCount(1)
    await browserExpect(page.locator('#first')).not.toHaveAttribute('slot', 'focus-visual')
    await browserExpect(page.locator('#first [slot="focus-action"]')).toHaveCount(0)
    expect(await page.getByRole('dialog').evaluate((dialog) => dialog.matches(':modal'))).toBe(true)

    await page.keyboard.press('Escape')
    await browserExpect(page.getByRole('dialog')).toHaveCount(0)
    await browserExpect(page.locator('#trigger')).toBeFocused()
    expect(await page.locator('#parent').evaluate((parent) => parent.firstElementChild?.id)).toBe('first')
  } finally {
    await page.close()
  }
})

test('a stale mount completion cannot steal focus after reopening the same source', async () => {
  const page = await setupPage()
  try {
    await page.locator('#first').evaluate((source: any) => {
      let mounts = 0
      const pending = new Promise<void>((resolve) => { source.resolveFirstMount = resolve })
      source.ensureMounted = () => ++mounts === 1 ? pending : Promise.resolve()
    })
    await page.locator('#trigger').focus()
    await dispatchVisualAction(page, 'first', 'focus')
    const close = page.getByRole('button', { name: 'Close visual modal' })
    await browserExpect(close).toBeFocused()

    // A programmatic close leaves the first dialog's interaction flag clear,
    // so its eventual completion must be rejected by dialog identity alone.
    await close.evaluate((button: HTMLButtonElement) => button.click())
    await browserExpect(page.getByRole('dialog')).toHaveCount(0)
    await browserExpect(page.locator('#trigger')).toBeFocused()
    await dispatchVisualAction(page, 'first', 'focus')
    await browserExpect(close).toBeFocused()
    await page.locator('#first').evaluate((source) => {
      const next = document.createElement('button')
      next.textContent = 'Inspect reopened visual'
      source.append(next)
    })
    await page.keyboard.press('Tab')
    const next = page.getByRole('button', { name: 'Inspect reopened visual' })
    await browserExpect(next).toBeFocused()

    await page.locator('#first').evaluate(async (source: any) => {
      source.resolveFirstMount()
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    })
    await browserExpect(next).toBeFocused()
    expect(await page.getByRole('dialog').evaluate((dialog) => dialog.matches(':modal'))).toBe(true)
  } finally {
    await page.close()
  }
})

test('show-data balances small result tables and preserves scrolling for wide results', async () => {
  const page = await setupPage()
  try {
    await page.setViewportSize({ width: 1280, height: 640 })
    await page.addStyleTag({ content: ':root { --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --control-small-size: 32px; --lv-bg-app: #ffffff; --lv-bg-panel: #ffffff; --lv-bg-panel-muted: #f6f8fa; --lv-border-muted: 1px solid #d0d7de; --lv-border-default: 1px solid #afb8c1; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-caption: 400 12px/1.25 system-ui; }' })
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
      const secondRow = modal.shadowRoot.querySelector('lv-record-table tbody tr:nth-child(2)') as HTMLElement
      const firstCell = row.querySelector('td') as HTMLElement
      const lastCell = modal.shadowRoot.querySelector('lv-record-table tbody tr:last-child td') as HTMLElement
      const table = modal.shadowRoot.querySelector('lv-record-table table') as HTMLTableElement
      const headers = Array.from(table.querySelectorAll('th')) as HTMLElement[]
      const revenueHeaderLabel = headers[1].querySelector('.record-table-sort > span:first-child') as HTMLElement
      const dialog = modal.shadowRoot.querySelector('[role="dialog"]') as HTMLElement
      const revenueHeaderStyle = getComputedStyle(headers[1])
      const firstHeaderStyle = getComputedStyle(headers[0])
      const tableBounds = table.getBoundingClientRect()
      const scrollBounds = scroll.getBoundingClientRect()
      return {
        rowHeight: row.getBoundingClientRect().height,
        rowBackground: getComputedStyle(row).backgroundColor,
        secondRowBackground: getComputedStyle(secondRow).backgroundColor,
        rowDividerWidth: Number.parseFloat(getComputedStyle(firstCell).borderBottomWidth),
        rowDividerStyle: getComputedStyle(firstCell).borderBottomStyle,
        lastRowDividerWidth: Number.parseFloat(getComputedStyle(lastCell).borderBottomWidth),
        scrollHeight: scroll.scrollHeight,
        clientHeight: scroll.clientHeight,
        tableWidth: table.getBoundingClientRect().width,
        availableWidth: scroll.getBoundingClientRect().width,
        tableLayout: getComputedStyle(table).tableLayout,
        tableLeftGap: Math.round(tableBounds.left - scrollBounds.left),
        tableRightGap: Math.round(scrollBounds.right - tableBounds.right),
        columnWidths: headers.map((header) => Math.round(header.getBoundingClientRect().width)),
        revenueAlignment: getComputedStyle(headers[1]).textAlign,
        headerTextTransform: firstHeaderStyle.textTransform,
        firstColumnRightPadding: Number.parseFloat(firstHeaderStyle.paddingRight),
        revenueLeftPadding: Number.parseFloat(revenueHeaderStyle.paddingLeft),
        columnDividerWidth: Number.parseFloat(firstHeaderStyle.borderRightWidth),
        columnDividerStyle: firstHeaderStyle.borderRightStyle,
        revenueHeaderRightGap: Math.round(
          headers[1].getBoundingClientRect().right
          - Number.parseFloat(revenueHeaderStyle.paddingRight)
          - revenueHeaderLabel.getBoundingClientRect().right,
        ),
        dialogWidth: dialog.getBoundingClientRect().width,
        dialogBottom: dialog.getBoundingClientRect().bottom,
      }
    })
    expect(state.rowHeight).toBe(32)
    expect(state.rowBackground).not.toBe(state.secondRowBackground)
    expect(state.rowDividerWidth).toBe(1)
    expect(state.rowDividerStyle).toBe('solid')
    expect(state.lastRowDividerWidth).toBe(0)
    expect(state.scrollHeight).toBeGreaterThan(state.clientHeight)
    expect(state.tableLayout).toBe('auto')
    expect(state.tableWidth).toBe(state.availableWidth)
    expect(state.tableLeftGap).toBe(0)
    expect(state.tableRightGap).toBe(0)
    expect(state.revenueAlignment).toBe('right')
    expect(state.headerTextTransform).toBe('uppercase')
    expect(state.firstColumnRightPadding).toBe(8)
    expect(state.revenueLeftPadding).toBe(8)
    expect(state.columnDividerWidth).toBe(1)
    expect(state.columnDividerStyle).toBe('solid')
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

test('table focus fits its content while chart focus stays large', async () => {
  const page = await setupPage()
  try {
    await page.addStyleTag({ content: ':root { --base-size-28: 28px; } #second { display: block; height: 300px; }' })
    await dispatchVisualAction(page, 'second', 'focus')
    const modal = page.locator('lv-visual-modal')
    const dialog = modal.getByRole('dialog')
    const tableBounds = await dialog.boundingBox()
    expect(tableBounds!.height).toBeLessThan(400)
    expect(tableBounds!.height).toBeGreaterThanOrEqual(300)
    await page.keyboard.press('Escape')
    await dispatchVisualAction(page, 'first', 'focus')
    expect((await dialog.boundingBox())!.height).toBeGreaterThan(600)
  } finally {
    await page.close()
  }
})

async function setupRenderedPage() {
  const page = await setupPage()
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.addStyleTag({ content: `
    :root {
      ${typographyTestTokens}
      --base-size-28: 28px; --base-size-2: 2px; --base-size-4: 4px;
      --base-size-6: 6px; --base-size-8: 8px; --base-size-12: 12px;
      --base-size-16: 16px; --base-size-24: 24px; --base-size-48: 48px;
      --lv-space-sm: 8px; --lv-space-md: 12px; --lv-space-lg: 16px;
      --lv-control-small: 28px; --lv-radius-default: 6px;
      --control-small-size: 28px; --control-medium-size: 32px;
      --lv-chart-surface: #1b222a; --lv-bg-overlay: #1b222a;
      --lv-bg-panel-muted: #222a33; --lv-fg-default: #f0f3f6;
      --lv-fg-muted: #9ba7b4; --lv-line-default: #3d444d;
      --lv-border-default: 1px solid #3d444d;
      --lv-modal-backdrop: rgb(0 0 0 / .4); --zIndex-modal: 200;
    }
    #parent { display: grid; gap: 16px; width: 600px; }
    #parent > lv-visualization-host { height: 280px; }
  ` })
  await page.addScriptTag({ type: 'module', url: `${baseURL}/host/visualization-host-under-test.js` })
  await page.waitForFunction(() => customElements.get('lv-visualization-host'))
  await page.evaluate(async (envelopes) => {
    for (const [id, envelope] of [['first', envelopes.orders_chart], ['second', envelopes.orders]] as const) {
      envelope.status = { kind: 'ready' }
      envelope.diagnostics = []
      const host = document.getElementById(id) as any
      host.envelope = envelope
      await host.ensureMounted()
    }
  }, testVisualizationEnvelopes())
  return page
}

test('long focused tables stay bounded and preserve a rendered tile, live state, and updates', async () => {
  const page = await setupRenderedPage()
  try {
    await page.locator('#second').evaluate((host: any) => {
      (window as any).originalController = host.controller
      ;(window as any).originalTable = host.shadowRoot.querySelector('lv-report-table')
    })
    await dispatchVisualAction(page, 'second', 'focus')
    const modal = page.locator('lv-visual-modal')
    const dialogBounds = (await modal.getByRole('dialog').boundingBox())!
    expect(dialogBounds.height).toBeGreaterThan(800)
    const table = modal.locator('lv-report-table')
    const shell = table.locator('.shell')
    const footer = table.locator('.footer')
    await browserExpect.poll(async () => (await shell.boundingBox())?.height ?? 0).toBeGreaterThan(800)
    expect((await footer.boundingBox())!.y).toBeGreaterThan(dialogBounds.y + dialogBounds.height - 80)
    const preview = page.locator('#parent > [data-visual-focus-preview]')
    await browserExpect.poll(() => preview.locator('lv-report-table .row:not(.skeleton-row)').count()).toBeGreaterThan(0)
    expect(await preview.getAttribute('inert')).not.toBeNull()
    expect(await preview.getAttribute('aria-hidden')).toBe('true')
    await page.locator('#second').evaluate(async (host: any) => {
      const envelope = structuredClone(host.envelope)
      envelope.dataRevision++
      envelope.dataState.dataRevision++
      envelope.dataState.blocks.a.rows[0] = ['updated-order']
      // A window reply must acknowledge the live table's pending request.
      const liveTable = host.shadowRoot.querySelector('lv-report-table')
      envelope.dataState.blocks.a.requestSeq = liveTable.expectedBlocks.get('a')?.requestSeq ?? envelope.dataState.blocks.a.requestSeq
      host.envelope = envelope
      await host.ensureMounted()
    })
    await browserExpect.poll(() => table.locator('.cell-value').first().innerText()).toContain('updated-order')
    await browserExpect.poll(() => preview.locator('lv-report-table .cell-value').first().innerText()).toContain('updated-order')
    await page.setViewportSize({ width: 844, height: 390 })
    await browserExpect.poll(async () => (await shell.boundingBox())?.height ?? 0).toBeLessThan(340)
    const smallDialog = (await modal.getByRole('dialog').boundingBox())!
    const smallFooter = (await footer.boundingBox())!
    expect(smallFooter.y + smallFooter.height).toBeLessThanOrEqual(smallDialog.y + smallDialog.height)
    expect(await table.locator('.table-scrollport').evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true)
    await page.keyboard.press('Escape')
    expect(await preview.count()).toBe(0)
    expect(await page.locator('#second').evaluate((host: any) => (
      host.controller === (window as any).originalController &&
      host.shadowRoot.querySelector('lv-report-table') === (window as any).originalTable
    ))).toBe(true)
    await browserExpect.poll(async () => (await page.locator('#second lv-report-table .shell').boundingBox())?.height ?? 0).toBeLessThan(300)
  } finally {
    await page.close()
  }
}, 20_000)

test('windowed table previews follow the live viewport without requesting their own blocks', async () => {
  const page = await setupRenderedPage()
  try {
    await page.evaluate(() => {
      const host = document.getElementById('second') as any
      ;(window as any).windowRequests = []
      document.addEventListener('lv-visualization-window-request', (event: Event) => {
        const request = (event as CustomEvent).detail
        ;(window as any).windowRequests.push(request)
        const envelope = structuredClone(host.envelope)
        envelope.dataRevision++
        envelope.dataState.dataRevision++
        const firstStart = Math.max(0, request.start - 50)
        const starts = request.blockID === 'all'
          ? [firstStart, firstStart + 50, firstStart + 100]
          : [request.start]
        for (const [index, start] of starts.entries()) {
          const id = request.blockID === 'all' ? ['a', 'b', 'c'][index] : request.blockID
          envelope.dataState.blocks[id] = {
            id, start, rows: Array.from({ length: Math.min(50, 250 - start) }, (_, row) => [`o${start + row + 1}`]),
            requestSeq: request.requestSeq, resetVersion: request.resetVersion, sort: envelope.dataState.sort,
          }
        }
        host.envelope = envelope
      })
    })
    await dispatchVisualAction(page, 'second', 'focus')
    const preview = page.locator('#parent > [data-visual-focus-preview]')
    await browserExpect.poll(() => preview.locator('.row:not(.skeleton-row)').count()).toBeGreaterThan(0)
    const live = page.locator('lv-visual-modal #second lv-report-table')
    await live.locator('.table-scrollport').evaluate(element => { element.scrollTop = 4200 })
    await browserExpect.poll(() => live.locator('.cell-value').first().innerText()).toBe('o149')
    await browserExpect.poll(() => preview.locator('.cell-value').first().innerText()).toBe('o149')
    expect(await preview.locator('.skeleton-row').count()).toBe(0)
    expect(await preview.locator('.footer').innerText()).not.toContain('loading')
    const requestCount = await page.evaluate(() => (window as any).windowRequests.length)
    await preview.locator('lv-report-table').evaluate((table: any) => {
      table.ensureBlocksForScroll()
    })
    expect(await page.evaluate(() => (window as any).windowRequests.length)).toBe(requestCount)
    expect(await preview.locator('lv-report-table').evaluate((table: any) => table.expectedBlocks.size)).toBe(0)
    await page.locator('#second').evaluate(async (host: any) => {
      const envelope = structuredClone(host.envelope)
      envelope.dataRevision++
      envelope.dataState.dataRevision++
      envelope.dataState.resetVersion++
      envelope.dataState.availableRows = 3
      envelope.dataState.cardinality.count = 3
      envelope.dataState.blocks = {
        a: { id: 'a', start: 0, rows: [['filtered-1'], ['filtered-2'], ['filtered-3']],
          requestSeq: 0, resetVersion: envelope.dataState.resetVersion, sort: envelope.dataState.sort },
      }
      host.envelope = envelope
      await host.ensureMounted()
    })
    await browserExpect.poll(() => preview.locator('.cell-value').first().innerText()).toBe('filtered-1')
    await browserExpect.poll(() => preview.locator('.row:not(.skeleton-row)').count()).toBe(3)
    expect(await preview.locator('.skeleton-row').count()).toBe(0)
    await page.keyboard.press('Escape')
    expect(await preview.count()).toBe(0)
  } finally {
    await page.close()
  }
}, 20_000)

test('short tables fit their rows without empty space and remain usable when the viewport shrinks', async () => {
  const page = await setupRenderedPage()
  try {
    await page.locator('#second').evaluate(async (host: any) => {
      const envelope = structuredClone(host.envelope)
      envelope.dataState.cardinality.count = 5
      envelope.dataState.availableRows = 5
      envelope.dataState.blocks.a.rows = envelope.dataState.blocks.a.rows.slice(0, 5)
      host.envelope = envelope
      await host.ensureMounted()
    })
    await dispatchVisualAction(page, 'second', 'focus')
    const modal = page.locator('lv-visual-modal')
    const tableShell = modal.locator('lv-report-table .shell')
    await browserExpect.poll(async () => (await tableShell.boundingBox())?.height ?? 0).toBeLessThan(400)
    expect((await tableShell.boundingBox())!.height).toBeGreaterThan(200)
    const lastRow = (await modal.locator('lv-report-table .row:not(.skeleton-row)').last().boundingBox())!
    const compactFooter = (await modal.locator('lv-report-table .footer').boundingBox())!
    expect(compactFooter.y - lastRow.y - lastRow.height).toBeLessThanOrEqual(2)
    expect(await modal.locator('lv-report-table .row:not(.skeleton-row)').count()).toBe(5)
    const fiveRowHeight = (await tableShell.boundingBox())!.height
    await page.locator('#second').evaluate(async (host: any) => {
      const envelope = structuredClone(host.envelope)
      envelope.dataRevision++
      envelope.dataState.dataRevision++
      envelope.dataState.cardinality.count = 3
      envelope.dataState.availableRows = 3
      envelope.dataState.blocks.a.rows = envelope.dataState.blocks.a.rows.slice(0, 3)
      host.envelope = envelope
      await host.ensureMounted()
    })
    await browserExpect.poll(async () => (await tableShell.boundingBox())?.height ?? 0).toBeLessThan(fiveRowHeight - 50)
    expect(await modal.locator('lv-report-table .row:not(.skeleton-row)').count()).toBe(3)
    await page.setViewportSize({ width: 844, height: 390 })
    await browserExpect.poll(async () => (await tableShell.boundingBox())?.height ?? 0).toBeLessThan(340)
    const bounds = (await modal.getByRole('dialog').boundingBox())!
    const footer = (await modal.locator('lv-report-table .footer').boundingBox())!
    expect(bounds.y).toBeGreaterThanOrEqual(28)
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(390 - 28)
    expect(footer.y + footer.height).toBeLessThanOrEqual(bounds.y + bounds.height)
    await modal.getByRole('button', { name: 'Close visual modal' }).click()
    expect(await page.locator('#parent > [data-visual-focus-preview]').count()).toBe(0)
  } finally {
    await page.close()
  }
}, 20_000)

test('focused pie charts keep the original chart rendered and clean up when the modal detaches', async () => {
  const page = await setupRenderedPage()
  try {
    await page.locator('#first').evaluate(async (host: any) => {
      const envelope = structuredClone(host.envelope)
      envelope.specRevision = `sha256:${'4'.repeat(64)}`
      envelope.dataState.specRevision = envelope.specRevision
      for (const dataset of envelope.dataState.datasets) dataset.specRevision = envelope.specRevision
      const { x, y, ...base } = envelope.spec
      envelope.spec = {
        ...base, kind: 'proportional', mark: 'pie',
        category: { dataset: 'primary', field: 'label' },
        value: { dataset: 'primary', field: 'value' },
        presentation: {
          legend: 'bottom', labelPosition: 'outside', orientation: 'vertical', rose: false,
          labelPolicy: { density: 'always', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true },
        },
      }
      host.envelope = envelope
      await host.ensureMounted()
    })
    await dispatchVisualAction(page, 'first', 'focus')
    const preview = page.locator('#parent > [data-visual-focus-preview]')
    for (const host of [preview, page.locator('lv-visual-modal #first')]) {
      await browserExpect.poll(() => host.evaluate(async (element: any) => {
        await element.ensureMounted()
        const canvas = element.shadowRoot.querySelector('.renderer canvas') as HTMLCanvasElement | null
        if (!canvas?.width || !canvas.height) return false
        const center = canvas.getContext('2d')!.getImageData(canvas.width / 2, canvas.height / 2, 1, 1).data
        return element.envelope.spec.kind === 'proportional' && center[3] > 0
      })).toBe(true)
    }
    expect((await preview.boundingBox())!.height).toBe(280)
    expect((await page.locator('lv-visual-modal #first').boundingBox())!.height).toBeGreaterThan(800)
    await page.locator('lv-visual-modal').evaluate(modal => modal.remove())
    expect(await preview.count()).toBe(0)
    expect(await page.locator('#parent > #first').count()).toBe(1)
  } finally {
    await page.close()
  }
}, 20_000)

for (const interaction of ['keyboard', 'pointer'] as const) {
  test(`${interaction} image preview consumes its Escape before the focused table modal closes`, async () => {
    const page = await setupRenderedPage()
    try {
      await page.locator('#second').evaluate(async (host: any) => {
        const table = host.shadowRoot.querySelector('lv-report-table')
        const sort = { key: 'label', direction: 'asc' }
        table.table = { ...table.table, availableRows: 1, cardinality: { kind: 'exact', value: 1 }, sort,
          columns: [
            { key: 'label', label: 'Part', role: 'row_header' },
            { key: 'preview', label: 'Preview', role: 'row_header', content: { kind: 'image', display: 'tooltip', altField: 'label', width: 160, height: 100 } },
          ],
          blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort, rows: [{ id: 'part-1', label: 'Drive motor', preview: '/motor.svg' }] } },
        }
        await table.updateComplete
        ;(window as any).originalTable = table
        ;(window as any).originalController = host.controller
      })
      await page.locator('#trigger').focus()
      await dispatchVisualAction(page, 'second', 'focus')
      const modal = page.locator('lv-visual-modal')
      const dialog = modal.getByRole('dialog')
      const trigger = modal.getByRole('button', { name: 'View image: Drive motor', exact: true })
      const preview = modal.locator('lv-table-cell-content .preview')
      if (interaction === 'keyboard') {
        // Reach the preview through real sequential keyboard navigation.
        for (let step = 0; step < 25 && !await trigger.evaluate(element => element.matches(':focus')); step++) {
          await page.keyboard.press('Tab')
        }
        await browserExpect(trigger).toBeFocused()
      } else {
        await trigger.hover()
      }
      await browserExpect(trigger).toHaveAttribute('aria-expanded', 'true')
      await browserExpect(preview).toBeVisible()
      await page.keyboard.press('Escape')
      await browserExpect(trigger).toHaveAttribute('aria-expanded', 'false')
      await browserExpect(preview).toBeHidden()
      await browserExpect(dialog).toBeVisible()
      expect(await dialog.evaluate(element => element.matches(':modal'))).toBe(true)
      if (interaction === 'keyboard') await browserExpect(trigger).toBeFocused()
      expect(await page.locator('#parent > [data-visual-focus-preview]').count()).toBe(1)
      await page.keyboard.press('Escape')
      await browserExpect(dialog).toHaveCount(0)
      await browserExpect(page.locator('#trigger')).toBeFocused()
      expect(await page.locator('#parent > [data-visual-focus-preview]').count()).toBe(0)
      expect(await page.locator('#parent > #second').evaluate((host: any) => (
        host.controller === (window as any).originalController &&
        host.shadowRoot.querySelector('lv-report-table') === (window as any).originalTable
      ))).toBe(true)
    } finally { await page.close() }
  }, 20_000)
}
