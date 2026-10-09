import { expect, test } from 'bun:test'
import type { Page } from '@playwright/test'
import { hostBrowserFixture, hostBrowserStep } from './host-browser.test-fixture'

const fixture = hostBrowserFixture()

async function waitForPanelState(page: Page, stage: 'failure' | 'visibility' | 'recovery'): Promise<void> {
  try {
    await page.waitForFunction(stage => {
      const host = (window as any).__lvHiddenPanel.host
      if (stage === 'failure') return host.shadowRoot.querySelector('[role="alert"]')
      if (stage === 'visibility') return host.rendererHasSize
      return host.presented && !host.shadowRoot.querySelector('[role="alert"]') && !host.shadowRoot.querySelector('[data-visualization-loading]')
    }, stage, { timeout: 5_000 })
  } catch (error) {
    const state = await page.evaluate(() => {
      const { panel, host } = (window as any).__lvHiddenPanel
      return { hidden: panel.hidden, error: host.error, applying: host.applying, pending: Boolean(host.pendingApply), queued: host.applyQueued, presented: host.presented, hasSize: host.rendererHasSize, rendererChildren: host.shadowRoot.querySelector('.renderer').childElementCount }
    }).catch(() => 'browser unavailable')
    throw new Error(`Selected chart ${stage} did not settle: ${JSON.stringify(state)}`, { cause: error })
  }
}

test('deferred initial invalid visual data shows an error and recovers when replaced', async () => {
  const page = await fixture.newPage()
  try {
    await hostBrowserStep('navigate loading fixture', page.goto(fixture.baseURL, { waitUntil: 'domcontentloaded', timeout: 5_000 }))
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
  } finally { await hostBrowserStep('close loading-test page', page.close(), 2_000) }
})

test('a stalled renderer reaches a retryable error and the selected visual can recover', async () => {
  const page = await fixture.newPage()
  let releaseRenderer!: () => void
  let rendererRequested!: () => void
  const blocked = new Promise<void>(resolve => { releaseRenderer = resolve })
  const requested = new Promise<void>(resolve => { rendererRequested = resolve })
  try {
    await hostBrowserStep('intercept renderer module', page.route('**/chunks/echarts-*.js', async route => {
      rendererRequested()
      await blocked
      await route.continue().catch(error => { if (!page.isClosed()) throw error })
    }))
    await hostBrowserStep('navigate loading fixture', page.goto(fixture.baseURL, { waitUntil: 'domcontentloaded', timeout: 5_000 }))
    await hostBrowserStep('load source envelope', page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_kpi?.envelope, undefined, { timeout: 5_000 }))
    await hostBrowserStep('present source KPI', page.waitForFunction(() => (window as any).__lvSourceHosts.orders_kpi.presented, undefined, { timeout: 5_000 }))
    await hostBrowserStep('create stalled visual', page.evaluate(() => {
      const original = window.setTimeout.bind(window)
      let captured = false
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        if (timeout === 30_000 && !captured && typeof handler === 'function') {
          captured = true
          ;(window as any).__expireStalledPreparation = () => handler(...args)
        }
        return original(handler, timeout, ...args)
      }) as typeof window.setTimeout
      const host = document.createElement('lv-visualization-host') as any
      host.style.cssText = 'display:block;width:600px;height:320px'
      host.envelope = JSON.parse(JSON.stringify((window as any).__lvSourceHosts.orders_chart.envelope))
      document.body.append(host)
      ;(window as any).__lvStalledVisual = host
    }))
    await hostBrowserStep('request renderer module', requested)
    // Start the timeout assertion only after the renderer is actually blocked.
    await hostBrowserStep('expire blocked preparation', page.evaluate(() => (window as any).__expireStalledPreparation()))
    await hostBrowserStep('show preparation timeout', page.waitForFunction(() => (window as any).__lvStalledVisual.shadowRoot.querySelector('[data-visualization-retry]'), undefined, { timeout: 5_000 }))
    const pending = await page.evaluate(() => {
      const host = (window as any).__lvStalledVisual
      return { error: host.shadowRoot.querySelector('[role="alert"]')?.textContent, loading: Boolean(host.shadowRoot.querySelector('[data-visualization-loading]')), busy: host.applying }
    })
    expect(pending.error).toContain('took too long')
    expect(pending.loading).toBe(false)
    expect(pending.busy).toBe(false)
    releaseRenderer()
    await page.evaluate(() => (window as any).__lvStalledVisual.shadowRoot.querySelector('[data-visualization-retry]').click())
    await hostBrowserStep('recover blocked renderer', page.waitForFunction(() => {
      const host = (window as any).__lvStalledVisual
      return host.presented && !host.shadowRoot.querySelector('[role="alert"]') && !host.shadowRoot.querySelector('[data-visualization-loading]')
    }, undefined, { timeout: 5_000 }))
  } finally { releaseRenderer(); await hostBrowserStep('close loading-test page', page.close(), 2_000) }
}, 20_000)


