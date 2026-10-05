import { expect, test } from 'bun:test'
import { dashboardBuilderBrowserFixture } from './dashboard-builder-browser.test-fixture'
import { governedBarPreviewEnvelope } from './dashboard-builder-test-fixtures'

const fixture = dashboardBuilderBrowserFixture()

test('local builder selections and pane toggles keep the grid and its editing state intact', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const canvas = root.querySelector('.canvas') as any
      const grid = canvas.gridstack
      let editingCalls = 0
      for (const method of ['enableMove', 'enableResize']) {
        const original = grid[method].bind(grid)
        grid[method] = (...args: unknown[]) => { editingCalls++; return original(...args) }
      }
      ;(root.querySelector('[data-pane-toggle="data"]') as HTMLButtonElement).click()
      await element.updateComplete
      ;(root.querySelector('.canvas') as HTMLElement).click()
      await element.updateComplete
      ;(root.querySelector('.visual') as HTMLElement).click()
      await element.updateComplete
      return { retained: grid === canvas.gridstack, editingCalls, selected: root.querySelector('.visual')?.getAttribute('data-selected') }
    })
    expect(state).toEqual({ retained: true, editingCalls: 0, selected: 'true' })
  } finally { await page.close() }
})

test('retained builder grids use the current canvas dimensions during drag callbacks', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const canvas = root.querySelector('.canvas') as any
      const grid = canvas.gridstack
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      pages[0].canvas.width = 1000
      pages[0].canvas.height = 950
      mergePatch({ builder: { pages } })
      await element.updateComplete
      grid._gsEventHandler.drag(new Event('drag'), root.querySelector('.visual'))
      return {
        retained: grid === canvas.gridstack,
        width: canvas.style.getPropertyValue('--builder-grid-width'),
        height: canvas.style.height,
      }
    })
    expect(state).toEqual({ retained: true, width: '968px', height: '918px' })
  } finally { await page.close() }
})

test('preview replacement refreshes retained grid drag handles so the new header remains movable', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const element = page.locator('lv-dashboard-builder')
    const setup = await element.evaluate(async (builder: any, preview: any) => {
      await builder.updateComplete
      const root = builder.shadowRoot as ShadowRoot
      const canvas = root.querySelector('.canvas') as any
      const grid = canvas.gridstack
      const before = root.querySelector('.visual-drag-header')
      ;(window as any).__performancePlacementCommands = []
      builder.addEventListener('lv-builder-command', (event: CustomEvent) => {
        if (event.detail.action === 'set_placements') (window as any).__performancePlacementCommands.push(event.detail)
      })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builderVisuals: { 'sales-chart': preview } })
      await builder.updateComplete
      const host = root.querySelector('lv-visualization-host') as any
      await host.ensureMounted()
      return { retained: grid === canvas.gridstack, changedHeader: before !== root.querySelector('.visual-drag-header'), scale: builder.canvasScale }
    }, governedBarPreviewEnvelope('sha256:performance-drag'))
    expect(setup.retained).toBe(true)
    expect(setup.changedHeader).toBe(true)
    const header = await element.locator('.visual-drag-header').boundingBox()
    expect(header).not.toBeNull()
    await page.mouse.move(header!.x + header!.width / 2, header!.y + header!.height / 2)
    await page.mouse.down()
    await page.mouse.move(header!.x + header!.width / 2, header!.y + header!.height / 2 + 128 * setup.scale, { steps: 8 })
    await page.mouse.up()
    await page.waitForFunction(() => (window as any).__performancePlacementCommands.length > 0)
    const placements = await page.evaluate(() => (window as any).__performancePlacementCommands.at(-1).placements)
    expect(placements.find((placement: any) => placement.componentId === 'sales-chart').placement.row).toBeGreaterThan(1)
  } finally { await page.close() }
})
