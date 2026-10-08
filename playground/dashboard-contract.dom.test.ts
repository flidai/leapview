import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from 'bun:test'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { startTestPlayground } from './test-server'

let server: Awaited<ReturnType<typeof startTestPlayground>>
let browser: Browser
let page: Page
let errors: string[]
let unexpectedRequests: string[]

beforeAll(async () => {
  server = await startTestPlayground()
  browser = await chromium.launch()
}, 60000)
afterAll(async () => { await browser?.close(); await server?.stop(true) })
beforeEach(async () => {
  errors = []
  unexpectedRequests = []
  page = await browser.newPage({ baseURL: server.url.href, viewport: { width: 1440, height: 1100 }, reducedMotion: 'reduce' })
  page.setDefaultTimeout(7000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`) })
  page.on('request', request => {
    const url = new URL(request.url())
    if (['blob:', 'data:'].includes(url.protocol)) return
    if (url.origin !== server.url.origin || !['/', '/index.html', '/__playground/events'].includes(url.pathname) && !url.pathname.startsWith('/assets/') && !url.pathname.startsWith('/static/')) unexpectedRequests.push(request.url())
  })
  await page.goto(`${server.url}#recipes/dashboard-contract`)
  await browserExpect(page.locator('playground-dashboard-contract')).toBeVisible()
})
afterEach(async () => {
  await page?.close()
  expect(errors).toEqual([])
  expect(unexpectedRequests).toEqual([])
})

const example = () => page.locator('playground-dashboard-contract')
const compiler = () => example().getByRole('region', { name: 'LeapView compiler check', exact: true })
const sourceBadge = () => example().locator('.source-pane > .pane-heading .badge')

test('schema rules switch without changing the chosen document or edited source', async () => {
  const host = example()
  await host.getByText('Compare with other schema', {exact:true}).click()
  await host.getByLabel('Document scenario', { exact: true }).selectOption('zero-span')
  const source = await host.evaluate((element: any) => element.getExampleCode())
  await browserExpect(sourceBadge()).toHaveText('Zero column span')
  await host.getByLabel('Schema version', { exact: true }).selectOption('before')
  expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(source)
  await browserExpect(host.getByLabel('Document scenario', { exact: true })).toHaveValue('zero-span')
  await browserExpect(host.getByRole('region', { name: 'Baseline schema validation' }).locator('.status')).toHaveText('Accepted')
  await browserExpect(host.getByRole('region', { name: 'Tightened schema validation' }).locator('.status')).toHaveText('Rejected')
  const edited = source + '\n# local note\n'
  await host.evaluate(async (element: any, value) => element.restoreExampleState({ version: 'before', scenario: 'zero-span', source: value }), edited)
  await browserExpect(sourceBadge()).toHaveText('Zero column span · Edited')
  await host.getByLabel('Schema version', { exact: true }).selectOption('after')
  expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(edited)
  await browserExpect(sourceBadge()).toHaveText('Zero column span · Edited')
  await host.evaluate(async (element: any, source) => element.restoreExampleState({ version: 'after', scenario: 'zero-span', source }), source)
  await browserExpect(sourceBadge()).toHaveText('Zero column span')
})

test('real compiler evidence distinguishes valid shape from the original semantic error', async () => {
  await example().getByLabel('Document scenario', { exact: true }).selectOption('original-guide')
  await browserExpect(example().getByRole('region', { name: 'Tightened schema validation' }).locator('.status')).toHaveText('Accepted')
  await browserExpect(compiler().locator('.status')).toHaveText('Rejected')
  await browserExpect(compiler()).toContainText('purchase_month')
  await browserExpect(compiler()).toContainText('Recorded local run')
  await example().getByLabel('Document scenario', { exact: true }).selectOption('corrected-monthly')
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  await browserExpect(compiler()).toContainText('purchase_date')
  await browserExpect(compiler()).toContainText('month')
  await browserExpect(compiler()).toContainText('purchase_month')
})

test('editing invalidates recorded compiler evidence until exact fixture bytes are restored', async () => {
  const host = example()
  await browserExpect(host.getByLabel('Document scenario', { exact: true })).toHaveValue('corrected-monthly')
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide')
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  const source = await host.evaluate((element: any) => element.getExampleCode())
  const textbox = host.getByRole('textbox', { name: 'Dashboard YAML source', exact: true })
  await browserExpect(textbox).toBeVisible()
  await textbox.focus()
  await textbox.press('ControlOrMeta+End')
  await page.keyboard.insertText('\n# editor change\n')
  await browserExpect(compiler()).toContainText('Compiler result unavailable for edited YAML')
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide · Edited')
  const editedSnapshot = await host.evaluate((element: any) => element.getExampleState())
  await browserExpect(compiler().locator('.status')).toHaveCount(0)
  await browserExpect(host.getByRole('region', { name: 'Tightened schema validation' }).locator('.status')).toHaveText('Accepted')
  // Monaco can adjust indentation while inserting text; Reset restores the exact recorded bytes.
  await host.getByRole('button', { name: 'Reset YAML', exact: true }).click()
  expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(source)
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide')
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  const snapshot = await host.evaluate((element: any) => element.getExampleState())
  await host.getByLabel('Document scenario', { exact: true }).selectOption('out-of-grid')
  await browserExpect(compiler().locator('.status')).toHaveText('Rejected')
  await host.evaluate(async (element: any, value) => element.restoreExampleState(value), snapshot)
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide')
  await host.evaluate(async (element: any, value) => element.restoreExampleState(value), editedSnapshot)
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide · Edited')
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: async (value: string) => { (window as any).copiedContractLink = value },
    } })
  })
  await page.getByRole('button', { name: 'Copy link', exact: true }).click()
  const link = await page.evaluate(() => (window as any).copiedContractLink as string)
  expect(JSON.parse(new URL(link).searchParams.get('state')!).example).toEqual(editedSnapshot)
  await page.goto(link)
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide · Edited')
  expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(editedSnapshot.source)
  await host.getByRole('button', { name: 'Reset YAML', exact: true }).click()
  await browserExpect(sourceBadge()).toHaveText('Corrected monthly guide')
})

