import { beforeAll, afterEach, expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'
import type { DataExploreResultSignal, DataExploreStatusSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'

let Results!: typeof import('./data-explorer-results')

beforeAll(async () => {
  const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' })
  const window = dom.window
  const globals = [
    'window', 'document', 'Document', 'HTMLElement', 'Element', 'Node', 'Event', 'CustomEvent', 'KeyboardEvent', 'ShadowRoot',
    'DocumentFragment', 'HTMLButtonElement', 'MutationObserver', 'CSSStyleSheet', 'customElements', 'getComputedStyle',
  ] as const
  for (const name of globals) Object.defineProperty(globalThis, name, { configurable: true, value: (window as any)[name] })
  Object.defineProperty(globalThis, 'ResizeObserver', {
    configurable: true,
    value: class { observe(): void {}; disconnect(): void {} },
  })
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: () => ({ matches: false, addEventListener(): void {}, removeEventListener(): void {} }),
  })

  // The result surface only verifies and forwards envelopes. A small host
  // double keeps these unit tests independent from canvas/ECharts rendering.
  class VisualizationHostStub extends HTMLElement {
    envelope?: VisualizationEnvelope
  }
  customElements.define('lv-visualization-host', VisualizationHostStub)
  Results = await import('./data-explorer-results')
})

afterEach(() => {
  document.body.replaceChildren()
})

const result = (overrides: Partial<DataExploreResultSignal> = {}): DataExploreResultSignal => ({
  columns: [{ key: 'status', label: 'Status', type: 'string' }],
  rows: [{ status: 'paid' }],
  rowsReturned: 1,
  durationMs: 12,
  requestSeq: 1,
  truncated: false,
  warnings: [],
  ...overrides,
})

const status = (overrides: Partial<DataExploreStatusSignal> = {}): DataExploreStatusSignal => ({
  loading: false,
  requestSeq: 1,
  stale: false,
  state: 'success',
  ...overrides,
})

const envelope = (kind: VisualizationEnvelope['spec']['kind']): VisualizationEnvelope => ({ spec: { kind } } as VisualizationEnvelope)

async function mount(properties: Record<string, unknown> = {}): Promise<HTMLElement & { updateComplete: Promise<unknown> }> {
  const element = document.createElement('lv-data-explorer-results') as HTMLElement & { updateComplete: Promise<unknown> }
  Object.assign(element, { result: result(), status: status(), ...properties })
  document.body.append(element)
  await element.updateComplete
  return element
}

test('renders accessible tabs and disables views without compatible envelopes', async () => {
  const element = await mount({
    recommendedView: 'table',
    visualizations: { table: envelope('table') },
    selectedDataset: { title: 'Orders', grainEntity: 'order_id' },
    grain: 'Order',
  })
  const tabs = [...element.shadowRoot!.querySelectorAll<HTMLButtonElement>('[role="tab"]')]
  expect(tabs.map((tab) => tab.textContent?.trim())).toEqual(['Table', 'Chart', 'Pivot', 'SQL / Details'])
  expect(tabs[0]?.disabled).toBe(false)
  expect(tabs[1]?.getAttribute('aria-disabled')).toBe('true')
  expect(tabs[1]?.getAttribute('aria-describedby')).toContain('unavailable')
  expect(tabs[0]?.getAttribute('aria-selected')).toBe('true')
  expect(element.shadowRoot?.querySelector('lv-visualization-host')).not.toBeNull()
  expect(element.shadowRoot?.textContent).toContain('Dataset: Orders')
  expect(element.shadowRoot?.textContent).toContain('Grain: Order')
  expect(element.shadowRoot?.textContent).toContain('Freshness: Unknown')
  expect(element.shadowRoot?.querySelector('[data-result-freshness]')?.getAttribute('data-state')).toBe('unknown')
})

test('uses roving tab focus and skips disabled views with arrow keys', async () => {
  const element = await mount({ visualizations: { table: envelope('table') } })
  const table = element.shadowRoot!.querySelector<HTMLButtonElement>('[data-view="table"]')!
  table.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, composed: true }))
  await element.updateComplete
  expect(element.shadowRoot!.querySelector('[data-view="details"]')?.getAttribute('aria-selected')).toBe('true')
  expect(element.shadowRoot!.querySelector('[data-view="details"]')?.getAttribute('tabindex')).toBe('0')
})

test('preserves a user-selected view across a later completed request', async () => {
  const element = await mount({ visualizations: { table: envelope('table') } }) as any
  const details = element.shadowRoot!.querySelector('[data-view="details"]') as HTMLButtonElement
  details.click()
  await element.updateComplete
  element.result = result({ requestSeq: 2, rows: [{ status: 'shipped' }] })
  element.status = status({ requestSeq: 2 })
  await element.updateComplete
  expect(element.shadowRoot!.querySelector('[data-view="details"]')?.getAttribute('aria-selected')).toBe('true')
})