test('a selected chart recovers when its panel becomes visible after first-frame failure', async () => {
  const page = await fixture.newPage()
  try {
    await hostBrowserStep('navigate loading fixture', page.goto(fixture.baseURL, { waitUntil: 'domcontentloaded', timeout: 5_000 }))
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
    await waitForPanelState(page, 'failure')
    await page.evaluate(() => { (window as any).__lvHiddenPanel.panel.hidden = false })
    await waitForPanelState(page, 'visibility')
    await waitForPanelState(page, 'recovery')
    const mounted = await page.evaluate(() => (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('.renderer').childElementCount)
    expect(mounted).toBeGreaterThan(0)
  } finally { await hostBrowserStep('close loading-test page', page.close(), 2_000) }
}, 20_000)

for (const recovery of ['visibility', 'retry'] as const) test(`a selected chart queues ${recovery} recovery while its failed apply is settling`, async () => {
  const page = await fixture.newPage()
  try {
    await hostBrowserStep('navigate loading fixture', page.goto(fixture.baseURL, { waitUntil: 'domcontentloaded', timeout: 5_000 }))
    await page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_chart?.envelope)
    await page.evaluate((recovery) => {
      const original = window.setTimeout.bind(window)
      let expired = false
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        // Expire the first readiness deadline before ECharts emits its frame.
        // Keep the failed apply pending across a recovery request deterministically.
        if (timeout === 5_000 && !expired && typeof handler === 'function') {
          expired = true
          handler(...args)
          return 0
        }
        return original(handler, timeout, ...args)
      }) as typeof window.setTimeout
      const panel = document.createElement('section')
      panel.hidden = recovery === 'visibility'
      const host = document.createElement('lv-visualization-host') as any
      host.style.cssText = 'display:block;width:600px;height:320px'
      host.envelope = structuredClone((window as any).__lvSourceHosts.orders_chart.envelope)
      const apply = host.applyEnvelope.bind(host)
      let held = false
      host.applyEnvelope = async () => {
        await apply()
        if (host.error && !held) {
          held = true
          await new Promise<void>(resolve => { (window as any).__lvReleaseFailedApply = resolve })
        }
      }
      panel.append(host)
      document.body.append(panel)
      ;(window as any).__lvHiddenPanel = { panel, host }
    }, recovery)
    await page.waitForFunction(() => (window as any).__lvReleaseFailedApply && (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('[role="alert"]'), undefined, { timeout: 5_000 })
    if (recovery === 'visibility') {
      await page.evaluate(() => { (window as any).__lvHiddenPanel.panel.hidden = false })
      await waitForPanelState(page, 'visibility')
    } else {
      await page.evaluate(() => (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('[data-visualization-retry]').click())
    }
    expect(await page.evaluate(() => (window as any).__lvHiddenPanel.host.applyQueued)).toBe(true)
    await page.evaluate(() => (window as any).__lvReleaseFailedApply())
    await waitForPanelState(page, 'recovery')
    const mounted = await page.evaluate(() => (window as any).__lvHiddenPanel.host.shadowRoot.querySelector('.renderer').childElementCount)
    expect(mounted).toBeGreaterThan(0)
  } finally { await hostBrowserStep('close loading-test page', page.close(), 2_000) }
}, 20_000)

test('a late timed-out mount cannot overwrite a recovered visual', async () => {
  const page = await fixture.newPage()
  try {
    await page.route('**/chunks/echarts-*.js', route => route.fulfill({ contentType: 'text/javascript', body: `export const adapter = { async mount(container) {
      window.__lateMountStarted = true;
      window.__expirePreparation();
      await new Promise(resolve => { window.__releaseLateMount = resolve });
      container.replaceChildren(document.createTextNode('expired mount'));
      window.__lateMountFinished = true;
      return { update(){}, resize(){}, snapshot(){}, dispose(){container.replaceChildren()} };
    } };` }))
    await hostBrowserStep('navigate loading fixture', page.goto(fixture.baseURL, { waitUntil: 'domcontentloaded', timeout: 5_000 }))
    await page.waitForFunction(() => (window as any).__lvSourceHosts?.orders_kpi?.presented)
    await page.evaluate(() => {
      const original = window.setTimeout.bind(window)
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
        if (timeout === 30_000 && typeof handler === 'function') (window as any).__expirePreparation = () => handler(...args)
        return original(handler, timeout, ...args)
      }) as typeof window.setTimeout
      const host = document.createElement('lv-visualization-host') as any
      host.style.cssText = 'display:block;width:600px;height:320px'
      host.envelope = structuredClone((window as any).__lvSourceHosts.orders_chart.envelope)
      document.body.append(host)
      ;(window as any).__lvLateMountHost = host
    })
    await page.waitForFunction(() => (window as any).__lateMountStarted && (window as any).__lvLateMountHost.shadowRoot.querySelector('[data-visualization-retry]'))
    await page.evaluate(async () => {
      const host = (window as any).__lvLateMountHost
      host.envelope = structuredClone((window as any).__lvSourceHosts.orders_kpi.envelope)
      await host.ensureMounted()
      await host.updateComplete
    })
    const recovered = await page.evaluate(() => (window as any).__lvLateMountHost.shadowRoot.querySelector('.renderer').textContent)
    expect(recovered.length).toBeGreaterThan(0)
    await page.evaluate(() => (window as any).__releaseLateMount())
    await page.waitForFunction(() => (window as any).__lateMountFinished)
    expect(await page.evaluate(() => (window as any).__lvLateMountHost.shadowRoot.querySelector('.renderer').textContent)).toBe(recovered)
  } finally { await hostBrowserStep('close loading-test page', page.close(), 2_000) }
}, 20_000)