test('expanded compiler intent can be focused and scrolled with the keyboard', async () => {
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  await compiler().getByText('Full verified query and layout', { exact: true }).click()
  const json = compiler().getByRole('region', { name: 'Verified query and layout JSON', exact: true })
  await browserExpect(json).toHaveAttribute('tabindex', '0')
  await json.focus()
  await browserExpect(json).toBeFocused()
  await json.press('ControlOrMeta+End')
  await browserExpect.poll(() => json.evaluate(element => element.scrollTop)).toBeGreaterThan(0)
})

test('reset and rule changes keep one editor and a stable compiler result without pending flicker', async () => {
  const host = example()
  await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  await browserExpect(host.locator('lv-code-editor .monaco-editor')).toBeVisible()
  await host.locator('lv-code-editor').evaluate((element: any) => {
    ;(window as any).contractEditor = element.editor
    ;(window as any).contractNavigations = performance.getEntriesByType('navigation').length
  })
  await host.evaluate((element: any) => {
    ;(window as any).pendingFlashes = []
    const record = () => {
      const text = element.shadowRoot.querySelector('[aria-label="LeapView compiler check"]').textContent
      if (text.includes('Matching this YAML')) (window as any).pendingFlashes.push(text)
    }
    new MutationObserver(record).observe(element.shadowRoot, {subtree:true,childList:true,characterData:true})
  })
  for (let i = 0; i < 3; i++) {
    await host.getByLabel('Document scenario', {exact:true}).selectOption('original-guide')
    await browserExpect(compiler().locator('.status')).toHaveText('Rejected')
    await host.getByLabel('Document scenario', {exact:true}).selectOption('corrected-monthly')
    await host.getByRole('button', {name:'Reset YAML',exact:true}).click()
    await host.getByLabel('Schema version', {exact:true}).selectOption(i % 2 ? 'after' : 'before')
    await browserExpect(compiler().locator('.status')).toHaveText('Accepted')
  }
  expect(await page.evaluate(() => (window as any).pendingFlashes)).toEqual([])
  expect(await host.locator('lv-code-editor').evaluate((element: any) => element.editor === (window as any).contractEditor)).toBe(true)
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(1)
})

test('the default review shows the selected rules and keeps unrelated or duplicate content out of the way', async () => {
  const host = example()
  await browserExpect(host.getByRole('region',{name:'Tightened schema validation'})).toBeVisible()
  await browserExpect(host.getByRole('region',{name:'Baseline schema validation',includeHidden:true})).not.toBeVisible()
  await browserExpect(host.locator('lv-visualization-host')).toHaveCount(0)
  await browserExpect(host.getByRole('heading',{name:'Document references',exact:true})).toHaveCount(0)
  await host.getByText('Document references',{exact:true}).click()
  await browserExpect(host).toContainText('dashboard:executive-sales')
  await host.getByLabel('Document scenario',{exact:true}).selectOption('zero-span')
  await browserExpect(host.locator('.selected .status')).toHaveText('Rejected')
  await host.getByLabel('Schema version',{exact:true}).selectOption('before')
  await browserExpect(host.locator('.selected .status')).toHaveText('Accepted')
})

