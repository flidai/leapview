import { beforeAll, expect, test } from 'bun:test'
import type { Browser } from '@playwright/test'
import { dashboardBuilderBrowserFixture } from './dashboard-builder-browser.test-fixture'
import { governedBarPreviewEnvelope, headerlessKPIPreviewEnvelope, windowedTablePreviewEnvelope } from './dashboard-builder-test-fixtures'
import { verifyBuilderZoomActionTargets } from './dashboard-builder-zoom-targets.test-fixture'
import visualReference from '../../../docs/visuals/catalog.json'

const fixture = dashboardBuilderBrowserFixture()
let browser: Browser
let baseURL = ''
beforeAll(() => { ({ browser, baseURL } = fixture) })

test('saved visual imports update the current builder without navigation and retain undo history', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const envelope = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      ;(window as any).originalBuilder = element
      ;(window as any).originalCanvas = element.shadowRoot.querySelector('.canvas')
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builderFilterContract: { definitions: { old: { id: 'old' } }, bindings: { old_page: { key: 'old_page', id: 'old', filter: 'old', scope: 'page', pageID: 'overview', default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } } }, builderFilterState: { appliedControls: { old_page: { expression: { kind: 'unfiltered' } } }, draftControls: {} }, builderFilterOptionPages: { old_page: { bindingKey: 'old_page' } } })
      const builder = JSON.parse(JSON.stringify(element.builder))
      builder.revision = { id: 'rev-8', number: 8, contentHash: 'sha256:imported' }
      builder.pages[0].visuals.push({ ...builder.pages[0].visuals[0], id: 'saved-copy', visualId: 'saved-copy', placement: { col: 1, row: 7, colSpan: 6, rowSpan: 5 } })
      return { builder, builderVisuals: {}, builderFilterContract: { applicationMode: 'immediate', definitions: {}, bindings: {} }, builderFilterState: { revision: 1, appliedControls: {}, draftControls: {}, dirtyBindings: [], defaultsRevision: 'current' }, builderFilterOptionPages: {}, runtime: { servingStateId: 'generation-7' }, status: element.status }
    })
    let imports = 0
    await page.route('**/draft/saved-visual', async route => {
      imports++
      const form = new URLSearchParams(route.request().postData() ?? '')
      expect(form.get('builderReceipt')).toBe('1')
      expect(form.get('pageId')).toBe('overview')
      expect(form.get('savedVisualId')).toBe('saved-visual-1')
      expect(JSON.parse(form.get('builderRuntime')!).servingStateId).toBe('generation-7')
      const imported = envelope.builder.pages[0].visuals[1]
      imported.id = imported.visualId = `saved_${form.get('savedVisualId')!.replaceAll('-', '')}_${form.get('idempotencyKey')!.replaceAll('-', '')}`
      await route.fulfill({ contentType: 'text/html', body: `<html><body><div id="chat-dashboard-receipt"></div><script>parent.postMessage(${JSON.stringify({ type: 'lv-builder-imported', envelope, agentContext: {} })}, location.origin)</script></body></html>` })
    })
    await page.locator('lv-dashboard-builder').evaluate((element: any) => element.addSavedVisual('saved-visual-1'))
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-builder') as any)?.builder?.revision?.id === 'rev-8')
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        sameBuilder: (window as any).originalBuilder === element,
        sameCanvas: (window as any).originalCanvas === element.shadowRoot.querySelector('.canvas'),
        selectedPage: element.builder.selectedPageId,
        pending: element.commandPending,
        undo: element.undoStack.map((revision: any) => revision.id),
        visuals: element.shadowRoot.querySelectorAll('.canvas .visual').length,
        importedSource: element.importedVisualSources.get(element.builder.pages[0].visuals[1].id),
        staleBindings: Object.keys(element.builderFilterContract.bindings),
        staleControls: Object.keys(element.builderFilterState.appliedControls),
        staleOptions: Object.keys(element.rawBuilderFilterOptionPages),
      }
    })
    expect(imports).toBe(1)
    expect(page.url()).toBe(`${baseURL}/`)
    expect(state).toEqual({ sameBuilder: true, sameCanvas: false, selectedPage: 'overview', pending: false, undo: ['rev-7'], visuals: 2, importedSource: 'saved-visual-1', staleBindings: [], staleControls: [], staleOptions: [] })
  } finally {
    await page.close()
  }
})

test('dashboard builder clears pending filter commands and surfaces transport failures', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builderFilterContract: { applicationMode: 'immediate', definitions: { status: { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } } }, bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } } },
        builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' },
      })
      await element.updateComplete
      element.dispatchEvent(new CustomEvent('lv-filter-mutate', { bubbles: true, composed: true, detail: { bindingKey: key, expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'paid' }] } } }))
      const pendingBefore = element.builderFilterController.pending
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await element.updateComplete
      return { pendingBefore, pendingAfter: element.builderFilterController.pending, error: (element.shadowRoot as ShadowRoot).querySelector('.filter-validation')?.textContent?.trim() ?? '' }
    })
    expect(state.pendingBefore).toBe(true)
    expect(state.pendingAfter).toBe(false)
    expect(state.error).toContain('Dashboard filter update could not be completed')
  } finally {
    await page.close()
  }
})

