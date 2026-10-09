import { beforeAll, expect, test } from 'bun:test'
import type { Browser } from '@playwright/test'
import { dashboardBuilderBrowserFixture } from './dashboard-builder-browser.test-fixture'

const fixture = dashboardBuilderBrowserFixture()
let browser: Browser
let baseURL = ''
beforeAll(() => { ({ browser, baseURL } = fixture) })

test('Fix visuals repairs fields and saves one balanced layout atomically', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const builder = JSON.parse(JSON.stringify(element.builder))
      const visual = builder.pages[0].visuals[0]
      builder.pages[0].visuals = ['combo', 'bar', 'line'].map((type, i) => ({ ...visual, id: `chart-${i}`, type, slots: [], previewError: 'Add a measure to preview.', placement: { col: 1, row: 1 + i * 5, colSpan: 12, rowSpan: 5 } }))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder })
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { (window as any).arrangeCommand = event.detail }, { once: true })
      await element.updateComplete
    })
    await page.getByRole('button', { name: 'Fix visuals', exact: true }).click()
    const command = await page.evaluate(() => (window as any).arrangeCommand)
    expect(command).toMatchObject({ action: 'set_placements', pageId: 'overview', fillMissingFields: true, placements: [
      { componentId: 'chart-0', placement: { column: 1, row: 1, columnSpan: 12, rowSpan: 5 } },
      { componentId: 'chart-1', placement: { column: 1, row: 6, columnSpan: 6, rowSpan: 5 } },
      { componentId: 'chart-2', placement: { column: 7, row: 6, columnSpan: 6, rowSpan: 5 } },
    ] })
  } finally { await page.close() }
})

for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
  test(`standalone Fix visuals has a labelled wand at ${viewport.width}px`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
      const control = await page.locator('lv-dashboard-builder').evaluate(async (e: any, mobile) => {
        await e.updateComplete
        const root = e.shadowRoot as ShadowRoot
        if (mobile) (root.querySelector('.more-actions') as HTMLDetailsElement).open = true
        const button = root.querySelector<HTMLButtonElement>(mobile ? '.arrange-mobile' : '.arrange-toolbar')!
        return {
          label: button.getAttribute('aria-label'), title: button.title, text: button.textContent?.trim(),
          visible: button.getBoundingClientRect().width > 0,
          icon: Boolean(button.querySelector('svg path')),
          gridIcon: Boolean(button.querySelector('svg rect')),
        }
      }, viewport.width < 640)
      expect(control).toEqual({ label: 'Fix visuals', title: 'Fix visuals', text: 'Fix visuals', visible: true, icon: true, gridIcon: false })
      expect(await page.getByRole('button', { name: 'Arrange visuals', exact: true }).count()).toBe(0)
    } finally { await page.close() }
  })
}

for (const needsRepair of [true, false]) {
  test(`standalone Fix waits for its revision and completes ${needsRepair ? 'missing-field' : 'ready'} visuals once`, async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
      const result = await page.locator('lv-dashboard-builder').evaluate(async (e: any, needsRepair) => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
        const builder = JSON.parse(JSON.stringify(e.builder))
        const visual = builder.pages[0].visuals[0]
        if (needsRepair) {
          visual.slots = []
          visual.previewError = 'Add a measure to preview.'
        }
        mergePatch({ builder }); await e.updateComplete
        const filters = JSON.stringify(e.builderFilterState)
        const commands: any[] = []
        e.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
        e.setAttribute('data-on:lv-builder-command', '')
        e.arrangeVisuals(); await e.updateComplete
        const busy = (e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled
        e.arrangeVisuals()
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: e } }))
        await e.updateComplete
        const waitingForRevision = Boolean(e.pendingFixVisuals) && e.commandPending
        builder.revision = { id: 'fixed-and-arranged', number: builder.revision.number + 1, contentHash: 'sha256:fixed' }
        visual.slots = [{ id: 'dimension-0', kind: 'dimension', fieldId: 'country', label: 'Country' }, { id: 'metric-0', kind: 'metric', fieldId: 'revenue', label: 'Revenue' }]
        visual.previewError = ''
        const placement = commands[0].placements[0].placement
        visual.placement = { col: placement.column, row: placement.row, colSpan: placement.columnSpan, rowSpan: placement.rowSpan }
        mergePatch({ builder }); await e.updateComplete
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: e } }))
        await e.updateComplete
        return {
          commands, busy, waitingForRevision, pending: e.pendingFixVisuals, message: e.fixVisualsMessage,
          finalDisabled: (e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled,
          status: e.shadowRoot.querySelector('.magic-fill-result')?.textContent?.trim(),
          undo: e.undoStack.map((revision: any) => revision.id),
          filtersUnchanged: filters === JSON.stringify(e.builderFilterState),
        }
      }, needsRepair)
      expect(result.commands).toHaveLength(1)
      expect(result.commands[0]).toMatchObject({ action: 'set_placements', fillMissingFields: true })
      expect(result.busy).toBe(true)
      expect(result.waitingForRevision).toBe(true)
      expect(result.pending).toBeNull()
      expect(result.message).toBe(needsRepair ? 'Completed 1 visual. Layout arranged.' : 'Visuals are ready. Layout arranged.')
      expect(result.status).toBe(result.message)
      expect(result.finalDisabled).toBe(false)
      expect(result.undo).toHaveLength(1)
      expect(result.filtersUnchanged).toBe(true)
    } finally { await page.close() }
  })
}

