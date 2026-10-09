import { expect, test } from 'bun:test'
import { hostBrowserFixture } from './host-browser.test-fixture'

const fixture = hostBrowserFixture()

test('deferred initial invalid visual data shows an error and recovers when replaced', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_kpi?.envelope)
    for (const boundaryFailure of [true, false]) {
      const result = await page.evaluate(async (boundaryFailure) => {
        const valid = JSON.parse(JSON.stringify((window as any).__lvSourceHosts.orders_kpi.envelope))
        const host = document.createElement('lv-visualization-host') as any
        host.deferMount = true
        const invalid = JSON.parse(JSON.stringify(valid))
        if (boundaryFailure) invalid.dataRevision = -1
        else delete invalid.spec.accessibility
        host.envelope = invalid
        document.body.append(host)
        await host.updateComplete
        while (host.pendingEnvelopeValidation) await host.pendingEnvelopeValidation
        await host.updateComplete
        const error = host.shadowRoot.querySelector('[role="alert"]')?.textContent ?? ''
        const loading = Boolean(host.shadowRoot.querySelector('[data-visualization-loading]'))
        host.envelope = valid
        await host.ensureMounted()
        await host.updateComplete
        const recovered = !host.shadowRoot.querySelector('[role="alert"]') && !host.shadowRoot.querySelector('[data-visualization-loading]')
        host.remove()
        return { error, loading, recovered }
      }, boundaryFailure)
      expect(result.error).toContain('invalid visualization envelope')
      expect(result.loading).toBe(false)
      expect(result.recovered).toBe(true)
    }
  } finally { await page.close() }
})

test('a stalled renderer reaches a retryable error and the selected visual can recover', async () => {
  const page = await fixture.browser.newPage()
  let releaseRenderer!: () => void
  let rendererRequested!: () => void
  const blocked = new Promise<void>(resolve => { releaseRenderer = resolve })
  const requested = new Promise<void>(resolve => { rendererRequested = resolve })
  try {
    await page.route('**/chunks/echarts-*.js', async route => {
      rendererRequested()
      await blocked
      await route.continue()
    })
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_kpi?.envelope)
    await page.waitForFunction(() => !(window as any).__lvSourceHosts.orders_kpi.shadowRoot.querySelector('[data-visualization-loading]'))
    await page.evaluate(() => {
      const original = window.setTimeout.bind(window)
      let shortened = false
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        if (timeout === 30_000 && !shortened) { shortened = true; timeout = 150 }
        return original(handler, timeout, ...args)
      }) as typeof window.setTimeout
      const host = document.createElement('lv-visualization-host') as any
      host.style.cssText = 'display:block;width:600px;height:320px'
      host.envelope = JSON.parse(JSON.stringify((window as any).__lvSourceHosts.orders_chart.envelope))
      document.body.append(host)
      ;(window as any).__lvStalledVisual = host
    })
    await requested
    await page.waitForFunction(() => (window as any).__lvStalledVisual.shadowRoot.querySelector('[data-visualization-retry]'))
    const pending = await page.evaluate(() => {
      const host = (window as any).__lvStalledVisual
      return { error: host.shadowRoot.querySelector('[role="alert"]')?.textContent, loading: Boolean(host.shadowRoot.querySelector('[data-visualization-loading]')), busy: host.applying }
    })
    expect(pending.error).toContain('took too long')
    expect(pending.loading).toBe(false)
    expect(pending.busy).toBe(false)
    releaseRenderer()
    await page.evaluate(() => (window as any).__lvStalledVisual.shadowRoot.querySelector('[data-visualization-retry]').click())
    await page.waitForFunction(() => {
      const host = (window as any).__lvStalledVisual
      return host.presented && !host.shadowRoot.querySelector('[role="alert"]') && !host.shadowRoot.querySelector('[data-visualization-loading]')
    })
  } finally { releaseRenderer(); await page.close() }
}, 20_000)


test('a selected chart recovers when its panel becomes visible after first-frame failure', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_chart?.envelope)
    await page.evaluate(() => {
      const original = window.setTimeout.bind(window)
      let expired = false
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        // Expire the first readiness deadline before ECharts emits its frame.
        // This reproduces a panel hidden during preparation deterministically.
        if (timeout === 5_000 && !expired && typeof handler === 'function') {
          expired = true
          handler(...args)
          return 0
        }
        return original(handler, timeout, ...args)
      }) as typeof window.setTimeout
      const panel = document.createElement('section')
      panel.hidden = true
      const host = document.createElement('lv-visualization-host') as any
      host.style.cssText = 'display:block;width:600px;height:320px'
      host.envelope = structuredClone((window as any).__lvSourceHosts.orders_chart.envelope)
      panel.append(host)
      document.body.append(panel)
      ;(window as any).__lvHiddenPanel = { panel, host }
    })
    await page.waitForFunction(() => (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('[role="alert"]'))
    await page.evaluate(() => { (window as any).__lvHiddenPanel.panel.hidden = false })
    await page.waitForFunction(() => {
      const host = (window as any).__lvHiddenPanel.host
      return host.presented && !host.shadowRoot.querySelector('[role="alert"]') && !host.shadowRoot.querySelector('[data-visualization-loading]')
    })
    const mounted = await page.evaluate(() => (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('.renderer').childElementCount)
    expect(mounted).toBeGreaterThan(0)
  } finally { await page.close() }
}, 20_000)