test('dashboard builder authors one visual interaction target at a time', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const visual = (id: string, visualId: string, title: string, fields: string[], placement: Record<string, number>) => ({
        id, visualId, title, titleVisible: true, type: 'bar', legendVisible: true, axisVisible: true, dataLabelsVisible: false, formatOptions: [], placement,
        slots: fields.map((fieldId, index) => ({ id: `slot-${index}`, label: fieldId, kind: index === 0 ? 'dimension' : 'metric', fieldId, required: true })), filters: [],
      })
      const source = {
        ...visual('source-component', 'source-visual', 'Sales by status', ['orders.status', 'orders.total'], { col: 1, row: 1, colSpan: 4, rowSpan: 4 }),
        interaction: { configured: true, editable: true, mode: 'single', toggle: true, mappings: [{ field: 'orders.status', value: 'orders.status' }], targets: ['filter-visual'], highlightTargets: ['highlight-visual'], noneTargets: [] },
      }
      mergePatch({ builder: { selectedVisualId: 'source-component', pages: [{ id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [
        source,
        visual('filter-component', 'filter-visual', 'Filtered revenue', ['orders.total'], { col: 5, row: 1, colSpan: 4, rowSpan: 4 }),
        visual('filter-component-copy', 'filter-visual', 'Filtered revenue copy', ['orders.total'], { col: 9, row: 1, colSpan: 4, rowSpan: 4 }),
        visual('highlight-component', 'highlight-visual', 'Revenue comparison', ['orders.status', 'orders.total'], { col: 1, row: 5, colSpan: 4, rowSpan: 4 }),
      ], filterComponents: [] }] } })
      await element.updateComplete
      const rows = Array.from(root.querySelectorAll<HTMLElement>('[data-interaction-target]')).map((row) => ({
        id: row.dataset.interactionTarget,
        title: row.querySelector('.interaction-target-title')?.textContent?.trim(),
        checked: row.querySelector<HTMLInputElement>('input:checked')?.value,
        options: Array.from(row.querySelectorAll<HTMLInputElement>('input')).map((input) => ({ value: input.value, disabled: input.disabled })),
      }))
      root.querySelector<HTMLInputElement>('[data-interaction-target="highlight-visual"] input[value="none"]')?.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { rows, commands, sectionLabel: root.querySelector('.interaction-editor')?.getAttribute('aria-label') }
    })
    expect(state.sectionLabel).toBe('Visual interactions')
    expect(state.rows).toHaveLength(2)
    expect(state.rows.map((row) => ({ id: row.id, checked: row.checked }))).toEqual([
      { id: 'filter-visual', checked: 'filter' },
      { id: 'highlight-visual', checked: 'highlight' },
    ])
    expect(state.rows[0].options.find((option) => option.value === 'highlight')?.disabled).toBe(true)
    expect(state.rows[1].options.every((option) => !option.disabled)).toBe(true)
    expect(state.commands.at(-1)).toMatchObject({ action: 'set_interaction_target', pageId: 'overview', visualId: 'source-component', targetVisualId: 'highlight-component', effect: 'none' })
  } finally {
    await page.close()
  }
})

test('dashboard builder gates publishing on exact draft state and visible validation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const publish = root.querySelector<HTMLButtonElement>('[data-builder-action="publish"]')!
      const initial = { disabled: publish.disabled, label: publish.textContent?.trim(), previewAction: Boolean(root.querySelector('[data-builder-action="preview"]')) }
      publish.click()
      await element.updateComplete
      const publishing = { disabled: publish.disabled, label: publish.textContent?.trim() }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { hasUnpublishedChanges: false, lifecycle: 'published', save: { state: 'saved', message: 'Saved' } } })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const published = { disabled: publish.disabled, label: publish.textContent?.trim() }
      mergePatch({ builder: { hasUnpublishedChanges: true, diagnostics: [{ severity: 'error', code: 'INVALID_VISUAL', message: 'Choose a supported field.' }], preview: { href: '' } } })
      await element.updateComplete
      const details = root.querySelector<HTMLDetailsElement>('.secondary-details')!
      const blocked = { disabled: publish.disabled, title: publish.title, detailsOpen: details.open, summary: details.querySelector('summary')?.textContent?.trim(), previewLink: Boolean(root.querySelector('[data-builder-action="preview"]')) }
      mergePatch({ builder: { hasUnpublishedChanges: true, diagnostics: [], preview: { active: false, href: '', error: 'strictly compile dashboard draft: compile dashboard filters: filter "category": is visible on incompatible target "overview/sales-chart"; narrow targets explicitly' } } })
      await element.updateComplete
      const filterBlocked = {
        disabled: publish.disabled,
        title: publish.title,
        filterMessage: root.querySelector('.filter-validation')?.textContent?.trim(),
      }
      return { initial, publishing, published, blocked, filterBlocked, commands }
    })
    expect(state.initial.disabled).toBe(false)
    expect(state.initial.label).toBe('Publish')
    expect(state.initial.previewAction).toBe(false)
    expect(state.publishing).toEqual({ disabled: true, label: 'Publishing…' })
    expect(state.published).toEqual({ disabled: true, label: 'Published' })
    expect(state.blocked.disabled).toBe(true)
    expect(state.blocked.title).toContain('Fix 1 validation error')
    expect(state.blocked.detailsOpen).toBe(true)
    expect(state.blocked.summary).toBe('Fix 1 validation error')
    expect(state.blocked.previewLink).toBe(false)
    expect(state.filterBlocked.disabled).toBe(true)
    expect(state.filterBlocked.title).toContain('filter scope')
    expect(state.filterBlocked.filterMessage).toBe('Choose a narrower filter scope for compatible visuals.')
    expect(state.commands.at(-1)).toMatchObject({ action: 'publish', revisionId: 'rev-7', revisionNumber: '7' })
  } finally {
    await page.close()
  }
})


test('collapsing every pane retains the tool headers and saved library tabs retain their frame', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const editor = page.locator('lv-dashboard-builder')
    for (const pane of ['Filters', 'Visuals', 'Data']) {
      await editor.getByRole('button', { name: `Collapse ${pane} pane`, exact: true }).click()
    }
    expect(await editor.locator('.right-dock').isVisible()).toBe(true)
    await editor.getByRole('button', { name: 'Expand Data pane', exact: true }).click()
    expect(await editor.getByRole('searchbox', { name: 'Search fields' }).isVisible()).toBe(true)
    await editor.getByRole('button', { name: 'Saved visuals', exact: true }).click()
    await editor.evaluate((element: any) => { (window as any).libraryFrame = element.shadowRoot.querySelector('.saved-visuals-frame') })
    expect(await editor.locator('.data-pane .pane-title').textContent()).toBe('Data')
    await editor.getByRole('button', { name: 'Fields', exact: true }).click()
    await editor.getByRole('button', { name: 'Saved visuals', exact: true }).click()
    expect(await editor.evaluate((element: any) => element.shadowRoot.querySelector('.saved-visuals-frame') === (window as any).libraryFrame)).toBe(true)
    await editor.getByRole('button', { name: 'Collapse Data pane', exact: true }).click()
    expect(await editor.locator('.right-dock').isVisible()).toBe(true)
    await editor.getByRole('button', { name: 'Expand Data pane', exact: true }).click()
    expect(await editor.getByRole('searchbox', { name: 'Search fields' }).isVisible()).toBe(true)
  } finally { await page.close() }
})