test('standalone Fix is disabled during filtering and for read-only or empty pages', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const commands: any[] = []
      e.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      const disabled: boolean[] = []
      e.builderFilterCommandInFlight = { key: 'pending-filter' }
      e.requestUpdate(); await e.updateComplete
      disabled.push((e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled)
      e.arrangeVisuals()
      e.builderFilterCommandInFlight = null
      e.builderFilterController.clear('fixture-filter')
      e.builderFilterCommandInFlight = null
      await e.updateComplete
      disabled.push((e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled)
      e.arrangeVisuals()
      e.builderFilterController.reconcile(e.builderFilterState)
      const builder = JSON.parse(JSON.stringify(e.builder))
      builder.capabilities.canEdit = false
      mergePatch({ builder }); await e.updateComplete
      disabled.push((e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled)
      e.arrangeVisuals()
      builder.capabilities.canEdit = true
      builder.pages[0].visuals = []
      mergePatch({ builder }); await e.updateComplete
      disabled.push((e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled)
      e.arrangeVisuals()
      return { disabled, commands }
    })
    expect(result.disabled).toEqual([true, true, true, true])
    expect(result.commands).toEqual([])
  } finally { await page.close() }
})

test('standalone Fix clears pending state and rolls back history after a failed save', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (e: any) => {
      await e.updateComplete
      const before = JSON.stringify(e.builder.pages)
      const commands: any[] = []
      e.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      e.arrangeVisuals(); await e.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: e, argsRaw: { status: 503 } } }))
      await e.updateComplete
      return {
        pending: e.pendingFixVisuals, message: e.fixVisualsMessage, commands,
        unchanged: before === JSON.stringify(e.builder.pages), undo: e.undoStack.length,
        disabled: (e.shadowRoot.querySelector('.arrange-toolbar') as HTMLButtonElement).disabled,
      }
    })
    expect(result.pending).toBeNull()
    expect(result.message).toContain('Dashboard builder action could not be completed')
    expect(result.message).not.toContain('Layout arranged')
    expect(result.commands).toHaveLength(1)
    expect(result.unchanged).toBe(true)
    expect(result.undo).toBe(0)
    expect(result.disabled).toBe(false)
  } finally { await page.close() }
})

test('dashboard builder keeps metadata quiet and groups secondary actions behind More', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const toolbar = root.querySelector('.toolbar-actions') as HTMLElement
      let visibilityCommand: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { visibilityCommand = event.detail }, { once: true })
      const more = root.querySelector('.more-actions') as HTMLDetailsElement
      more.open = true
      ;(root.querySelector('.more-menu button[aria-label="Toggle dashboard visibility"]') as HTMLButtonElement).click()
      return {
        badgeCount: root.querySelectorAll('.meta .badge').length,
        metadataLines: root.querySelectorAll('.meta > span').length,
        metadata: root.querySelector('.meta > span')?.textContent?.trim(),
        topLevelActions: Array.from(toolbar.children).map((child) => child.localName === 'details' ? 'more' : child.textContent?.trim()),
        moreLabel: root.querySelector('.more-actions summary')?.textContent?.trim(),
        moreAriaLabel: root.querySelector('.more-actions summary')?.getAttribute('aria-label'),
        visibilityCommand,
      }
    })
    expect(state.badgeCount).toBe(0)
    expect(state.metadataLines).toBe(1)
    expect(state.metadata).toContain('Saved · Unpublished')
    expect(state.metadata).not.toContain('Unsaved')
    expect(state.topLevelActions).toEqual(['Fix visuals', 'Hide tools', 'more', 'Undo', 'Redo', 'Switch to dark mode', 'more', 'Publish'])
    expect(state.moreLabel).toBe('More')
    expect(state.moreAriaLabel).toBe('More dashboard actions')
    expect(state.visibilityCommand).toMatchObject({ action: 'set_visibility', visibility: 'organization' })
  } finally {
    await page.close()
  }
})