test('falls back safely when a selected visualization is unavailable in a later result', async () => {
  const element = await mount({ visualizations: { table: envelope('table'), chart: envelope('cartesian') } }) as any
  ;(element.shadowRoot!.querySelector('[data-view="chart"]') as HTMLButtonElement).click()
  await element.updateComplete
  element.visualizations = { table: envelope('table') }
  element.result = result({ requestSeq: 2 })
  element.status = status({ requestSeq: 2 })
  await element.updateComplete
  expect(element.shadowRoot!.querySelector('[data-view="table"]')?.getAttribute('aria-selected')).toBe('true')
})

test('keeps freshness, snapshot, truncation, warnings, and safely escaped SQL details visible', async () => {
  const element = await mount({
    result: result({
      rowsReturned: 1000,
      truncated: true,
      freshness: { status: 'stale', snapshotId: 'snapshot-7' },
      sql: '<script>alert(1)</script>',
      plan: '<b>scan orders</b>',
      warnings: ['Result is capped'],
    }),
    status: status({ stale: true, state: 'stale' }),
  }) as any
  ;(element.shadowRoot!.querySelector('[data-view="details"]') as HTMLButtonElement).click()
  await element.updateComplete
  expect(element.shadowRoot?.textContent).toContain('Freshness: Stale')
  expect(element.shadowRoot?.textContent).toContain('Snapshot: snapshot-7')
  expect(element.shadowRoot?.textContent).toContain('Rows: 1000 (truncated)')
  expect(element.shadowRoot?.textContent).toContain('Result is capped')
  expect(element.shadowRoot?.querySelector('[data-sql]')?.textContent).toBe('<script>alert(1)</script>')
  expect(element.shadowRoot?.querySelector('script')).toBeNull()
  expect(element.shadowRoot?.querySelector('b')).toBeNull()
})

test('offers explicit drill actions and forwards the selected governed mode', async () => {
  const element = await mount({ visualizations: { table: envelope('table') } })
  let forwarded: unknown
  element.addEventListener('lv-data-explore-interaction', (event) => { forwarded = (event as CustomEvent).detail })
  const mapping = { field: 'orders.status', dataset: 'orders', value: 'paid', label: 'Paid' } as const
  const command = {
    sourceKind: 'visual' as const,
    sourceId: 'orders-table',
    interactionKind: 'selection',
    action: 'set' as const,
    toggle: false,
    mappings: [mapping],
  }
  element.shadowRoot!.querySelector('lv-visualization-host')!.dispatchEvent(new CustomEvent('lv-interaction-select', {
    bubbles: true,
    composed: true,
    detail: command,
  }))
  await element.updateComplete
  expect(element.shadowRoot?.textContent).toContain('Drill to rows')
  ;([...element.shadowRoot!.querySelectorAll('button')].find((button) => button.textContent?.includes('Explore from here')) as HTMLButtonElement).click()
  expect(forwarded).toEqual({ command, mode: 'explore_from_here' })
})

test('clears an old selection when a newer request completes and scopes tab ids per instance', async () => {
  const first = await mount({ visualizations: { table: envelope('table') } }) as any
  const second = await mount({ visualizations: { table: envelope('table') } }) as any
  const command = {
    sourceKind: 'visual' as const,
    sourceId: 'orders-table',
    interactionKind: 'selection',
    action: 'set' as const,
    toggle: false,
    mappings: [{ field: 'orders.status', dataset: 'orders', value: 'paid', label: 'Paid' }],
  }
  first.shadowRoot!.querySelector('lv-visualization-host')!.dispatchEvent(new CustomEvent('lv-interaction-select', {
    bubbles: true, composed: true, detail: command,
  }))
  await first.updateComplete
  expect(first.shadowRoot?.textContent).toContain('Drill to rows')

  first.result = result({ requestSeq: 2, rows: [{ status: 'shipped' }] })
  first.status = status({ requestSeq: 2 })
  await first.updateComplete
  expect(first.shadowRoot?.textContent).not.toContain('Drill to rows')
  expect(first.shadowRoot!.querySelector('[role="tab"]')?.id)
    .not.toBe(second.shadowRoot!.querySelector('[role="tab"]')?.id)
})

test('normalizes SQL as the details view and falls back to table for unknown recommendations', () => {
  expect(Results.normalizeView('sql')).toBe('details')
  expect(Results.normalizeView('line')).toBe('chart')
  expect(Results.normalizeView('unknown')).toBe('table')
  expect(Results.envelopeSupportsView(envelope('table'), 'table')).toBe(true)
  expect(Results.envelopeSupportsView(envelope('kpi'), 'chart')).toBe(true)
})