test('embedded chat panels keep distinct click targets and default Data to fields', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 850 } })
  try {
    await page.goto(`${baseURL}/embed-host`)
    const frame = page.frameLocator('iframe')
    const editor = frame.locator('lv-dashboard-builder')
    await frame.locator('.field-results').waitFor()
    expect(await editor.getByRole('searchbox', { name: 'Search fields' }).isVisible()).toBe(true)
    for (const pane of ['filters', 'visuals', 'data']) {
      const toggle = editor.locator(`[data-pane-toggle="${pane}"]`)
      if (await toggle.getAttribute('aria-expanded') === 'false') await toggle.click()
      expect(await toggle.getAttribute('aria-expanded')).toBe('true')
      const overlap = await toggle.evaluate(e => {
        const rect = e.getBoundingClientRect()
        const section = e.closest('aside')!.getBoundingClientRect()
        return rect.top < section.top || rect.bottom > section.bottom + 1
      })
      expect(overlap).toBe(false)
    }
    await editor.getByRole('button', { name: 'Saved visuals', exact: true }).click()
    expect(await editor.locator('.data-pane .pane-title').textContent()).toBe('Data')
    await editor.getByRole('button', { name: 'Collapse Data pane', exact: true }).click()
    expect(await editor.locator('.right-dock').isVisible()).toBe(true)
    expect(await editor.locator('[data-pane-toggle="visuals"]').count()).toBe(1)
    for (const pane of ['filters', 'visuals', 'data']) {
      expect(await editor.locator(`[data-pane-toggle="${pane}"]`).isVisible()).toBe(true)
    }
    await editor.getByRole('button', { name: 'Expand Data pane', exact: true }).click()
    expect(await editor.getByRole('searchbox', { name: 'Search fields' }).isVisible()).toBe(true)
  } finally { await page.close() }
})

test('chat preview starts with side-by-side tools open and preserves independent manual collapse', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 850 } })
  try {
    await page.addInitScript(() => {
      if (!sessionStorage.getItem('seeded-pane-preferences')) {
        localStorage.setItem('leapview-dashboard-builder-collapsed-panes-chat', JSON.stringify({ version: 2, collapsed: ['filters', 'visuals', 'agent'] }))
        sessionStorage.setItem('seeded-pane-preferences', '1')
      }
    })
    await page.goto(`${baseURL}/embed-host`)
    const frame = page.frameLocator('iframe')
    const editor = frame.locator('lv-dashboard-builder')
    await frame.locator('.field-results').waitFor()
    expect(await editor.locator('[data-pane-toggle="visuals"]').count()).toBe(1)
    for (const pane of ['filters', 'visuals', 'data']) {
      expect(await editor.locator(`[data-pane-toggle="${pane}"]`).getAttribute('aria-expanded')).toBe('true')
    }
    const regions = await editor.evaluate((element: any) => ['.canvas-pane', '.filters-pane', '.visual-builder', '.data-pane'].map(selector => {
      const rect = element.shadowRoot.querySelector(selector).getBoundingClientRect()
      return { left: rect.left, right: rect.right, top: rect.top }
    }))
    for (let i = 1; i < regions.length; i++) {
      expect(regions[i].left).toBeGreaterThanOrEqual(regions[i - 1].right - 1)
      expect(regions[i].top).toBeCloseTo(regions[0].top, 0)
    }
    expect(await editor.locator('[data-visual-picker-type="bar"]').isVisible()).toBe(true)
    expect(await editor.locator('.visual-builder').evaluate(e => e.scrollWidth <= e.clientWidth + 1)).toBe(true)
    expect(await editor.getByRole('button', { name: 'Collapse Visuals pane', exact: true }).isVisible()).toBe(true)
    expect(await editor.locator('.visual-builder .pane-content').isVisible()).toBe(true)
    await editor.getByRole('button', { name: 'Collapse Data pane', exact: true }).click()
    expect(await editor.getByRole('button', { name: 'Collapse Filters pane', exact: true }).isVisible()).toBe(true)
    expect(await editor.locator('.visual-builder .pane-content').isVisible()).toBe(true)
    const visualBefore = await editor.evaluate((element: any) => JSON.stringify(element.builder.pages))
    await editor.getByRole('button', { name: 'Collapse Visuals pane', exact: true }).click()
    expect(await editor.locator('#builder-visuals-content').isVisible()).toBe(false)
    expect(await editor.locator('[data-pane-toggle="filters"]').getAttribute('aria-expanded')).toBe('true')
    await page.reload()
    await frame.locator('[data-pane-toggle="data"]').waitFor()
    expect(await editor.locator('[data-pane-toggle="data"]').getAttribute('aria-expanded')).toBe('false')
    expect(await editor.locator('[data-pane-toggle="filters"]').getAttribute('aria-expanded')).toBe('true')
    expect(await editor.locator('.visual-builder').getAttribute('data-collapsed')).toBe('true')
    await editor.getByRole('button', { name: 'Expand Visuals pane', exact: true }).click()
    expect(await editor.locator('#builder-visuals-content').isVisible()).toBe(true)
    expect(await editor.evaluate((element: any) => JSON.stringify(element.builder.pages))).toBe(visualBefore)
    const retained = await editor.evaluate(async (element: any) => {
      const fields = element.shadowRoot.querySelector('#builder-visuals-content')
      element.shadowRoot.querySelector('[data-pane-toggle="visuals"]').click()
      await element.updateComplete
      element.shadowRoot.querySelector('[data-pane-toggle="visuals"]').click()
      await element.updateComplete
      return fields === element.shadowRoot.querySelector('#builder-visuals-content')
    })
    expect(retained).toBe(true)
  } finally { await page.close() }
})

