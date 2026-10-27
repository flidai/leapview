import { afterAll, beforeAll, expect, test } from 'bun:test'
import { chromium, type Browser } from '@playwright/test'
import { startSiteTestServer, type SiteTestServer } from './test_server'

// Interaction and polish checks for the public site shell. Kept apart from
// site.dom.test.ts so that file stays inside the engineering quality budget,
// and run as its own bun process by the ci:test:frontend:site task because each
// Playwright site file owns a browser and a site server.
let baseURL: string
let browser: Browser
let siteServer: SiteTestServer | undefined
const siteReadyTimeout = 60_000

beforeAll(async () => {
  const startupDeadline = Date.now() + siteReadyTimeout
  try {
    siteServer = await startSiteTestServer(0, startupDeadline)
    baseURL = siteServer.baseURL
    browser = await chromium.launch()
  } catch (error) {
    await siteServer?.stop()
    siteServer = undefined
    throw error
  }
}, siteReadyTimeout + 10_000)

afterAll(async () => {
  try {
    await browser?.close()
  } finally {
    await siteServer?.stop()
  }
})


test('mobile menu offers the hosted demo', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    const menu = page.locator('lv-site-mobile-menu')
    await menu.getByRole('button').click()
    const demo = menu.getByRole('link', { name: 'Try the demo', exact: true })
    expect(await demo.getAttribute('href')).toBe('https://demo.leapview.dev')
    expect(await demo.getAttribute('target')).toBe('_blank')
    expect(await demo.getAttribute('rel')).toContain('noopener')
  } finally { await page.close() }
})

test('documentation search closes with one Escape and ArrowDown enters the results', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/docs/guides/build/dashboard`)
    const search = page.locator('lv-site-search')
    await search.getByRole('button', { name: 'Search documentation' }).click()
    const input = search.locator('input[slot="input"]')
    await page.waitForFunction(() => document.activeElement?.matches('lv-site-search input[slot="input"]'))
    await input.fill('semantic relationships')
    const firstResult = search.locator('.results a').first()
    await firstResult.waitFor({ state: 'visible' })

    await page.keyboard.press('ArrowDown')
    expect(await firstResult.evaluate(el => el.matches(':focus'))).toBe(true)
    await page.keyboard.press('ArrowDown')
    expect(await search.locator('.results a').nth(1).evaluate(el => el.matches(':focus'))).toBe(true)
    await page.keyboard.press('ArrowUp')
    await page.keyboard.press('ArrowUp')
    expect(await input.evaluate(el => el.matches(':focus'))).toBe(true)

    await page.keyboard.press('Escape')
    await search.getByRole('dialog', { name: 'Search documentation' }).waitFor({ state: 'hidden' })
  } finally { await page.close() }
})

test('keyboard focus on a copy-code button shows a visible focus ring', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/docs/guides/build/dashboard`)
    await page.waitForFunction(() => Boolean(document.querySelector('.site-docs-article lv-code-block .code-block-copy')))
    const copy = page.locator('.site-docs-article lv-code-block .code-block-copy').first()
    await copy.focus()
    await page.keyboard.press('Shift+Tab')
    await page.keyboard.press('Tab')
    expect(await copy.evaluate(el => el.matches(':focus-visible'))).toBe(true)
    const outline = await copy.evaluate(el => {
      const style = getComputedStyle(el)
      return { style: style.outlineStyle, width: parseFloat(style.outlineWidth) }
    })
    expect(outline.style).not.toBe('none')
    expect(outline.width).toBeGreaterThanOrEqual(2)
  } finally { await page.close() }
})

test('footer links meet the minimum target size', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(baseURL)
    const heights = await page.locator('.site-footer nav a').evaluateAll(links => links.map(link => link.getBoundingClientRect().height))
    expect(heights.length).toBeGreaterThan(0)
    for (const height of heights) expect(height).toBeGreaterThanOrEqual(24)
  } finally { await page.close() }
})

test('every integration node stays clickable above the orbit heading', async () => {
  const context = await browser.newContext({ reducedMotion: 'no-preference', viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  try {
    await page.goto(baseURL)
    await page.locator('#connections').scrollIntoViewIfNeeded()
    const center = page.locator('.orbit-center')
    expect(await center.evaluate(el => getComputedStyle(el).pointerEvents)).toBe('none')
    expect(await page.locator('.orbit-cta').evaluate(el => getComputedStyle(el).pointerEvents)).toBe('auto')
    await page.getByRole('button', { name: 'Pause animation', exact: true }).click()
    const covered = await page.locator('.orbit-node').evaluateAll(nodes => nodes.flatMap(node => {
      const box = node.getBoundingClientRect()
      const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)
      return hit && !node.contains(hit) && hit.closest('.orbit-center') ? [node.getAttribute('aria-label')] : []
    }))
    expect(covered).toEqual([])
  } finally { await context.close() }
}, 30000)
