import { expect, type Browser, type Locator, type Page } from '@playwright/test'

export type DataExplorerRouteQAContext = {
  browser: Browser
  baseURL: string
  collectBlockingConsoleMessages: (page: Page) => string[]
  assertNoBlockingConsoleMessages: (label: string, messages: string[]) => void
  focusByTab: (page: Page, target: Locator, label: string, maximumTabs?: number) => Promise<void>
}

export async function verifyDataExplorerRecoveryActions({
  browser,
  baseURL,
  collectBlockingConsoleMessages,
  assertNoBlockingConsoleMessages,
}: DataExplorerRouteQAContext): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)

  try {
    const response = await page.goto(new URL('/explore', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`/explore recovery: status ${response?.status() ?? 'unknown'}`)
    const explorer = page.locator('lv-data-explorer')
    await explorer.waitFor()
    // /explore opens in semantic-query mode. Preview recovery belongs to the
    // browse mode of the same canonical route, so switch the typed command
    // state before selecting a resource instead of clicking a hidden tree.
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: { command: { mode: 'browse' } } })
    })
    await expect(explorer.locator('.route')).not.toHaveClass(/semantic/)

    const preview = explorer.locator('lv-data-preview-table')
    if (!await preview.isVisible()) {
      const firstGroup = explorer.locator('details.resource-group').first()
      await firstGroup.locator(':scope > summary').click()
      const firstObject = firstGroup.locator('.object-button').first()
      await firstObject.waitFor({ state: 'visible' })
      await firstObject.click()
    }
    await preview.waitFor({ state: 'visible' })
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: { preview: { error: 'Qualification-injected preview failure.' } } })
    })

    const failure = preview.locator('[role="alert"]')
    await expect(failure).toContainText('Qualification-injected preview failure.')
    const retryRequest = page.waitForRequest((request) => new URL(request.url()).pathname === '/explore/command' && request.method() === 'POST')
    await failure.getByRole('button', { name: 'Retry', exact: true }).click()
    await retryRequest

    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: { preview: { error: 'Qualification-injected preview failure.' } } })
    })
    await expect(failure).toBeVisible()
    const resetRequest = page.waitForRequest((request) => new URL(request.url()).pathname === '/explore/command' && request.method() === 'POST')
    await failure.getByRole('button', { name: 'Reset view', exact: true }).click()
    const reset = await resetRequest
    if (!reset.postData()?.includes('resetVersion')) {
      throw new Error('/explore recovery reset did not send canonical reset state')
    }

    const semanticResponse = await page.goto(new URL('/explore', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!semanticResponse?.ok()) throw new Error(`/explore semantic recovery: status ${semanticResponse?.status() ?? 'unknown'}`)
    const semantic = await enterDataExplorerAnalyzeMode(page)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: { explore: { result: { error: 'Qualification-injected semantic failure.' } } } })
    })
    const semanticFailure = semantic.explorer.locator('.semantic-result [role="alert"]')
    await expect(semanticFailure).toContainText('Qualification-injected semantic failure.')
    const semanticRetryRequest = page.waitForRequest((request) => new URL(request.url()).pathname === '/explore/command' && request.method() === 'POST')
    const semanticRetryResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/explore/command' && response.request().method() === 'POST')
    await semanticFailure.getByRole('button', { name: 'Retry', exact: true }).click()
    const semanticRetry = await semanticRetryRequest
    await (await semanticRetryResponse).finished()
    if (!semanticRetry.postData()?.includes('"action":"run"')) {
      throw new Error('/explore semantic recovery retry did not send an explicit run action')
    }

    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: { explore: { result: { error: 'Qualification-injected semantic failure.' } } } })
    })
    await expect(semanticFailure).toBeVisible()
    const semanticResetRequest = page.waitForRequest((request) => new URL(request.url()).pathname === '/explore/command' && request.method() === 'POST')
    await semanticFailure.getByRole('button', { name: 'Reset query', exact: true }).click()
    const semanticReset = await semanticResetRequest
    const semanticResetBody = semanticReset.postData() ?? ''
    if (!semanticResetBody.includes('resetVersion')
      || !semanticResetBody.includes('"action":"configure"')
      || semanticResetBody.includes('"action":"run"')) {
      throw new Error('/explore semantic recovery reset did not send canonical reset and configure-only state')
    }
    assertNoBlockingConsoleMessages('data explorer recovery', messages)
  } finally {
    await page.close()
  }
}

