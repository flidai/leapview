import { expect, type Locator, type Page } from '@playwright/test'

type RouteStateOptions = {
  page: Page
  path: string
  root: string
  focusByTab: (page: Page, target: Locator, label: string, maximumTabs?: number) => Promise<void>
  scan: (state: string) => Promise<void>
}

// Exercise mounted controls without changing credentials, profile settings or
// pipeline state. The caller owns authentication, hydration, Axe and cleanup.
export async function verifyDevelopAdminRouteState({ page, path, root, focusByTab, scan }: RouteStateOptions): Promise<void> {
  const writes: string[] = []
  const recordWrite = (request: import('@playwright/test').Request) => {
    if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method())) {
      writes.push(`${request.method()} ${new URL(request.url()).pathname}`)
    }
  }
  page.on('request', recordWrite)
  try {
    await assertRouteFitsViewport(page, root, path)
    if (path === '/pipelines') {
      const runs = page.locator(root).getByRole('link', { name: 'Runs', exact: true })
      await focusByTab(page, runs, 'Pipeline Runs tab', 100)
      await page.keyboard.press('Enter')
      await page.waitForURL((url) => url.pathname === '/pipelines/runs')
      await page.waitForFunction(() => {
        const element = document.querySelector('lv-pipelines-page') as HTMLElement & { page?: { activeTab?: string } }
        return element?.page?.activeTab === 'runs'
      })
      await expect(page.locator(root).getByRole('link', { name: 'Runs', exact: true })).toHaveAttribute('aria-current', 'page')
      await assertRouteFitsViewport(page, root, '/pipelines/runs')
      await scan('keyboard-selected Runs tab')
    } else if (path === '/admin/profile') {
      const theme = page.locator('lv-personal-settings').getByRole('button', { name: /^Theme / })
      await focusByTab(page, theme, 'Profile theme picker', 100)
      await page.keyboard.press('Enter')
      await expect(theme).toHaveAttribute('aria-expanded', 'true')
      const list = page.getByRole('listbox', { name: 'Theme', exact: true })
      await expect(list.getByRole('option', { selected: true })).toBeFocused()
      await scan('keyboard-open theme picker')
      await page.keyboard.press('Escape')
      await expect(list).toHaveCount(0)
      await expect(theme).toHaveAttribute('aria-expanded', 'false')
      await expect(theme).toBeFocused()
      await scan('theme picker dismissed with focus restored')
    } else if (path === '/login') {
      // The managed QA fixture provisions local auth. A missing form is an
      // explicit prerequisite failure, never a silently skipped journey.
      const email = page.locator(root).getByRole('textbox', { name: 'Email', exact: true })
      const password = page.locator(root).getByLabel('Password', { exact: true })
      await focusByTab(page, email, 'Local login email')
      await page.keyboard.type('route-qa@example.invalid')
      await focusByTab(page, password, 'Local login password')
      await page.keyboard.type('non-secret-route-fixture')
      const reveal = page.locator(root).getByRole('button', { name: 'Show password', exact: true })
      await focusByTab(page, reveal, 'Password visibility control')
      await page.keyboard.press('Enter')
      await expect(password).toHaveAttribute('type', 'text')
      await expect(reveal).toHaveCount(0)
      const hide = page.locator(root).getByRole('button', { name: 'Hide password', exact: true })
      await expect(hide).toBeFocused()
      await scan('keyboard-revealed fixture password')
      await page.keyboard.press('Enter')
      await expect(password).toHaveAttribute('type', 'password')
      await expect(email).toHaveValue('route-qa@example.invalid')
      await expect(password).toHaveValue('non-secret-route-fixture')
      await expect(reveal).toBeFocused()
    }
    expect(writes, `${path}: inspection must not submit a product mutation`).toEqual([])
    const keyboard = ['/pipelines', '/admin/profile', '/login'].includes(path)
    console.log(`${path}: viewport${keyboard ? ' and keyboard' : ''} assertions passed at ${page.viewportSize()?.width}x${page.viewportSize()?.height}`)
  } finally {
    page.off('request', recordWrite)
  }
}

async function assertRouteFitsViewport(page: Page, root: string, path: string): Promise<void> {
  const bounds = await page.locator(root).evaluate((element) => ({
    viewport: window.innerWidth,
    document: document.documentElement.scrollWidth,
    left: element.getBoundingClientRect().left,
    right: element.getBoundingClientRect().right,
  }))
  expect(bounds.document, `${path}: document overflows the viewport`).toBeLessThanOrEqual(bounds.viewport + 1)
  expect(bounds.left, `${path}: route starts outside the viewport`).toBeGreaterThanOrEqual(-1)
  expect(bounds.right, `${path}: route extends beyond the viewport`).toBeLessThanOrEqual(bounds.viewport + 1)
}
