import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

test('chat visual card opens a side panel with the chart and Save action', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    fixture.savedVisualRequest = null
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-thread'))
    await page.locator('lv-chat-page lv-chat-thread').waitFor()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const field = (id: string, role: string) => ({ id, role, dataType: role === 'metric' ? 'decimal' : 'string', nullable: false, label: id })
      mergePatch({
        agent: { transcript: [{
          id: 'tool-chart', kind: 'tool', name: 'query_visual', status: 'complete', conversationId: 'c1',
          argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['country'], metrics: ['revenue'] } } }),
          resultJson: JSON.stringify({ ok: true, type: 'bar', id: 'chart-1', datasetId: 'orders', semanticModelRef: { kind: 'semantic_model', id: 'sales' }, fields: [{ fieldId: 'sales.country', role: 'dimension', explorerFieldId: 'orders.country' }] }),
          artifact: { id: 'chart-1', type: 'bar', summary: 'Revenue by country' },
        }] },
        visuals: { 'chart-1': {
          schemaVersion: 14, visualID: 'chart-1', rendererID: 'echarts', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1,
          spec: { kind: 'cartesian', mark: 'bar', title: 'Revenue by country', datasets: [{ id: 'primary', fields: [field('label', 'dimension'), field('value', 'metric')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Revenue by country', description: 'Revenue' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
          dataState: { kind: 'inline', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['France', 42]], completeness: 'complete' }] },
          selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
        } },
      })
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelector('[data-visual-id="chart-1"]')))
    await page.locator('lv-chat-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-thread').shadowRoot.querySelector('[data-visual-id="chart-1"]').click()
    })
    const state = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const panel = element.shadowRoot.querySelector('lv-chat-visual-panel') as any
      await panel?.updateComplete
      return {
        open: Boolean(panel),
        title: panel?.shadowRoot.querySelector('h2')?.textContent,
        chart: panel?.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec?.kind,
        schemaVersion: panel?.shadowRoot.querySelector('lv-visual-artifact')?.payload?.schemaVersion,
        revisions: [panel?.payload?.specRevision, panel?.payload?.dataState?.specRevision, panel?.payload?.dataRevision, panel?.payload?.dataState?.dataRevision],
        save: Boolean(panel?.shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]')),
        hasExploreLink: Boolean(panel?.shadowRoot.querySelector('a[aria-label="Open visual in Data Explorer"]')),
        explorerHref: panel?.explorerHref,
        auditHref: panel?.auditHref,
        visualExplorerHref: panel?.shadowRoot.querySelector('lv-visual-artifact')?.explorerHref,
      }
    })
    expect(state.open).toBe(true)
    expect(state.title).toBe('Revenue by country')
    expect(state.chart).toBe('cartesian')
    expect(state.schemaVersion).toBe(14)
    expect(state.revisions).toEqual([`sha256:${'2'.repeat(64)}`, `sha256:${'2'.repeat(64)}`, 1, 1])
    expect(state.save).toBe(true)
    expect(state.hasExploreLink).toBe(false)
    expect(state.explorerHref).toContain('/explore?')
    expect(state.auditHref).toContain('/visuals/chart-1/explore')
    expect(state.visualExplorerHref).toBe(state.explorerHref)
    const latest = await page.locator('lv-chat-page').evaluate(async (chat: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const original = chat.agent.transcript[0]
      const input = JSON.parse(original.argumentsJson)
      input.visual.query.metrics = ['profit']
      const payload = chat.visuals['chart-1']
      mergePatch({ agent: { transcript: [original, { ...original, id: 'new-tool', runId: 'new-run', argumentsJson: JSON.stringify(input) }] },
        visuals: { 'chart-1': { ...payload, specRevision: `sha256:${'3'.repeat(64)}`, spec: { ...payload.spec, title: 'Profit by country' } } },
      })
      await chat.updateComplete
      const panel = chat.shadowRoot.querySelector('lv-chat-visual-panel')
      await panel.updateComplete
      return { explorerHref: panel.explorerHref, auditHref: panel.auditHref, title: panel.title, payloadTitle: panel.payload.spec.title }
    })
    expect(latest.payloadTitle).toBe('Profit by country')
    expect(latest.title).toBe('Profit by country')
    expect(latest.explorerHref).not.toBe(state.explorerHref)
    expect(JSON.parse(new URL(latest.explorerHref, fixture.baseURL).searchParams.get('state')!).metrics).toEqual([{ field: 'profit' }])
    expect(latest.auditHref).toBe('/chats/c1/visuals/chart-1/explore?run=new-run')
    state.explorerHref = latest.explorerHref
    const screenshotDir = process.env.LEAPVIEW_CHAT_SCREENSHOT_DIR
    if (screenshotDir) {
      await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.querySelector('lv-visual-artifact')?.shadowRoot?.querySelector('lv-visualization-host')?.shadowRoot?.querySelector('.renderer canvas')))
      await mkdir(screenshotDir, { recursive: true })
      await page.screenshot({ path: join(screenshotDir, 'chat-visual-panel-after.png'), fullPage: true })
    }
    await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('lv-chat-visual-panel').shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]').click())
    await page.waitForFunction(() => document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.textContent?.includes('Saved to Data Explorer.'))
    expect(JSON.parse(fixture.savedVisualRequest!.body)).toMatchObject({ title: 'Profit by country', explorerUrl: state.explorerHref })
    expect(fixture.savedVisualRequest!.csrf).toBe('test-csrf')
    await page.setViewportSize({ width: 390, height: 820 })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('.main')?.hasAttribute('inert')))
    const mobilePanel = await page.locator('lv-chat-page').evaluate((element: any) => {
      const panel = element.shadowRoot.querySelector('lv-chat-visual-panel') as HTMLElement
      const bounds = panel.getBoundingClientRect()
      return { x: bounds.x, width: bounds.width, modal: panel.shadowRoot?.querySelector('aside')?.getAttribute('aria-modal'), chatInert: element.shadowRoot.querySelector('.main')?.inert }
    })
    expect(mobilePanel).toEqual({ x: 0, width: 390, modal: 'true', chatInert: true })
    if (screenshotDir) await page.screenshot({ path: join(screenshotDir, 'chat-visual-panel-mobile-after.png'), fullPage: true })
    await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('lv-chat-visual-panel').shadowRoot.querySelector('[aria-label="Close visual details"]').click())
    expect(await page.locator('lv-chat-page').evaluate((element: any) => Boolean(element.shadowRoot.querySelector('lv-chat-visual-panel')))).toBe(false)
    await page.setViewportSize({ width: 1280, height: 820 })
    await page.getByRole('button', { name: 'Visuals (1)', exact: true }).click()
    const preview = page.locator('.preview-panel lv-visual-artifact')
    await preview.locator('lv-visualization-host').waitFor()
    expect(await preview.evaluate((artifact: any) => artifact.shadowRoot.querySelector('lv-visualization-host').exploreHref)).toBe(state.explorerHref)
    expect(await preview.locator('[slot="agent-action"]').count()).toBe(0)
    await page.evaluate(async () => {
      const chat = document.querySelector('lv-chat-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const item = chat.agent.transcript[0]
      const input = JSON.parse(item.argumentsJson)
      input.visual.query.type = 'records'
      mergePatch({ agent: { transcript: [{ ...item, runId: 'unsupported-run', argumentsJson: JSON.stringify(input) }] } })
    })
    await preview.getByRole('link', { name: 'View saved visual', exact: true }).waitFor()
    expect(await preview.getByRole('link', { name: 'View saved visual', exact: true }).getAttribute('href')).toBe('/chats/c1/visuals/chart-1/explore?run=unsupported-run')
    expect(await preview.evaluate((artifact: any) => artifact.shadowRoot.querySelector('lv-visualization-host').exploreHref)).toBe('')
  } finally {
    await page.close()
  }
})