for (const embedded of [false, true]) {
  test(`Visual magic in the ${embedded ? 'preview' : 'standalone'} builder repairs missing fields and fits compact charts`, async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    try {
      await page.goto(embedded ? `${baseURL}/embed-host` : baseURL)
      const editor = embedded ? page.frameLocator('iframe').locator('lv-dashboard-builder') : page.locator('lv-dashboard-builder')
      await editor.locator('.field-results').waitFor()
      expect(await editor.getByRole('button', { name: 'Arrange visuals', exact: true }).count()).toBe(0)
      expect(await editor.getByRole('button', { name: 'Collapse Visuals pane', exact: true }).isVisible()).toBe(true)
      await editor.evaluate(async (element: any) => {
        const builder = JSON.parse(JSON.stringify(element.builder))
        const visual = builder.pages[0].visuals[0]
        builder.pages[0].visuals = ['combo', 'bar', 'line'].map((type, i) => ({ ...visual, id: `chart-${i}`, type, slots: [], previewError: 'Add a measure to preview.', placement: { col: 1, row: 1 + i * 5, colSpan: 12, rowSpan: 5 } }))
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
        mergePatch({ builder })
        element.canvasZoom = 1.25
        element.testCommands = []
        element.addEventListener('lv-builder-command', (event: CustomEvent) => element.testCommands.push(event.detail))
        await element.updateComplete
        window.dispatchEvent(new MessageEvent('message', { origin: location.origin, source: window, data: { type: 'lv-arrange-dashboard-visuals' } }))
      })
      expect(await editor.evaluate((e: any) => e.testCommands.length)).toBe(0)
      await editor.getByRole('button', { name: 'Visual magic', exact: true }).click()
      expect(await editor.getByRole('button', { name: 'Visual magic', exact: true }).isDisabled()).toBe(true)
      await editor.evaluate(async (e: any) => {
        const started = Date.now()
        while (e.testCommands.length !== 1) {
          if (Date.now() - started > 3000) throw new Error('Parent Arrange command did not reach builder')
          await new Promise(resolve => setTimeout(resolve, 10))
        }
      })
      expect(await editor.evaluate((e: any) => e.testCommands[0])).toMatchObject({ action: 'set_placements', fillMissingFields: true, placements: [
        { componentId: 'chart-0', placement: { column: 1, row: 1, columnSpan: 6, rowSpan: 5 } },
        { componentId: 'chart-1', placement: { column: 7, row: 1, columnSpan: 6, rowSpan: 5 } },
        { componentId: 'chart-2', placement: { column: 1, row: 6, columnSpan: 6, rowSpan: 5 } },
      ] })
      expect(await editor.evaluate((e: any) => e.canvasZoom)).toBeNull()
      const result = await editor.evaluate(async (e: any) => {
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: e } }))
        await e.updateComplete
        return { pending: e.pendingFixVisuals, message: e.fixVisualsMessage }
      })
      expect(result.pending).toBeNull()
      expect(result.message).toContain('3 visuals could not be completed automatically')
      expect(await editor.getByRole('status').filter({ hasText: result.message }).isVisible()).toBe(true)
    } finally { await page.close() }
  })

  test(`Visual magic in the ${embedded ? 'preview' : 'standalone'} builder also fits completed charts`, async () => {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
    try {
      await page.goto(embedded ? `${baseURL}/embed-host` : baseURL)
      const editor = embedded ? page.frameLocator('iframe').locator('lv-dashboard-builder') : page.locator('lv-dashboard-builder')
      await editor.locator('.field-results').waitFor()
      await editor.evaluate(async (element: any) => {
        const builder = JSON.parse(JSON.stringify(element.builder))
        const visual = builder.pages[0].visuals[0]
        const metric = { id: 'value', label: 'Total', kind: 'metric', fieldId: 'orders.total', required: true }
        builder.pages[0].visuals = ['kpi', 'line'].map((type, i) => ({ ...visual, id: `ready-${i}`, type, previewError: '', slots: type === 'kpi' ? [metric] : [...visual.slots, metric], placement: { col: 1, row: 1 + i * 5, colSpan: 12, rowSpan: 5 } }))
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
        mergePatch({ builder })
        element.testCommands = []
        element.addEventListener('lv-builder-command', (event: CustomEvent) => element.testCommands.push(event.detail))
        await element.updateComplete
      })
      await editor.getByRole('button', { name: 'Visual magic', exact: true }).click()
      const command = await editor.evaluate((e: any) => e.testCommands[0])
      expect(command).toMatchObject({ action: 'set_placements', placements: [
        { componentId: 'ready-0', placement: { column: 1, row: 1, columnSpan: 3, rowSpan: 2 } },
        { componentId: 'ready-1', placement: { column: 4, row: 1, columnSpan: 9, rowSpan: 5 } },
      ] })
      expect(command.fillMissingFields).toBeUndefined()
    } finally { await page.close() }
  })

}

test('new preview visual automatically fits after its add settles and shares one Undo', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(`${baseURL}/embed-host`)
    const editor = page.frameLocator('iframe').locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (element: any) => {
      const original = element.builder.revision.id
      const originalPlacement = { ...element.builder.pages[0].visuals[0].placement }
      const originalID = element.builder.pages[0].visuals[0].id
      element.testCommands = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => element.testCommands.push(event.detail))
      element.addVisual('funnel')
      const builder = JSON.parse(JSON.stringify(element.builder))
      builder.revision.id = 'added-funnel'
      builder.revision.number++
      builder.pages[0].visuals.push({ ...builder.pages[0].visuals[0], id: 'new-funnel', type: 'funnel', slots: [], placement: { col: 1, row: 12, colSpan: 12, rowSpan: 12 } })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder })
      await element.updateComplete
      const beforeSettle = element.testCommands.length
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const afterSettle = element.testCommands.slice()
      const undo = element.undoStack.map((r: any) => r.id)
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      return { original, originalPlacement, originalID, beforeSettle, commands: afterSettle, undo, finalCount: element.testCommands.length }
    })
    expect(result.beforeSettle).toBe(1)
    expect(result.commands.map((command: any) => command.action)).toEqual(['add_visual', 'set_placements'])
    expect(result.commands[1].placements.find((p: any) => p.componentId === 'new-funnel').placement).toMatchObject({ columnSpan: 6, rowSpan: 5 })
    expect(result.commands[1].placements.find((p: any) => p.componentId === result.originalID).placement).toEqual({ column: result.originalPlacement.col, row: result.originalPlacement.row, columnSpan: result.originalPlacement.colSpan, rowSpan: result.originalPlacement.rowSpan })
    expect(result.undo).toEqual([result.original])
    expect(result.finalCount).toBe(2)
  } finally { await page.close() }
})

