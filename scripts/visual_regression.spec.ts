import { expect, test, type Page } from '@playwright/test'

const states = [
  {
    name: 'executive-sales-overview',
    path: '/dashboards/dashboard:executive-sales/pages/overview',
    heading: 'Executive Sales',
  },
  {
    name: 'visual-showcase-overview',
    path: '/dashboards/dashboard:visual-showcase/pages/overview',
    heading: 'Visual Showcase',
  },
  {
    name: 'visual-showcase-tables',
    path: '/dashboards/dashboard:visual-showcase/pages/tables',
    heading: 'Visual Showcase',
  },
] as const

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'compact', width: 600, height: 900 },
] as const

const modes = ['light', 'dark'] as const

for (const state of states) {
  for (const viewport of viewports) {
    for (const mode of modes) {
      test.describe(`${state.name} / ${viewport.name} / ${mode}`, () => {
        test.use({
          viewport: { width: viewport.width, height: viewport.height },
          colorScheme: mode,
        })

        test('matches the reviewed baseline', async ({ page, baseURL }) => {
          await page.addInitScript((colorMode) => {
            localStorage.setItem('leapview-color-mode', colorMode)
            localStorage.removeItem('leapview-sidebar-collapsed')
          }, mode)
          await openStableDashboard(page, new URL(state.path, baseURL!).toString(), state.heading)
          const screenshot = await page.screenshot({ animations: 'disabled', caret: 'hide', scale: 'css' })
          const isDenseDesktopTable = viewport.name === 'desktop' && state.name === 'visual-showcase-tables'
          const isDesktop = viewport.name === 'desktop'
          expect(screenshot).toMatchSnapshot(`${state.name}-${viewport.name}-${mode}.png`, {
            // Chromium/Skia produces two equivalent antialias variants for dense
            // desktop SVG and text edges. Keep the allowance scoped to those states.
            maxDiffPixelRatio: isDenseDesktopTable ? 0.009 : isDesktop ? 0.003 : 0.001,
            threshold: isDesktop ? 0.5 : 0.2,
          })
        })
      })
    }
  }
}

async function openStableDashboard(page: Page, url: string, heading: string): Promise<void> {
  const response = await page.goto(url, { waitUntil: 'domcontentloaded' })
  expect(response?.ok(), `${url} should return a successful response`).toBe(true)
  await page
    .getByRole('navigation', { name: 'Breadcrumb' })
    .getByText(heading, { exact: true })
    .waitFor()
  await page.waitForFunction(() => {
    const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & {
      status?: { loading?: boolean }
      updateComplete?: Promise<unknown>
      shadowRoot: ShadowRoot
    }
    if (!dashboard?.shadowRoot || dashboard.status?.loading !== false) return false
    const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as Array<HTMLElement & {
      envelope?: { status?: { kind?: string } }
      shadowRoot: ShadowRoot
      updateComplete?: Promise<unknown>
    }>
    return hosts.length > 0 && hosts.every((host) => {
      const status = host.envelope?.status?.kind
      const renderer = host.shadowRoot?.querySelector('.renderer')
      return (status === 'ready' || status === 'partial') && (renderer?.childElementCount ?? 0) > 0
    })
  }, undefined, { timeout: 60_000 })
  await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
    await dashboard.updateComplete
    const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
    await Promise.all(hosts.map((host) => host.updateComplete))
    const fontSample = 'LeapView dashboard table 0123456789'
    await Promise.all([
      document.fonts.load('400 16px "Inter Variable"', fontSample),
      document.fonts.load('600 16px "Inter Variable"', fontSample),
    ])
    await document.fonts.ready
    if (!document.fonts.check('400 16px "Inter Variable"', fontSample)
      || !document.fonts.check('600 16px "Inter Variable"', fontSample)) {
      throw new Error('Inter Variable did not finish loading before visual capture')
    }
    await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  })
}