async function enterDataExplorerAnalyzeMode(page: Page): Promise<{ explorer: Locator; controls: Locator }> {
  const explorer = page.locator('lv-data-explorer')
  await explorer.waitFor()
  const analyze = explorer.getByRole('button', { name: 'Analyze', exact: true })
  await analyze.waitFor({ state: 'visible' })
  if (await analyze.getAttribute('aria-pressed') !== 'true') {
    const commandRequest = page.waitForRequest((request) => new URL(request.url()).pathname === '/explore/command' && request.method() === 'POST')
    await analyze.click()
    await commandRequest
  }
  await expect(analyze).toHaveAttribute('aria-pressed', 'true')
  await expect(explorer.locator('.route')).toHaveClass(/semantic/)
  const controls = explorer.locator('lv-data-explorer-query-controls')
  await controls.waitFor({ state: 'visible' })
  return { explorer, controls }
}

export async function verifyDataExplorerKeyboardJourney({
  browser,
  baseURL,
  collectBlockingConsoleMessages,
  assertNoBlockingConsoleMessages,
  focusByTab,
}: DataExplorerRouteQAContext): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)
  await page.addInitScript(() => {
    localStorage.removeItem('leapview-sidebar-collapsed')
    localStorage.removeItem('leapview-area-last-insights')
    localStorage.removeItem('leapview-area-last-develop')
  })

  try {
    const response = await page.goto(new URL('/explore', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`keyboard accessibility /explore: status ${response?.status() ?? 'unknown'}`)
    const explorer = page.locator('lv-data-explorer')
    await explorer.waitFor()
    const rows = explorer.getByRole('button', { name: 'Rows', exact: true })
    const analyze = explorer.getByRole('button', { name: 'Analyze', exact: true })
    await expect(rows, 'Data Explorer must start in Rows mode').toHaveAttribute('aria-pressed', 'true')
    await focusByTab(page, analyze, 'Analyze mode button')
    await assertElementFocused(page, analyze, 'Analyze mode button')
    await page.keyboard.press('Enter')
    await expect(analyze, 'Enter must activate Analyze mode').toHaveAttribute('aria-pressed', 'true')
    await expect(explorer.locator('.route')).toHaveClass(/semantic/)
    await assertElementFocused(page, analyze, 'Analyze mode button after activation')

    const controls = explorer.locator('lv-data-explorer-query-controls')
    await controls.waitFor({ state: 'visible' })
    const queryConfig = controls.locator('details.query-config')
    const queryConfigSummary = queryConfig.locator(':scope > summary')
    await focusByTab(page, queryConfigSummary, 'Query configuration disclosure', 220)
    await assertElementFocused(page, queryConfigSummary, 'Query configuration disclosure')
    await page.keyboard.press('Enter')
    await expect(queryConfig, 'query configuration must close from its focused summary').not.toHaveAttribute('open', '')
    await page.keyboard.press('Enter')
    await expect(queryConfig, 'query configuration must reopen from its focused summary').toHaveAttribute('open', '')

    const firstField = controls.locator('button.field-button:not(:disabled)').first()
    if (await firstField.count() === 0) throw new Error('Analyze query builder rendered no selectable semantic fields')
    await focusByTab(page, firstField, 'first selectable semantic field', 220)
    await assertElementFocused(page, firstField, 'first selectable semantic field')
    await expect(firstField).toHaveAttribute('aria-pressed', 'false')
    await page.keyboard.press('Enter')
    await expect(firstField, 'Enter must select the focused semantic field').toHaveAttribute('aria-pressed', 'true')
    await assertElementFocused(page, firstField, 'selected semantic field')

    const run = explorer.getByRole('button', { name: 'Run', exact: true })
    await run.waitFor({ state: 'visible', timeout: 30_000 })
    await focusByTab(page, run, 'Analyze Run button', 220)
    await assertElementFocused(page, run, 'Analyze Run button')
    assertNoBlockingConsoleMessages('data explorer keyboard accessibility journey', messages)
  } finally {
    await page.close()
  }
}