test('filter menu excludes incompatible datasets without blocking unfinished visuals', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    await editor.evaluate(async (element: any) => {
      const builder = JSON.parse(JSON.stringify(element.builder))
      const dimension = (id: string) => ({ id, label: id, kind: 'dimension', dataType: 'string', roles: ['dimension'], canFilter: id === 'country' })
      builder.semanticModel.datasets = [
        { id: 'sales', title: 'Sales', fields: [dimension('country'), { id: 'revenue', label: 'Revenue', kind: 'metric', dataType: 'number', roles: ['metric'] }] },
        { id: 'cash', title: 'Cash', fields: [dimension('scenario')] },
      ]
      const visual = builder.pages[0].visuals[0]
      builder.pages = [{ ...builder.pages[0], visuals: [
        { ...visual, datasetId: undefined, slots: [{ id: 'revenue', label: 'Revenue', fieldId: 'revenue', kind: 'metric', required: true }] },
        { ...visual, id: 'unfinished', datasetId: undefined, slots: [] },
      ] }]
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder })
      await element.updateComplete
    })
    expect(await editor.locator('.filter-add-option[data-field-id="country"]').evaluate((e: HTMLButtonElement) => e.disabled)).toBe(false)
    expect(await editor.locator('.filter-add-option[data-field-id="scenario"]').evaluate((e: HTMLButtonElement) => e.disabled)).toBe(true)
    const result = await editor.evaluate(async (element: any) => {
      const commands: any[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      const field = element.builder.semanticModel.datasets[1].fields[0]
      element.addFilterForField(field)
      await element.updateComplete
      return { commands, message: element.builderFilterTransportError }
    })
    expect(result.commands).toEqual([])
    expect(result.message).toContain('does not apply')
  } finally { await page.close() }
})

test('Add filter lists a shared semantic dimension once across datasets', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    await editor.evaluate(async (element: any) => {
      const field = { id: 'country', label: 'Country', kind: 'dimension', dataType: 'string', roles: ['dimension'], canFilter: true }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder: { semanticModel: { datasets: ['sales', 'profit', 'cash'].map(id => ({ id, title: id, fields: [field] })) } } })
      await element.updateComplete
    })
    await editor.getByRole('button', { name: 'Add filter', exact: true }).click()
    expect(await editor.getByRole('menuitem', { name: 'Country', exact: true }).count()).toBe(1)
    await editor.getByRole('menuitem', { name: 'Country', exact: true }).click()
    expect(await editor.getByRole('button', { name: 'Add filter', exact: true }).getAttribute('aria-expanded')).toBe('false')
  } finally { await page.close() }
})

test('filter settings stay open when changing scope moves the card between groups', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    await editor.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder: { filters: [{ id: 'status-filter', label: 'Status', dimension: 'orders.status', controlType: 'text', required: false, readerEditable: true, targets: [], bindings: [{ id: 'status-filter', scope: 'report', targets: [] }] }] } })
      element.selectFilterDefinition('status-filter')
      await element.updateComplete
    })
    await editor.locator('.filter-settings summary').click()
    expect(await editor.locator('.filter-settings').evaluate((details: HTMLDetailsElement) => details.open)).toBe(true)
    expect(await editor.locator('.filter-editor select').inputValue()).toBe('text')
    await editor.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const filter = { ...element.builder.filters[0], bindings: [{ id: 'status-filter', scope: 'page', pageId: 'overview', targets: [] }] }
      mergePatch({ builder: { filters: [filter] } })
      await element.updateComplete
    })
    expect(await editor.locator('.filter-settings').evaluate((details: HTMLDetailsElement) => details.open)).toBe(true)
    expect(await editor.locator('.filter-editor select').inputValue()).toBe('text')
    await editor.locator('.filter-settings summary').click()
    expect(await editor.locator('.filter-settings').evaluate((details: HTMLDetailsElement) => details.open)).toBe(false)
  } finally { await page.close() }
})