test('Search Ask AI opens an unsent draft once and clears only its prompt fragment', async () => {
  const page = await fixture.browser.newPage()
  const requestsBefore = fixture.draftTurnRequests
  const prompt = 'Why did sales fall in Europe?'
  try {
    await page.addInitScript(() => window.history.replaceState({ fromSearch: 'retained-state' }, '', window.location.href))
    await page.goto(`${fixture.baseURL}/new?context=search#${new URLSearchParams({ prompt, source: 'search' })}`)
    const composer = page.locator('lv-chat-page lv-chat-composer')
    await page.waitForFunction(prompt => {
      const composer = document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-composer') as any
      return composer?.getDraft?.() === prompt && !window.location.hash.includes('prompt=')
    }, prompt)
    expect(await composer.locator('textarea').inputValue()).toBe(prompt)
    expect(new URL(page.url()).hash).toBe('#source=search')
    expect(new URL(page.url()).search).toBe('?context=search')
    expect(await page.evaluate(() => window.history.state)).toEqual({ fromSearch: 'retained-state' })
    await composer.locator('textarea').fill('My revised question')
    await page.locator('lv-chat-page').evaluate(async (chat: any) => { chat.requestUpdate(); await chat.updateComplete })
    expect(await composer.locator('textarea').inputValue()).toBe('My revised question')
    expect(fixture.draftTurnRequests).toBe(requestsBefore)
  } finally { await page.close() }
})