export async function verifyDataExplorerResponsiveLayout({
  browser,
  baseURL,
  collectBlockingConsoleMessages,
  assertNoBlockingConsoleMessages,
}: DataExplorerRouteQAContext): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 600, height: 900 } })
  const messages = collectBlockingConsoleMessages(page)

  try {
    const response = await page.goto(new URL('/explore', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`responsive /explore: status ${response?.status() ?? 'unknown'}`)
    const { explorer, controls } = await enterDataExplorerAnalyzeMode(page)
    const filterAction = controls.locator('button.field-action').first()
    if (await filterAction.count() === 0) throw new Error('responsive Analyze query builder rendered no dimension filter action')
    await filterAction.click()
    const filterEditor = controls.getByRole('region', { name: 'Add filter', exact: true })
    await expect(filterEditor).toBeVisible()

    const layout = await page.evaluate(() => {
      const root = document.querySelector('lv-data-explorer') as HTMLElement & { shadowRoot: ShadowRoot }
      const route = root.shadowRoot?.querySelector('.route') as HTMLElement | null
      const explorer = root.shadowRoot?.querySelector('.explorer') as HTMLElement | null
      const browserResizer = root.shadowRoot?.querySelector('.browser-resizer') as HTMLElement | null
      const controls = root.shadowRoot?.querySelector('lv-data-explorer-query-controls') as HTMLElement & { shadowRoot: ShadowRoot } | null
      const filterEditor = controls?.shadowRoot?.querySelector('.filter-editor') as HTMLElement | null
      const targets = Array.from(controls?.shadowRoot?.querySelectorAll('button, input, select, summary') ?? [])
        .filter((element) => {
          const rect = (element as HTMLElement).getBoundingClientRect()
          return rect.width > 0 && rect.height > 0
        })
      const minimumTargetHeight = targets.reduce((minimum, element) => Math.min(minimum, (element as HTMLElement).getBoundingClientRect().height), Infinity)
      return {
        viewportWidth: window.innerWidth,
        documentScrollWidth: document.documentElement.scrollWidth,
        routeWidth: route?.getBoundingClientRect().width ?? 0,
        explorerWidth: explorer?.getBoundingClientRect().width ?? 0,
        explorerTracks: explorer ? getComputedStyle(explorer).gridTemplateColumns.trim().split(/\s+/).filter(Boolean).length : 0,
        browserResizerDisplay: browserResizer ? getComputedStyle(browserResizer).display : '',
        controlsWidth: controls?.getBoundingClientRect().width ?? 0,
        filterEditorWidth: filterEditor?.getBoundingClientRect().width ?? 0,
        filterEditorTracks: filterEditor ? getComputedStyle(filterEditor).gridTemplateColumns.trim().split(/\s+/).filter(Boolean).length : 0,
        minimumTargetHeight: Number.isFinite(minimumTargetHeight) ? minimumTargetHeight : 0,
      }
    })
    if (layout.documentScrollWidth > layout.viewportWidth + 1) {
      throw new Error(`/explore responsive layout overflows horizontally: ${JSON.stringify(layout)}`)
    }
    if (layout.routeWidth > layout.viewportWidth + 1 || layout.explorerWidth > layout.viewportWidth + 1) {
      throw new Error(`/explore responsive route exceeds viewport: ${JSON.stringify(layout)}`)
    }
    if (layout.explorerTracks !== 1 || layout.browserResizerDisplay !== 'none') {
      throw new Error(`/explore responsive layout did not collapse the browser rail: ${JSON.stringify(layout)}`)
    }
    if (layout.controlsWidth <= 0 || layout.filterEditorWidth > layout.controlsWidth + 1 || layout.filterEditorTracks !== 1) {
      throw new Error(`/explore responsive query controls did not stack within the viewport: ${JSON.stringify(layout)}`)
    }
    if (layout.minimumTargetHeight < 24) {
      throw new Error(`/explore responsive query controls rendered a target below 24px: ${JSON.stringify(layout)}`)
    }
    assertNoBlockingConsoleMessages('data explorer responsive layout', messages)
  } finally {
    await page.close()
  }
}

async function assertElementFocused(page: Page, target: Locator, label: string): Promise<void> {
  const focused = await target.evaluate((element) => {
    let active: Element | null = element.ownerDocument.activeElement
    while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
    return active === element
  })
  if (!focused) throw new Error(`${label} did not retain focus`)
}