for (const reference of visualReference.documents) test(`${reference.source} exposes applicable filter controls and sends scoped mutations`, async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (element: any, visualType: string) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const commands: any[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      const initial = JSON.parse(JSON.stringify(element.builder))
      const cases: Array<{ dataType: string; choices: string[] }> = [
        { dataType: 'string', choices: ['multiSelect', 'singleSelect', 'text'] },
        { dataType: 'boolean', choices: ['multiSelect', 'singleSelect'] },
        ...['integer', 'decimal', 'float'].map(dataType => ({ dataType, choices: ['numericRange', 'singleSelect', 'multiSelect'] })),
        ...['date', 'datetime', 'datetimetz'].map(dataType => ({ dataType, choices: ['relativePeriod', 'dateRange', 'singleSelect'] })),
      ]
      const fields = cases.map(({ dataType }) => ({ id: dataType, label: dataType, kind: 'dimension', dataType, roles: ['dimension'], canFilter: true }))
      let revision = initial.revision.number
      const reconcile = async (patch: Record<string, unknown>) => {
        mergePatch({ builder: { ...patch, revision: { id: `filter-matrix-${++revision}`, number: revision, contentHash: `sha256:${revision}` } } })
        await element.updateComplete
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
        await element.updateComplete
      }
      let controls = 0
      for (const visual of initial.visualCatalog.filter((visual: any) => visual.type === visualType)) {
        const pages = JSON.parse(JSON.stringify(initial.pages))
        pages[0].visuals[0].type = visual.type
        await reconcile({ pages, selectedPageId: pages[0].id, selectedVisualId: pages[0].visuals[0].id, filters: [], semanticModel: { id: 'orders', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields }] } })
        for (const { dataType, choices } of cases) {
          element.shadowRoot.querySelector('.filter-add-trigger').click()
          await element.updateComplete
          element.shadowRoot.querySelector(`.filter-add-option[data-field-id="${dataType}"]`).click()
          await element.updateComplete
          const add = commands.at(-1)
          if (add?.action !== 'add_filter' || add.fieldId !== dataType || add.dataset !== 'orders') throw new Error(`${visual.type}/${dataType}: missing Add filter command`)
          let filter = { id: 'test_filter', label: dataType, dimension: dataType, controlType: add.controlType, required: false, readerEditable: true, targets: [], bindings: [{ id: 'test_filter', scope: 'report', targets: [] }] }
          await reconcile({ filters: [filter] })
          const root = element.shadowRoot
          root.querySelector('.filter-card').click()
          await element.updateComplete
          root.querySelector('.filter-settings').open = true
          let select = root.querySelector('.filter-editor select') as HTMLSelectElement
          if (JSON.stringify([...select.options].map(option => option.value)) !== JSON.stringify(choices)) throw new Error(`${visual.type}/${dataType}: wrong controls`)
          controls += choices.length
          for (const control of choices) {
            if (control === filter.controlType) continue
            select = root.querySelector('.filter-editor select')
            select.value = control
            select.dispatchEvent(new Event('change', { bubbles: true }))
            if (commands.at(-1)?.action !== 'update_filter' || commands.at(-1)?.controlType !== control) throw new Error(`${visual.type}/${dataType}/${control}: missing update`)
            filter = { ...filter, controlType: control }
            await reconcile({ filters: [filter] })
          }
          for (const index of [1, 0, 2]) {
            root.querySelectorAll('.filter-scope-option input')[index].click()
            if (commands.at(-1)?.action !== 'set_filter_scope') throw new Error(`${visual.type}/${dataType}: missing scope`)
            await reconcile({ filters: [filter] })
          }
          root.querySelector('.filter-remove').click()
          if (commands.at(-1)?.action !== 'remove_filter') throw new Error(`${visual.type}/${dataType}: missing removal`)
          await reconcile({ filters: [] })
        }
      }
      return { controls, additions: commands.filter(command => command.action === 'add_filter').length, scopes: commands.filter(command => command.action === 'set_filter_scope').length, removals: commands.filter(command => command.action === 'remove_filter').length }
    }, reference.source)
    expect(result).toEqual({ controls: 23, additions: 8, scopes: 24, removals: 8 })
  } finally { await page.close() }
})

test('authoring request stays pending when a filter fetch finishes before its revision', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (e: any) => {
      e.setAttribute('data-on:lv-builder-command', 'void 0')
      e.emitCommand('assign_field', { fieldId: 'order_count', role: 'metric' })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: e } }))
      const pendingBeforeRevision = e.commandPending
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder: { revision: { ...e.builder.revision, id: 'field-assigned', number: e.builder.revision.number + 1 } } })
      await e.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: e } }))
      return { pendingBeforeRevision, pendingAfterRevision: e.commandPending }
    })
    expect(result).toEqual({ pendingBeforeRevision: true, pendingAfterRevision: false })
  } finally { await page.close() }
})

test('GridStack displays authoritative Arrange geometry after rebuilding and metadata patches', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const builder = JSON.parse(JSON.stringify(e.builder))
      builder.pages[0].visuals[0].placement = { col: 7, row: 3, colSpan: 6, rowSpan: 2 }
      builder.revision.id = 'arranged'
      mergePatch({ builder })
      await e.updateComplete
      const grid = e.gridStack
      const node = e.gridStack.getGridItems()[0].gridstackNode
      const geometry = { x: node.x, y: node.y, w: node.w, h: node.h }
      mergePatch({ builder: { title: 'Metadata change', revision: { ...builder.revision, id: 'metadata' } } })
      await e.updateComplete
      const after = e.gridStack.getGridItems()[0].gridstackNode
      return { geometry, after: { x: after.x, y: after.y, w: after.w, h: after.h }, retainedGrid: grid === e.gridStack }
    })
    expect(result.geometry).toEqual({ x: 6, y: 2, w: 6, h: 2 })
    expect(result.after).toEqual(result.geometry)
    expect(result.retainedGrid).toBe(true)
  } finally { await page.close() }
})

test('removed grid tiles stay removed when Undo and Redo restore the component list', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    const editor = page.locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const builder = JSON.parse(JSON.stringify(e.builder))
      const original = builder.pages[0].visuals[0]
      const added = { ...original, id: 'added-chart', placement: { col: 7, row: 1, colSpan: 6, rowSpan: 5 } }
      builder.pages[0].visuals = [original, added]
      mergePatch({ builder }); await e.updateComplete
      builder.pages[0].visuals = [original]
      mergePatch({ builder }); await e.updateComplete
      const afterUndo = [...e.shadowRoot.querySelectorAll('.canvas > .visual')].map((item: Element) => item.getAttribute('gs-id'))
      builder.pages[0].visuals = [original, added]
      mergePatch({ builder }); await e.updateComplete
      const afterRedo = [...e.shadowRoot.querySelectorAll('.canvas > .visual')].map((item: Element) => item.getAttribute('gs-id'))
      return { afterUndo, afterRedo }
    })
    expect(result.afterUndo).toEqual(['sales-chart'])
    expect(result.afterRedo.sort()).toEqual(['added-chart', 'sales-chart'])
  } finally { await page.close() }
})


test('Fix leaves complete filtered charts alone instead of treating loading data as missing fields', async () => {
  const page = await browser.newPage({viewport: {width: 1440, height: 900}})
  try {
    await page.goto(`${baseURL}/embed-host`)
    const editor = page.frameLocator('iframe').locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const result = await editor.evaluate(async (e: any) => {
      const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const builder = JSON.parse(JSON.stringify(e.builder))
      builder.preview.loading = true
      builder.pages[0].visuals[0].slots = [{id:'dimension-0', kind:'dimension', fieldId:'country', label:'Country'}, {id:'metric-0', kind:'metric', fieldId:'revenue', label:'Revenue'}]
      mergePatch({builder, builderVisuals: null})
      await e.updateComplete
      const before = JSON.stringify(e.builderFilterState)
      const commands: unknown[] = []
      e.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      e.arrangeVisuals()
      await e.updateComplete
      return {commands, message:e.fixVisualsMessage, unchanged:before===JSON.stringify(e.builderFilterState)}
    })
    expect(result.commands).toEqual([])
    expect(result.message).toBe('Visuals are ready. Your layout is unchanged.')
    expect(result.unchanged).toBe(true)
  } finally {await page.close()}
})

