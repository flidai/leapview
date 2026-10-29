import { expect, test } from 'bun:test'
import { hostBrowserFixture } from './host-browser.test-fixture'

const fixture = hostBrowserFixture()

test('chart visuals offer a download image action and non-image visuals omit it', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => Boolean((window as any).__lvSourceHosts))
    const state = await page.evaluate(async () => {
      const actions: string[] = []
      window.addEventListener('lv-visual-action', (event) => actions.push((event as CustomEvent).detail.action))
      const mount = async (id: string, envelope: unknown) => {
        const host = document.createElement('lv-visualization-host') as any
        host.id = id
        host.envelope = structuredClone(envelope)
        document.body.append(host)
        await host.updateComplete
        return host
      }
      const chart = await mount('download-chart', { ...(window as any).__lvSourceHosts.orders_chart.envelope, status: { kind: 'ready' } })
      const kpi = await mount('download-kpi', (window as any).__lvSourceHosts.orders_kpi.envelope)
      const menu = (host: any): HTMLElement[] => [...host.shadowRoot.querySelectorAll('[role="menuitem"]')]
      const labels = (host: any) => menu(host).map((item) => item.textContent?.trim())
      const item = menu(chart).find((candidate) => candidate.textContent?.includes('Download image'))
      item?.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
      return { chart: labels(chart), kpi: labels(kpi), actions }
    })
    expect(state.chart).toContain('Download image')
    expect(state.kpi).not.toContain('Download image')
    expect(state.actions).toEqual(['download-image'])
  } finally { await page.close() }
})
