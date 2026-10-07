import { expect, test } from 'bun:test'
import type { ChatTranscriptItemSignal } from '../../generated/signals'
import { retainedVisualExplorerHref, visualDataExplorerHref } from './visual-action-links'
import { searchActionHref } from './search-action'
import { dashboardActionLinks } from './dashboard-action-links'

function recordedVisual(overrides: Record<string, unknown> = {}): ChatTranscriptItemSignal {
  return {
    id: 'tool', kind: 'tool', name: 'query_visual', status: 'complete', runId: 'run-1', toolCallId: 'call-1',
    artifact: { id: 'chart-1', type: 'bar' },
    argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: {
      type: 'bar', query: { type: 'aggregate', dimensions: [{ dimension: 'ordered_at', grain: 'month', alias: 'month' }], metrics: ['revenue'], sort: [{ field: 'month', direction: 'asc' }], limit: 50 },
    }, filters: [{ dimension: 'ordered_at', default: { type: 'comparison', operator: 'greaterThanOrEqual', value: { type: 'date', value: '2026-01-01' } } }], ...overrides }),
    resultJson: JSON.stringify({ ok: true, id: 'chart-1', type: 'bar', datasetId: 'orders', semanticModelRef: { id: 'sales' }, completeness: { limit: 17 },
      fields: [{ role: 'dimension', fieldId: 'sales.ordered_at', explorerFieldId: 'ordered_at', alias: 'month' }],
      filters: [{ fieldId: 'sales.ordered_at', resolvedDatasetId: 'orders' }],
    }),
  } as ChatTranscriptItemSignal
}

test('editable handoff preserves canonical typed filters, recorded row limit and time sorting', () => {
  const url = new URL(visualDataExplorerHref(recordedVisual()), 'https://example.test')
  expect(url.searchParams.get('v')).toBe('2')
  expect(JSON.parse(url.searchParams.get('state')!)).toMatchObject({
    modelId: 'sales', datasetId: 'orders', limit: 17,
    time: { field: 'ordered_at', grain: 'month', alias: 'month' },
    sort: [{ field: 'ordered_at', direction: 'asc' }],
    filters: [{ field: 'ordered_at', datasetId: 'orders', expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'date', value: '2026-01-01' } } }],
  })
})

test('unmapped or relative filters retain audit access without a misleading editable query', () => {
  for (const filter of [
    { dimension: 'country', default: { type: 'comparison', operator: 'equals', value: { type: 'string', value: 'DE' } } },
    { dimension: 'ordered_at', default: { type: 'relativePeriod', direction: 'previous', count: 1, unit: 'month' } },
  ]) {
    const item = recordedVisual({ filters: [filter] })
    expect(visualDataExplorerHref(item)).toBe('')
    expect(retainedVisualExplorerHref('chat-1', item, { enabled: true, running: false })).toBe('/chats/chat-1/visuals/chart-1/explore?run=run-1')
  }
})

test('audit links wait for message persistence while older runs remain inspectable', () => {
  expect(retainedVisualExplorerHref('chat-1', recordedVisual(), { enabled: true, running: true, runId: 'run-1' })).toBe('')
  expect(retainedVisualExplorerHref('chat-1', recordedVisual(), { enabled: true, running: true, runId: 'run-2' })).toContain('run=run-1')
})

test('search handoff retains exact kinds, domain, cursor and limit', () => {
  const item = { kind: 'tool', name: 'catalog_search', status: 'complete', argumentsJson: JSON.stringify({ query: 'sales', kinds: ['model', 'dashboard'], domain: 'finance', cursor: 'page/2', limit: 7 }) } as ChatTranscriptItemSignal
  const url = new URL(searchActionHref(item)!, 'https://example.test')
  expect(url.searchParams.getAll('kind')).toEqual(['model', 'dashboard'])
  expect(Object.fromEntries(['q', 'domain', 'cursor', 'limit'].map(key => [key, url.searchParams.get(key)]))).toEqual({ q: 'sales', domain: 'finance', cursor: 'page/2', limit: '7' })
})

test('Builder handoff resolves the recorded operation within its conversation and run', () => {
  expect(dashboardActionLinks({ ...recordedVisual(), name: 'fork_dashboard' }, 'chat-1')).toEqual([
    { label: 'Open in Builder', href: '/chats/chat-1/actions/call-1/open?run=run-1' },
  ])
})