test('dragging an aggregate-only dimension creates a chart instead of an invalid records table', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const command = await page.locator('lv-dashboard-builder').evaluate(async (e: any) => {
      await e.updateComplete
      let command: unknown
      e.addEventListener('lv-builder-command', (event: CustomEvent) => {command=event.detail}, {once:true})
      e.createVisualFromField({id:'driver_order',label:'Driver order',kind:'dimension',dataType:'number',roles:['dimension']})
      return command
    })
    expect(command).toMatchObject({action:'add_visual',type:'bar',fieldId:'driver_order',role:'dimension'})
  } finally {await page.close()}
})

test('embedded preview page tabs select in place instead of reloading the builder', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/embed-host`)
    const builder = page.frameLocator('iframe[title="Chat builder"]').locator('lv-dashboard-builder')
    await builder.waitFor()
    const result = await builder.evaluate(async (element: any) => {
      element.pageBaseHref='/dashboards/demo/edit?draft=draft-7'
      await element.updateComplete
      let selection: any
      element.addEventListener('lv-builder-page-select',(event: CustomEvent)=>{selection=event.detail},{once:true})
      const link=element.shadowRoot.querySelector('.page-tab[href*="page=details"]')
      const prevented=!link.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true}))
      await element.updateComplete
      return {prevented, selection, pageId:element.selectedPage(element.builder)?.id}
    })
    expect(result.prevented).toBe(true)
    expect(result.selection).toMatchObject({pageId:'details'})
    expect(result.pageId).toBe('details')
  } finally {await page.close()}
})

test('Add filter stays inside its pane with light searchable rows and keyboard selection', async () => {
  const page = await browser.newPage({viewport:{width:1440,height:900}})
  try {
    await page.goto(baseURL)
    const editor=page.locator('lv-dashboard-builder')
    await editor.locator('.filter-add-trigger').waitFor()
    await editor.evaluate(async(element: any)=>{
      element.shadowRoot.querySelector('.filters-pane').style.width='170px'
      const builder=JSON.parse(JSON.stringify(element.builder))
      builder.filters=[]
      builder.semanticModel.datasets=[{id:'sales',title:'Sales',fields:[
        {id:'country',label:'Country',kind:'dimension',dataType:'string',roles:['dimension'],canFilter:true},
        {id:'long',label:'Customer region with a very long field name',kind:'dimension',dataType:'string',roles:['dimension'],canFilter:true},
      ]}]
      builder.pages[0].visuals[0].datasetId='sales'
      const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({builder});await element.updateComplete
      ;(window as any).filterCommands=[]
      element.addEventListener('lv-builder-command',(event: CustomEvent)=>(window as any).filterCommands.push(event.detail))
    })
    await page.getByRole('button',{name:'Add filter',exact:true}).click()
    const pane=(await editor.locator('.filters-pane').boundingBox())!, menu=(await editor.locator('.filter-add-menu').boundingBox())!
    expect(menu.x).toBeGreaterThanOrEqual(pane.x)
    expect(menu.x+menu.width).toBeLessThanOrEqual(pane.x+pane.width)
    const search=page.getByRole('searchbox',{name:'Search filter fields',exact:true})
    await search.fill('region')
    expect(await editor.getByRole('menuitem').count()).toBe(1)
    expect(await editor.getByRole('menuitem').evaluate(e=>getComputedStyle(e).borderTopWidth)).toBe('0px')
    await search.fill('no such field')
    await page.getByText('No matching fields',{exact:true}).waitFor()
    await search.press('Escape')
    expect(await page.getByRole('button',{name:'Add filter',exact:true}).getAttribute('aria-expanded')).toBe('false')
    await page.getByRole('button',{name:'Add filter',exact:true}).click()
    await search.fill('Country');await search.press('ArrowDown');await page.keyboard.press('Enter')
    expect(await page.evaluate(()=>(window as any).filterCommands.at(-1))).toMatchObject({action:'add_filter',fieldId:'country'})
    expect(await page.getByRole('button',{name:'Add filter',exact:true}).getAttribute('aria-expanded')).toBe('false')
  } finally {await page.close()}
})

test('builder Back retains the originating chat across reload URLs', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      element.backHref = '/chats/agentconv_origin'
      await element.updateComplete
    })
    expect(new URL(page.url()).searchParams.get('returnChat')).toBe('agentconv_origin')
    const back = page.getByRole('link', { name: 'Back to chat', exact: true })
    expect(await back.getAttribute('href')).toBe('/chats/agentconv_origin')
    await back.click()
    expect(new URL(page.url()).pathname).toBe('/chats/agentconv_origin')
  } finally { await page.close() }
})

test('embedded page tabs refresh selected-page previews without remounting the builder', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(`${baseURL}/embed-host`)
    const editor = page.frameLocator('iframe').locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    const envelope = await editor.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const builder = JSON.parse(JSON.stringify(element.builder))
      builder.pages[1].visuals = [{ ...builder.pages[0].visuals[0], slots: [
        { id: 'category', kind: 'dimension', fieldId: 'orders.status', label: 'Status' },
        { id: 'value', kind: 'metric', fieldId: 'orders.count', label: 'Orders' },
      ] }]
      mergePatch({ builder }); await element.updateComplete
      ;(window as any).retainedTabBuilder = element
      ;(window as any).retainedTabCanvas = element.shadowRoot.querySelector('.canvas')
      builder.selectedPageId = 'details'
      builder.preview.active = true
      return { builder, runtime: { servingStateId: 'generation-7' }, status: element.status }
    })
    const preview = governedBarPreviewEnvelope('page-details')
    preview.consumerIdentity = 'details/sales-chart'
    let requests = 0
    await page.route('**/*builderReceipt=1*', async route => {
      requests++
      expect(new URL(route.request().url()).searchParams.get('page')).toBe('details')
      expect(JSON.parse(new URL(route.request().url()).searchParams.get('builderRuntime')!).servingStateId).toBe('generation-7')
      await route.fulfill({ contentType: 'text/html', body: `<html><body><div id="chat-dashboard-receipt"></div><script>parent.postMessage(${JSON.stringify({ type: 'lv-builder-imported', envelope: { ...envelope, builderVisuals: { 'sales-chart': preview } }, agentContext: {} })}, location.origin)</script></body></html>` })
    })
    await editor.locator('.page-tab[data-page-id="details"]').click()
    await editor.locator('lv-visualization-host .renderer svg, lv-visualization-host .renderer canvas').first().waitFor({ timeout: 3000 })
    const state = await editor.evaluate((element: any) => ({
      sameBuilder: (window as any).retainedTabBuilder === element,
      sameCanvas: (window as any).retainedTabCanvas === element.shadowRoot.querySelector('.canvas'),
      selectedPage: element.selectedPage(element.builder)?.id,
      pending: element.commandPending,
      runtimePage: element.signal('runtime', {}).pageId,
    }))
    expect(requests).toBe(1)
    expect(state).toEqual({ sameBuilder: true, sameCanvas: false, selectedPage: 'details', pending: false, runtimePage: 'details' })
  } finally { await page.close() }
})

test('embedded builder relays table requests and publishes window changes with unchanged data revisions', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(`${baseURL}/embed-host`)
    const editor = page.frameLocator('iframe').locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    await page.evaluate(() => {
      ;(window as any).projections = []
      window.addEventListener('message', event => { if (event.data.type === 'lv-builder-saved') (window as any).projections.push(event.data) })
    })
    const envelope = windowedTablePreviewEnvelope()
    await editor.evaluate(async (e: any, envelope) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const dataState = { schemaVersion: 1, encoding: 'json', kind: 'windowed', specRevision: envelope.specRevision, dataRevision: envelope.dataRevision, generation: 1, payload: JSON.stringify(envelope.dataState) }
      mergePatch({ builderVisuals: { 'sales-chart': { ...envelope, dataState, servingStateID: 'generation-7', streamGeneration: 1, filterRevision: 0, interactionRevision: 0, consumerIdentity: 'overview/sales-chart' } } })
      e.requests = []
      e.addEventListener('lv-visualization-window-request', (event: CustomEvent) => e.requests.push(event.detail))
      await e.updateComplete
    }, envelope)
    await page.waitForFunction(() => (window as any).projections.some((p: any) => Object.values(p.visuals).some((v: any) => v.dataState.kind === 'windowed')))
    const request = { visualID: 'sales-chart', specRevision: 'table-spec', dataRevision: 1, requestSeq: 2, resetVersion: 1, start: 0, limit: 50, sort: [{ field: { dataset: 'primary', field: 'amount' }, direction: 'descending' }], blockID: 'all' }
    await page.evaluate(request => {
      const child = document.querySelector('iframe')!.contentWindow!
      child.postMessage({ type: 'lv-builder-visual-window', pageId: 'details', request }, location.origin)
      child.postMessage({ type: 'lv-builder-visual-window', pageId: 'overview', request }, location.origin)
    }, request)
    await page.waitForFunction(() => ((document.querySelector('iframe')!.contentDocument!.querySelector('lv-dashboard-builder') as any).requests.length > 0))
    expect(await editor.evaluate((e: any) => e.requests.filter((item: any) => item.resetVersion === 1))).toEqual([request])
    await editor.evaluate(async (e: any, request) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const base = e.signal('builderVisuals', {})['sales-chart']
      const state = JSON.parse(base.dataState.payload)
      state.resetVersion = request.resetVersion; state.sort = request.sort
      state.blocks = { a: { id: 'a', start: 0, rows: [[249]], requestSeq: request.requestSeq, resetVersion: request.resetVersion, sort: request.sort } }
      mergePatch({ builderVisuals: { 'window:generation-7:overview:0:sales-chart': { ...base, dataState: { ...base.dataState, payload: JSON.stringify(state) } } } })
      await e.updateComplete
    }, request)
    await page.waitForFunction(() => (window as any).projections.some((p: any) => Object.values(p.visuals).some((v: any) => v.dataState.resetVersion === 1 && v.dataState.blocks.a?.rows[0][0] === 249)))
    await editor.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      const slot = 'window:generation-7:overview:0:sales-chart'
      const previous = e.signal('builderVisuals', {})[slot]
      const state = JSON.parse(previous.dataState.payload)
      state.blocks = { b: { id: 'b', start: 150, rows: [[99]], requestSeq: 3, resetVersion: 1, sort: state.sort } }
      mergePatch({ builderVisuals: { [slot]: { ...previous, dataState: { ...previous.dataState, payload: JSON.stringify(state) } } } })
      await e.updateComplete
    })
    await page.waitForFunction(() => (window as any).projections.some((p: any) => Object.values(p.visuals).some((v: any) => v.dataState.blocks.b?.start === 150 && v.dataState.blocks.b.rows[0][0] === 99)))
  } finally { await page.close() }
})


test('embedded builder can explicitly close layout gaps without requesting field repair', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(`${baseURL}/embed-host`)
    const editor = page.frameLocator('iframe').locator('lv-dashboard-builder')
    await editor.locator('.field-results').waitFor()
    await editor.evaluate(async (e: any) => {
      const builder = JSON.parse(JSON.stringify(e.builder))
      const visual = builder.pages[0].visuals[0]
      builder.pages[0].visuals = ['kpi', 'kpi', 'kpi', 'line', 'pie', 'bar'].map((type, i) => ({ ...visual, id: `chart-${i}`, type, placement: { col: 1, row: 1 + i * 6, colSpan: 6, rowSpan: 5 } }))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ builder })
      e.testCommands = []
      e.addEventListener('lv-builder-command', (event: CustomEvent) => e.testCommands.push(event.detail))
      await e.updateComplete
    })
    await editor.getByLabel('More dashboard actions', {exact:true}).first().click()
    await editor.getByRole('button', {name:'Arrange visuals',exact:true}).click()
    const command = await editor.evaluate((e:any) => e.testCommands[0])
    expect(command.action).toBe('set_placements')
    expect(command.fillMissingFields).toBeUndefined()
    for (const row of new Set(command.placements.map((p:any) => p.placement.row))) {
      expect(command.placements.filter((p:any) => p.placement.row === row).reduce((width:number,p:any) => width + p.placement.columnSpan, 0)).toBe(12)
    }
  } finally { await page.close() }
})
