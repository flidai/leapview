import { expect, type Browser } from '@playwright/test'
import { uuidv7 } from '../web/components/shared/command'

export async function verifyDashboardCopyBuilder(browser: Browser, baseURL: string, storageState?: string): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 }, ...(storageState ? { storageState } : {}) })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    const response = await page.goto(new URL('/dashboards/dashboard:executive-sales/fork', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`dashboard copy form: status ${response?.status() ?? 'unknown'}`)
    await page.locator('form input[name="title"]').fill('QA dashboard copy')
    await page.locator('form input[name="slug"]').fill(`qa-dashboard-copy-${Date.now()}`)
    const stream = page.waitForResponse((reply) => {
      const url = new URL(reply.url())
      return url.pathname === '/updates' && url.searchParams.get('route') === 'dashboard_builder'
    }, { timeout: 20_000 })
    await page.locator('form button[type="submit"]').click()
    await page.waitForURL(/\/dashboards\/[^/]+\/edit\?draft=/, { timeout: 20_000 })
    const reply = await stream
    if (reply.status() !== 200) throw new Error(`copied dashboard builder stream: status ${reply.status()}`)
    await expect(page.getByText('Loading dashboard builder...')).toBeHidden({ timeout: 20_000 })
    await expect(page.locator('lv-dashboard-builder')).toBeVisible()
    if (errors.length) throw new Error(`dashboard copy builder page errors: ${errors.join('; ')}`)
  } finally {
    try {
      const match = new URL(page.url()).pathname.match(/^\/dashboards\/([^/]+)\/edit$/)
      if (match) {
        const csrfToken = await page.locator('meta[name="csrf-token"]').getAttribute('content')
        if (!csrfToken) throw new Error('copied dashboard cleanup: missing CSRF token')
        const deletion = await page.context().request.post(new URL(`/dashboards/${match[1]}/delete`, baseURL).toString(), {
          form: { 'gorilla.csrf.Token': csrfToken, idempotencyKey: uuidv7() },
          maxRedirects: 0,
        })
        if (deletion.status() !== 303) throw new Error(`copied dashboard cleanup: status ${deletion.status()}`)
      }
    } finally {
      await page.close()
    }
  }
}
