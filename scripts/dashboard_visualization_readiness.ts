import type { Page } from '@playwright/test'

/** Match one canonical dashboard update stream without accepting another route or duplicate identity parameters. */
export function isDashboardUpdateURL(value: string, dashboard: string, page: string): boolean {
  try {
    const url = new URL(value)
    const exactly = (name: string, expected: string): boolean => {
      const values = url.searchParams.getAll(name)
      return values.length === 1 && values[0] === expected
    }
    return url.pathname === '/updates'
      && exactly('route', 'dashboard')
      && exactly('dashboard', dashboard)
      && exactly('page', page)
  } catch {
    return false
  }
}

/** Force only the requested dashboard renderers before renderer-specific QA. */
export async function ensureDashboardVisualizationsMounted(page: Page, visualIDs: string[] = []): Promise<void> {
  await page.waitForFunction((expected) => {
    const hosts = Array.from(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
    return expected.length > 0 ? expected.every((visualID) => hosts.some((host) => host.envelope?.visualID === visualID)) : hosts.length > 0
  }, visualIDs, { timeout: 30_000 })
  await page.locator('lv-dashboard-page').evaluate(async (dashboard: any, expected: string[]) => {
    const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
    await Promise.all(hosts.filter((host) => expected.length === 0 || expected.includes(host.envelope?.visualID)).map((host) => host.ensureMounted()))
  }, visualIDs)
}
