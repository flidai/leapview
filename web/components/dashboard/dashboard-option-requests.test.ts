import { expect } from 'bun:test'
import type { Browser } from '@playwright/test'

export async function verifyDashboardOptionRequests(browser: Browser, baseURL: string) {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const stateFilter = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const seen: unknown[] = []
      element.addEventListener('lv-filter-options-request', (event: CustomEvent) => seen.push(event.detail))
      for (let index = 0; index < 2; index++) {
        element.dispatchEvent(new CustomEvent('lv-filter-options-needed', {
          bubbles: true,
          composed: true,
          detail: { bindingKey: 'fb_state', search: '', limit: 50 },
        }))
      }
      for (const detail of [
        { bindingKey: 'fb_state', search: 'S', limit: 50 },
        { bindingKey: 'fb_state', search: 'SP', limit: 50 },
        { bindingKey: 'fb_state', search: 'SP', cursor: 'next-page', limit: 50 },
      ]) element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { bubbles: true, composed: true, detail }))
      await element.updateComplete
      const originalPage = element.signal('filterOptionPages', {}).fb_state
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ filterOptionPages: { fb_state: { ...originalPage, requestGeneration: 3 } } })
      await element.updateComplete
      const lateSearchVisible = element.currentFilterOptionPages.fb_state !== undefined
      mergePatch({ filterOptionPages: { fb_state: { ...originalPage, requestGeneration: 4 } } })
      await element.updateComplete
      const currentSearchVisible = element.currentFilterOptionPages.fb_state?.requestGeneration === 4
      return {
        lateSearchVisible, currentSearchVisible,
        requests: seen,
        definition: element.signal('filterContract', {}).definitions?.state,
        binding: element.signal('filterContract', {}).bindings?.fb_state,
        page: originalPage,
        purchaseDateDefinition: element.signal('filterContract', {}).definitions?.purchase_date,
        purchaseDateBinding: element.signal('filterContract', {}).bindings?.fb_purchase_date,
      }
    })
    expect(stateFilter.lateSearchVisible).toBe(false)
    expect(stateFilter.currentSearchVisible).toBe(true)
    expect(stateFilter.requests).toHaveLength(4)
    expect(stateFilter.requests).toMatchObject([
      { search: '', requestGeneration: 1 }, { search: 'S', requestGeneration: 2 },
      { search: 'SP', requestGeneration: 3 }, { search: 'SP', cursor: 'next-page', requestGeneration: 4 },
    ])
    expect(stateFilter.definition).toMatchObject({
      field: 'sales_orders.state',
      options: { kind: 'distinct', limit: 50, values: [] },
    })
    expect(stateFilter.binding).toMatchObject({ selectionMode: 'multiple', maxSelectedValues: 50 })
    expect(stateFilter.page).toMatchObject({
      bindingKey: 'fb_state', complete: true,
      items: [{ label: 'SP', available: true }],
    })
    expect(stateFilter.purchaseDateDefinition).toMatchObject({
      field: 'sales_orders.purchase_date', valueKind: 'date',
      predicates: [{ kind: 'range', operators: [] }],
      options: { kind: 'none', limit: 0, values: [] },
    })
    expect(stateFilter.purchaseDateBinding).toMatchObject({ scope: 'report', default: { kind: 'unfiltered' } })
  } finally {
    await page.close()
  }
}
