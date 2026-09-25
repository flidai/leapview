import { afterAll, beforeAll, expect, test } from 'bun:test'
import AxeBuilder from '@axe-core/playwright'
import { chromium, type Browser } from '@playwright/test'
import { startSiteTestServer, type SiteTestServer } from './test_server'

const sitePort = 30000 + (process.pid % 10000)
const baseURL = `http://127.0.0.1:${sitePort}`
let browser: Browser
let siteServer: SiteTestServer | undefined
const siteReadyTimeout = 60_000

beforeAll(async () => {
  const startupDeadline = Date.now() + siteReadyTimeout
  try {
    console.error('compliance test startup: site build start')
    siteServer = await startSiteTestServer(sitePort, startupDeadline)
    console.error('compliance test startup: site build done')
    await waitForSite(siteServer.process, startupDeadline)
    console.error('compliance test startup: site ready')
    browser = await chromium.launch()
    console.error('compliance test startup: browser ready')
  } catch (error) {
    await siteServer?.stop()
    siteServer = undefined
    throw error
  }
}, siteReadyTimeout + 10_000)

afterAll(async () => {
  console.error('compliance test cleanup: browser close start')
  try {
    await browser?.close()
    console.error('compliance test cleanup: browser close done')
  } finally {
    console.error('compliance test cleanup: site stop start')
    await siteServer?.stop()
    console.error('compliance test cleanup: site stop done')
  }
})

test('compliance page shows distinct, bounded assurance categories', async () => {
  const page = await browser.newPage()
  try {
    const response = await page.goto(`${baseURL}/compliance`)
    expect(response?.status()).toBe(200)
    expect(await page.title()).toBe('Compliance & Security — LeapView')
    expect(await page.getByRole('heading', { level: 1 }).allTextContents()).toEqual(['Compliance & Security'])
    for (const heading of [
      'Current assurance state',
      'Implemented capabilities',
      'Qualification-tested capabilities',
      'Pending verification & approval',
      'Certifications & regulatory status',
      'Documentation & security reporting',
    ]) {
      expect(await page.getByRole('heading', { level: 2, name: heading }).count()).toBe(1)
    }
    expect(await page.getByText('Pending approval', { exact: true }).count()).toBeGreaterThan(0)
    expect(await page.getByText('Not currently claimed', { exact: true }).count()).toBe(1)
    expect(await page.getByText('ISO/IEC 27001 certification is not currently claimed.').count()).toBe(1)
    expect(await page.locator('time[datetime="2026-09-24"]').count()).toBe(1)
    expect(await page.getByRole('link', { name: 'Report a security issue privately' }).getAttribute('href'))
      .toBe('https://github.com/flidai/leapview/security/advisories/new')
    const copy = await page.locator('#main-content').innerText()
    for (const unsupported of ['GDPR compliant', 'ISO certified', 'fully compliant', 'production-ready', '24/7 support', '99.9% availability']) {
      expect(copy).not.toContain(unsupported)
    }
  } finally {
    await page.close()
  }
})

test('compliance page remains readable and navigable on desktop and mobile', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/compliance`)
    for (const width of [320, 390, 768, 1280]) {
      await page.setViewportSize({ width, height: 900 })
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false)
      expect(await page.locator('.site-header').count()).toBe(1)
      expect(await page.locator('.site-footer').count()).toBe(1)
      expect(await page.locator('#main-content').isVisible()).toBe(true)
    }
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.keyboard.press('Tab')
    expect(await page.locator('.skip-link').evaluate((link) => link === document.activeElement)).toBe(true)
    await page.keyboard.press('Enter')
    expect(new URL(page.url()).hash).toBe('#main-content')
    await page.getByRole('navigation', { name: 'On this page' }).getByRole('link', { name: 'Pending' }).click()
    expect(new URL(page.url()).hash).toBe('#pending')
    await page.setViewportSize({ width: 390, height: 900 })
    await page.getByRole('button', { name: 'Open site navigation' }).click()
    expect(await page.getByRole('navigation', { name: 'Site navigation' }).getByRole('link', { name: 'Compliance' }).count()).toBe(1)
  } finally {
    await page.close()
  }
})

test('compliance content passes the public-site accessibility audit', async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 900 } })
  const page = await context.newPage()
  try {
    await page.goto(`${baseURL}/compliance`)
    // The shared PageSpec currently nests the existing site footer in <main>.
    // Keep the page-wide scan, but do not expand this FAI-991 change into a
    // cross-site landmark refactor.
    const results = await new AxeBuilder({ page }).disableRules(['landmark-contentinfo-is-top-level']).analyze()
    expect(results.violations).toEqual([])
  } finally {
    await page.close()
    await context.close()
  }
})

async function waitForSite(siteProcess: Bun.Subprocess, deadline: number): Promise<void> {
  while (Date.now() < deadline) {
    if (siteProcess.exitCode !== null) {
      throw new Error(`LeapView site exited before becoming ready (code ${siteProcess.exitCode})`)
    }
    try {
      const response = await fetch(baseURL)
      if (response.ok) return
    } catch {
      // The directly spawned site is still binding its listener.
    }
    await Bun.sleep(100)
  }
  throw new Error('LeapView site did not become ready')
}
