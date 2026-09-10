import type { Page } from '@playwright/test'

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