test('every recorded scenario remains usable under both schema selections', async () => {
  const host = example()
  const scenarios = await host.getByLabel('Document scenario',{exact:true}).locator('option').evaluateAll(options => options.map(option => (option as HTMLOptionElement).value))
  for (const scenario of scenarios) {
    await host.getByLabel('Document scenario',{exact:true}).selectOption(scenario)
    const source = await host.evaluate((element: any) => element.getExampleCode())
    for (const version of ['before','after']) {
      await host.getByLabel('Schema version',{exact:true}).selectOption(version)
      await browserExpect(compiler().locator('.status')).toHaveText(/Accepted|Rejected/)
      expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(source)
      expect(await host.locator('lv-code-editor').evaluate((element: any) => element.value)).toBe(source)
    }
  }
})

test('theme, size, preview and review tools preserve authored YAML without reloading', async () => {
  const host = example()
  const source = await host.evaluate((element: any) => element.getExampleCode()) + '\n# preserve this edit\n'
  await host.evaluate(async (element: any, source) => element.restoreExampleState({source}),source)
  await browserExpect(host.getByRole('textbox',{name:'Dashboard YAML source',exact:true})).toBeVisible()
  await page.getByRole('button',{name:'Switch to dark mode',exact:true}).click()
  await page.getByLabel('Preview width',{exact:true}).selectOption('360')
  await page.getByRole('button',{name:'Preview',exact:true}).click()
  await page.getByRole('button',{name:'Exit preview',exact:true}).click()
  expect(await host.evaluate((element: any) => element.getExampleCode())).toBe(source)
  const review = page.locator('playground-review-tools')
  await review.locator('summary').first().click()
  await review.getByRole('button',{name:'Copy component code',exact:true}).click()
  await browserExpect(review.getByLabel('Current component code',{exact:true})).toHaveText(source)
  await review.getByRole('button',{name:'Pin comparison',exact:true}).click()
  const url = new URL((await review.locator('iframe').getAttribute('src'))!)
  expect(JSON.parse(url.searchParams.get('state')!).example.source).toBe(source)
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(1)
})

test('copying empty authored YAML preserves its exact payload and explains the empty source', async () => {
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: async (value: string) => { (window as any).copiedContractSource = value },
    } })
  })
  const textbox = example().getByRole('textbox', { name: 'Dashboard YAML source', exact: true })
  await browserExpect(textbox).toBeVisible()
  await textbox.focus()
  await textbox.press('ControlOrMeta+A')
  await textbox.press('Backspace')
  await browserExpect.poll(() => example().evaluate((element: any) => element.getExampleCode())).toBe('')
  const review = page.locator('playground-review-tools')
  await review.locator('summary').first().click()
  await review.getByRole('button', { name: 'Copy component code', exact: true }).click()
  expect(await page.evaluate(() => (window as any).copiedContractSource)).toBe('')
  await browserExpect(review.getByLabel('Current component code', { exact: true })).toBeVisible()
  await browserExpect(review.getByLabel('Current component code', { exact: true })).toHaveText('')
  await browserExpect(review).toContainText('The current source is empty.')
})


test('large invalid YAML stays responsive and diagnostic disclosures render on demand', async () => {
  const host = example()
  await browserExpect(host.locator('lv-code-editor .monaco-editor')).toBeVisible()
  const { parse } = await import('yaml')
  const document = parse(await host.evaluate((element: any) => element.getExampleCode()))
  document.spec.pages[0].components = Array(2500).fill(null)
  const source = JSON.stringify(document)
  expect(source.length).toBeLessThan(20000)
  const elapsed = await host.evaluate(async (element: any, source) => {
    const start = performance.now()
    await element.restoreExampleState({ source })
    return performance.now() - start
  }, source)
  expect(elapsed).toBeLessThan(1000)
  const selected = host.getByRole('region', { name: 'Tightened schema validation' })
  await browserExpect(selected.locator('.status')).toHaveText('Rejected')
  await browserExpect(host.locator('.issues li')).toHaveCount(3)
  await browserExpect(host.getByRole('region', { name: 'Baseline schema validation', includeHidden: true })).toHaveCount(0)
  await selected.locator('.more-issues > summary').click()
  await browserExpect(selected.locator('.issues li')).toHaveCount(103)
  await selected.getByRole('button', { name: 'Show next 100 diagnostics', exact: true }).click()
  await browserExpect(selected.locator('.issues li')).toHaveCount(203)
  await selected.locator('.more-issues > summary').click()
  await browserExpect(selected.locator('.issues li')).toHaveCount(3)
  await host.locator('.comparison > summary').click()
  const baseline = host.getByRole('region', { name: 'Baseline schema validation' })
  await browserExpect(baseline.locator('.status')).toHaveText('Rejected')
  await browserExpect(host.locator('.issues li')).toHaveCount(6)
  await host.getByRole('button', { name: 'Reset YAML', exact: true }).click()
  await browserExpect(selected.locator('.status')).toHaveText('Accepted')
  expect(await host.evaluate((element: any) => element.getExampleCode())).not.toBe(source)
})
