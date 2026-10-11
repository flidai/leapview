import { expect, test } from 'bun:test'
import type { Browser, Page } from '@playwright/test'

export function registerRapidSelectionTests(getEnvironment: () => { browser: Browser; baseURL: string }): void {
  async function prepareRapidSelection(page: Page, priorError = '') {
    await page.goto(getEnvironment().baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    // Keep the module in a browser handle and observe settlement from the runner.
    // Separate synchronous actions make each phase attributable without one
    // long async CDP evaluation whose returned promise can be collected.
    const module = await page.evaluateHandle(() => import('/static/vendor/datastar-1.0.2.js?v=dev'))
    await module.evaluate((module: any, priorError: string) => module.mergePatch({
      interactionSelections: [], interactionRevision: 0,
      status: { generation: 3, refreshId: 'refresh-3', loading: false, progressPercent: 100, error: priorError },
    }), priorError)
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-dashboard-page') as any
      return element && !element.isUpdatePending && element.signal('interactionRevision', -1) === 0
        && element.shadowRoot.querySelector('lv-visualization-host')
    })
    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.rapidTransported = []
      element.addEventListener('lv-interaction-select', (event: CustomEvent) => element.rapidTransported.push(structuredClone(event.detail)))
    })
    return module
  }

  async function dispatchRapidSelections(page: Page, values: string[], action: 'set' | 'replace' = 'replace') {
    return page.locator('lv-dashboard-page').evaluate((element: any, { values, action }) => {
      const source = Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host'))
        .find((host: any) => host.envelope?.visualID === 'orders_chart') as HTMLElement
      for (const value of values) source.dispatchEvent(new CustomEvent('lv-interaction-select', {
        bubbles: true, composed: true, detail: { sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          action, toggle: action === 'set', mappings: [{ field: 'orders.status', dataset: 'orders', value, label: value }] },
      }))
      return structuredClone(element.rapidTransported)
    }, { values, action })
  }

  async function waitRapidCanonical(page: Page, generation: number) {
    await page.waitForFunction((generation) => {
      const element = document.querySelector('lv-dashboard-page') as any
      return element && !element.isUpdatePending && element.signal('status', {}).generation === generation
    }, generation)
  }

  test('rapid selection waits for canonical acceptance before transporting the next stamped intent', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      const module = await prepareRapidSelection(page)
      const before = await dispatchRapidSelections(page, ['delivered', 'shipped'])
      expect(before).toHaveLength(1)
      expect(before[0]).toMatchObject({ interactionRevision: 0, mappings: [{ value: 'delivered' }] })
      await module.evaluate((module: any) => module.mergePatch({
        interactionRevision: 1,
        interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          entries: [{ mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] }],
        status: { generation: 4, refreshId: 'refresh-4', loading: true, progressPercent: 0 },
      }))
      await waitRapidCanonical(page, 4)
      const result = await page.locator('lv-dashboard-page').evaluate((element: any) => ({
        transported: structuredClone(element.rapidTransported),
        selection: (Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host'))
          .find((host: any) => host.envelope?.visualID === 'orders_chart') as any).envelope.selection,
      }))
      expect(result.transported).toHaveLength(2)
      expect(result.transported[1]).toMatchObject({ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
        specRevision: 'sha256:' + '2'.repeat(64), dataRevision: 1, servingStateID: 'serving-test', filterRevision: 0,
        interactionRevision: 1, mappings: [{ field: 'orders.status', dataset: 'orders', value: 'shipped' }],
      })
      expect(result.selection).toEqual([{ datum: { dataset: 'primary', dataRevision: 1, identity: { label: 'shipped' } }, label: 'shipped' }])
    } finally { await page.close() }
  })

  for (const invalidation of ['different-selection', 'revision-jump', 'failed-command', 'filter-change', 'data-change', 'spec-change', 'serving-change', 'page-change', 'clear', 'disconnect'] as const) {
    test('rapid selection cancels pending transport on ' + invalidation, async () => {
      const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
      try {
        const module = await prepareRapidSelection(page)
        await dispatchRapidSelections(page, ['delivered', 'shipped'])
        await module.evaluate((module: any, invalidation: string) => {
          const accepted = { interactionRevision: 1,
            interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
              entries: [{ mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] }],
            status: { generation: 4, refreshId: 'refresh-4', loading: true, progressPercent: 0, error: '' },
          }
          if (invalidation === 'different-selection') accepted.interactionSelections[0].entries[0].mappings[0].value = 'unrelated'
          if (invalidation === 'revision-jump') accepted.interactionRevision = 2
          if (invalidation === 'failed-command') accepted.status.error = 'Selection was rejected'
          if (invalidation === 'filter-change') module.mergePatch({ filterState: { revision: 1 } })
          if (invalidation === 'data-change') module.mergePatch({ visuals: { orders_chart: { dataRevision: 2 } } })
          if (invalidation === 'spec-change') module.mergePatch({ visuals: { orders_chart: { specRevision: 'sha256:' + '9'.repeat(64) } } })
          if (invalidation === 'serving-change') module.mergePatch({ runtime: { servingStateId: 'serving-other' } })
          if (invalidation === 'page-change') module.mergePatch({ page: { pageId: 'details' } })
          if (invalidation === 'clear') document.querySelector('lv-dashboard-page')!.dispatchEvent(new CustomEvent('lv-selection-clear', { bubbles: true, composed: true }))
          if (invalidation === 'disconnect') {
            const element = document.querySelector('lv-dashboard-page')!
            element.remove()
            document.querySelector('main')!.appendChild(element)
          }
          module.mergePatch(accepted)
        }, invalidation)
        await waitRapidCanonical(page, 4)
        const transported = await page.locator('lv-dashboard-page').evaluate((element: any) => structuredClone(element.rapidTransported))
        expect(transported).toHaveLength(1)
        expect(transported[0]).toMatchObject({ interactionRevision: 0, mappings: [{ value: 'delivered' }] })
      } finally { await page.close() }
    })
  }

  test('rapid multiple selection preserves every queued toggle through canonical acknowledgments', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      const module = await prepareRapidSelection(page)
      const counts = [(await dispatchRapidSelections(page, ['delivered', 'shipped', 'delivered'], 'set')).length]
      for (const [index, values] of [['delivered'], ['delivered', 'shipped'], ['shipped']].entries()) {
        await module.evaluate((module: any, { index, values }) => module.mergePatch({ interactionRevision: index + 1,
          interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
            entries: values.map((value: string) => ({ mappings: [{ field: 'orders.status', dataset: 'orders', value }] })) }],
          status: { generation: index + 4, refreshId: 'refresh-' + (index + 4), loading: index !== 2, progressPercent: index === 2 ? 100 : 0 },
        }), { index, values })
        await waitRapidCanonical(page, index + 4)
        counts.push(await page.locator('lv-dashboard-page').evaluate((element: any) => element.rapidTransported.length))
      }
      const commands = await page.locator('lv-dashboard-page').evaluate((element: any) => element.rapidTransported.map((command: any) => ({ revision: command.interactionRevision, value: command.mappings[0].value })))
      expect({ counts, commands }).toEqual({ counts: [1, 2, 3, 3], commands: [
        { revision: 0, value: 'delivered' }, { revision: 1, value: 'shipped' }, { revision: 2, value: 'delivered' },
      ] })
    } finally { await page.close() }
  })

  test('rapid selection recovery retains the owned command while a prior error is still canonical', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      const module = await prepareRapidSelection(page, 'Previous refresh failed')
      await dispatchRapidSelections(page, ['delivered'])
      await waitRapidCanonical(page, 3)
      expect(await dispatchRapidSelections(page, ['shipped'])).toHaveLength(1)
      await module.evaluate((module: any) => module.mergePatch({ interactionRevision: 1,
        interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          entries: [{ mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] }],
        status: { generation: 4, refreshId: 'refresh-4', loading: true, error: '', progressPercent: 0 },
      }))
      await waitRapidCanonical(page, 4)
      const transported = await page.locator('lv-dashboard-page').evaluate((element: any) => structuredClone(element.rapidTransported))
      expect(transported).toHaveLength(2)
      expect(transported[1]).toMatchObject({ interactionRevision: 1, mappings: [{ value: 'shipped' }] })
    } finally { await page.close() }
  })

  test('rapid selection waits for a no-op refresh acknowledgment without predicting another revision', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      const module = await prepareRapidSelection(page)
      expect(await dispatchRapidSelections(page, ['delivered', 'delivered', 'shipped'])).toHaveLength(1)
      for (const [generation, revision] of [[4, 1], [5, 1]]) {
        await module.evaluate((module: any, { generation, revision }) => module.mergePatch({ interactionRevision: revision,
          interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
            entries: [{ mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] }],
          status: { generation, refreshId: 'refresh-' + generation, loading: true, progressPercent: 0 },
        }), { generation, revision })
        await waitRapidCanonical(page, generation)
      }
      const transported = await page.locator('lv-dashboard-page').evaluate((element: any) => structuredClone(element.rapidTransported))
      expect(transported).toHaveLength(3)
      expect(transported.map((command: any) => command.interactionRevision)).toEqual([0, 1, 1])
      expect(transported[2].mappings[0].value).toBe('shipped')
    } finally { await page.close() }
  })

  test('rapid source clear replaces queued point gestures while waiting for owned acceptance', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      const module = await prepareRapidSelection(page)
      await dispatchRapidSelections(page, ['delivered', 'shipped'])
      const before = await page.locator('lv-dashboard-page').evaluate((element: any) => {
        element.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: {
          sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', action: 'clear', toggle: false, mappings: [],
        } }))
        return structuredClone(element.rapidTransported)
      })
      expect(before).toHaveLength(1)
      await module.evaluate((module: any) => module.mergePatch({ interactionRevision: 1,
        interactionSelections: [{ sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          entries: [{ mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] }],
        status: { generation: 4, refreshId: 'refresh-4', loading: true, progressPercent: 0 },
      }))
      await waitRapidCanonical(page, 4)
      const transported = await page.locator('lv-dashboard-page').evaluate((element: any) => structuredClone(element.rapidTransported))
      expect(transported).toHaveLength(2)
      expect(transported[1]).toMatchObject({ action: 'clear', mappings: [], interactionRevision: 1 })
    } finally { await page.close() }
  })

  test('dashboard prevents invalid unstamped selection events from reaching the command bridge', async () => {
    const page = await getEnvironment().browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      await prepareRapidSelection(page)
      const count = await page.locator('lv-dashboard-page').evaluate((element: any) => {
        for (const detail of [null, {}, { sourceKind: 'visual', sourceId: 'missing' },
          { sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'wrong', action: 'set', toggle: true, mappings: [] }]) {
          element.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail }))
        }
        return element.rapidTransported.length
      })
      expect(count).toBe(0)
    } finally { await page.close() }
  })
}
