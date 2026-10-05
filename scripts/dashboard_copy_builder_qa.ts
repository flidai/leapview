import { expect, type Browser } from '@playwright/test'

export async function verifyDashboardCopyBuilder(browser: Browser, baseURL: string, storageState?: string): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 }, ...(storageState ? { storageState } : {}) })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    const response = await page.goto(new URL('/dashboards/dashboard:executive-sales/fork', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`dashboard copy form: status ${response?.status() ?? 'unknown'}`)
    await page.locator('form input[name="title"]').fill('QA dashboard copy')
    await page.locator('form input[name="slug"]').fill(`qa-dashboard-copy-${Date.now()}`)
    let streamStatus: number | undefined
    page.on('response', (reply) => {
      const url = new URL(reply.url())
      if (url.pathname === '/updates' && url.searchParams.get('route') === 'dashboard_builder') streamStatus = reply.status()
    })
    const editResponse = page.waitForResponse((reply) =>
      reply.request().method() === 'GET' && /^\/dashboards\/[^/]+\/edit$/.test(new URL(reply.url()).pathname),
    { timeout: 20_000 })
    await page.locator('form button[type="submit"]').click()
    const edit = await editResponse
    if (!edit.ok()) throw new Error(`copied dashboard edit: status ${edit.status()}`)
    await page.waitForURL(/\/dashboards\/[^/]+\/edit\?draft=/, { timeout: 20_000 })
    await expect.poll(() => streamStatus, { timeout: 20_000 }).toBe(200)
    await expect(page.getByText('Loading dashboard builder...')).toBeHidden({ timeout: 20_000 })
    await expect(page.locator('lv-dashboard-builder')).toBeVisible()
    if (errors.length) throw new Error(`dashboard copy builder page errors: ${errors.join('; ')}`)
  } finally {
    await page.close()
  }
}
