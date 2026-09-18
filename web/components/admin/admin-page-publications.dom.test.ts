import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('publications admin renders lifecycle controls and emits typed commands', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Publications', active: 'publications', headerTitle: 'Publications',
        headerDetail: 'Public dashboard lifecycle.',
        sidebar: { label: 'Admin', railLabel: 'Admin', ariaLabel: 'Admin navigation', storageKey: 'admin', activeId: 'publications', numbered: false, collapsible: false, items: [{ id: 'publications', title: 'Publications', href: '/admin/publications', active: true }] },
        publications: [{ projectId: 'visuals', name: 'website-showcase', dashboard: 'visual-showcase', defaultPage: 'overview', status: 'active', origins: ['https://leapview.dev'], generation: 'state-2', publicUrl: 'https://app.leapview.dev/public/dashboards/id', embedUrl: 'https://app.leapview.dev/embed/dashboards/id', iframeSnippet: '<iframe></iframe>', configuredAt: '2026-07-20', history: ['2026-07-20 · configured · owner'] }],
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-entity-list') as any
      await list.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-publication-command', (event: CustomEvent) => { detail = event.detail })
      const row = (element.shadowRoot as ShadowRoot).querySelector('.entity-list-table-row') as HTMLElement
      row.click()
      await element.updateComplete
      const buttons = Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('button')) as HTMLButtonElement[]
      buttons.find((button) => button.textContent?.trim() === 'Suspend')?.click()
      return {
        text: (element.shadowRoot as ShadowRoot).textContent.replace(/\s+/g, ' ').trim(),
        rows: (element.shadowRoot as ShadowRoot).querySelectorAll('.entity-list-table-row').length,
        drawer: Boolean((element.shadowRoot as ShadowRoot).querySelector('lv-drawer')),
        statusClass: (element.shadowRoot as ShadowRoot).querySelector('.entity-list-status')?.className,
        publicURL: (element.shadowRoot as ShadowRoot).querySelector('.publication-drawer-fact a')?.textContent?.trim(),
        history: (element.shadowRoot as ShadowRoot).querySelector('.publication-history')?.textContent?.trim(),
        detail,
      }
    })
    expect(state.rows).toBe(1)
    expect(state.drawer).toBe(true)
    expect(state.statusClass).toContain('is-success')
    expect(state.publicURL).toBe('https://app.leapview.dev/public/dashboards/id')
    expect(state.history).toContain('2026-07-20')
    expect(state.text).toContain('website-showcase')
    expect(state.text).toContain('Lifecycle history')
    expect(state.detail).toEqual({ publication: 'website-showcase', action: 'suspend' })
  } finally {
    await page.close()
  }
})
