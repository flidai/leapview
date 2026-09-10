import { expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExploreFieldSignal } from '../../generated/signals'
import type { OptimisticInteractionCommand } from '../dashboard/interaction-selection'
import { DataExplorerQueryController } from './data-explorer-controller'
import { explorationSpecFromInteraction } from './data-explorer-drill'

const field = (id: string, type: string, extra: Partial<DataExploreFieldSignal> = {}): DataExploreFieldSignal => ({
  id, type, kind: 'dimension', label: id, datasetId: id.split('.')[0]!, compatible: true, selected: true, ...extra,
})

const baseSpec = (): ExplorationSpec => ({
  schemaVersion: 1,
  modelId: 'semantic:sales',
  datasetId: 'orders',
  dimensions: [{ field: 'orders.status' }],
  metrics: [{ field: 'revenue' }],
  filters: [{ field: 'orders.status', datasetId: 'orders', expression: { kind: 'comparison', operator: 'equals', value: { kind: 'string', value: 'paid' } } }],
  time: {
    field: 'orders.created_at', grain: 'month', alias: 'period',
    range: { kind: 'absolute', lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true } },
  },
  sort: [{ field: 'revenue', direction: 'desc' }],
  limit: 250,
  table: { columns: [{ field: 'orders.status' }, { field: 'revenue' }] },
  visualization: { kind: 'cartesian', mark: 'bar', x: { field: 'orders.created_at' }, y: [{ field: 'revenue' }], title: 'Revenue' },
})

const command = (mappings: OptimisticInteractionCommand['mappings'], action: OptimisticInteractionCommand['action'] = 'set'): OptimisticInteractionCommand => ({
  sourceKind: 'visual', sourceId: 'sales-chart', interactionKind: 'selection', action, toggle: false, mappings,
})

test('creates type-safe equals and null filters for every supported scalar', () => {
  const spec = { ...baseSpec(), filters: [] }
  const fields = [
    field('orders.status', 'string'),
    field('orders.active', 'boolean'),
    field('orders.count', 'integer'),
    field('orders.amount', 'decimal'),
    field('orders.day', 'date'),
    field('orders.created_at', 'timestamp'),
  ]
  const result = explorationSpecFromInteraction(spec, fields, command([
    { field: 'orders.status', value: 'paid' },
    { field: 'orders.active', value: true },
    { field: 'orders.count', value: 7 },
    { field: 'orders.amount', value: 12.5 },
    { field: 'orders.day', value: '2026-02-28' },
    { field: 'orders.created_at', value: '2026-02-28T12:30:00Z' },
    { field: 'orders.status', value: null },
  ]), 'explore_from_here')

  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.filters.map((filter) => filter.expression)).toEqual([
    { kind: 'comparison', operator: 'equals', value: { kind: 'string', value: 'paid' } },
    { kind: 'comparison', operator: 'equals', value: { kind: 'boolean', value: true } },
    { kind: 'comparison', operator: 'equals', value: { kind: 'integer', value: '7' } },
    { kind: 'comparison', operator: 'equals', value: { kind: 'decimal', value: '12.5' } },
    { kind: 'comparison', operator: 'equals', value: { kind: 'date', value: '2026-02-28' } },
    { kind: 'comparison', operator: 'equals', value: { kind: 'timestamp', value: '2026-02-28T12:30:00Z' } },
    { kind: 'null_check', operator: 'is_null' },
  ])
})

test('retains time, grain, sort, limit, lineage, and visualization/pivot configuration in explore mode', () => {
  const spec: ExplorationSpec = {
    ...baseSpec(),
    dimensions: [{ field: 'orders.created_at', grain: 'month' }, { field: 'orders.status' }],
    pivot: { rows: [{ field: 'orders.status' }], columns: [{ field: 'orders.created_at', grain: 'month' }], metrics: [{ field: 'revenue' }] },
  }
  const result = explorationSpecFromInteraction(spec, [field('orders.status', 'string')], command([{ field: 'orders.status', value: 'shipped' }]), 'explore_from_here')
  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.modelId).toBe(spec.modelId)
  expect(result.spec.datasetId).toBe(spec.datasetId)
  expect(result.spec.time).toEqual(spec.time)
  expect(result.spec.sort).toEqual(spec.sort)
  expect(result.spec.limit).toBe(spec.limit)
  expect(result.spec.visualization).toEqual(spec.visualization)
  expect(result.spec.pivot).toEqual(spec.pivot)
})

test('drill switches to a table and keeps governed query selections', () => {
  const spec = baseSpec()
  const result = explorationSpecFromInteraction(spec, [field('orders.status', 'string'), field('orders.id', 'string')], command([{ field: 'orders.status', value: 'shipped' }]), 'drill', ['id'])
  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.visualization).toEqual({ kind: 'table', columns: [{ field: 'orders.id' }, { field: 'orders.created_at' }] })
  expect(result.spec.pivot).toBeUndefined()
  expect(result.spec.dimensions).toEqual([{ field: 'orders.id' }])
  expect(result.spec.metrics).toEqual([])
  expect(result.spec.sort).toEqual([])
  expect(result.spec.time).toEqual(spec.time)
})

test('drill pivot removal survives the query controller complete-spec merge', () => {
  const spec = {
    ...baseSpec(),
    pivot: { rows: [{ field: 'orders.status' }], columns: [{ field: 'orders.created_at', grain: 'month' }], metrics: [{ field: 'revenue' }] },
  }
  const result = explorationSpecFromInteraction(spec, [field('orders.status', 'string'), field('orders.id', 'string')], command([{ field: 'orders.status', value: 'shipped' }]), 'drill', ['id'])
  expect(result.ok).toBe(true)
  if (!result.ok) return
  const commandAfterMerge = new DataExplorerQueryController().explore({ spec, requestSeq: 1, resetVersion: 1, columnWidths: {} }, result.spec)
  expect(commandAfterMerge.spec.pivot).toBeUndefined()
})

test('scopes physical fields to the query dataset and leaves conformed fields unscoped', () => {
  const fields = [field('orders.status', 'string'), field('order_status', 'string', { datasetId: 'orders' })]
  const result = explorationSpecFromInteraction({ ...baseSpec(), filters: [] }, fields, command([
    { field: 'orders.status', value: 'paid' }, { field: 'order_status', value: 'paid' },
  ]), 'explore_from_here')
  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.filters[0]).toMatchObject({ field: 'orders.status', datasetId: 'orders' })
  expect(result.spec.filters[1]).toMatchObject({ field: 'order_status' })
  expect(result.spec.filters[1]).not.toHaveProperty('datasetId')
})

test('deduplicates exact filters and rejects more than one hundred distinct filters', () => {
  const spec = { ...baseSpec(), filters: [] }
  const fields = [field('orders.status', 'string')]
  const duplicate = command([{ field: 'orders.status', value: 'paid' }, { field: 'orders.status', value: 'paid' }])
  const deduped = explorationSpecFromInteraction(spec, fields, duplicate, 'explore_from_here')
  expect(deduped.ok).toBe(true)
  if (deduped.ok) expect(deduped.spec.filters).toHaveLength(1)

  const manyFields = Array.from({ length: 101 }, (_, index) => field(`orders.status_${index}`, 'string'))
  const tooMany = explorationSpecFromInteraction(spec, manyFields, command(manyFields.map((candidate) => ({ field: candidate.id, value: 'paid' }))), 'explore_from_here')
  expect(tooMany).toMatchObject({ ok: false, code: 'filter_limit' })
})

test('fails closed for unsafe fields, values, grains, and clear interactions', () => {
  const spec = baseSpec()
  expect(explorationSpecFromInteraction(spec, [field('orders.revenue', 'decimal', { kind: 'metric' })], command([{ field: 'orders.revenue', value: 1 }]), 'drill')).toMatchObject({ ok: false, code: 'not_dimension' })
  expect(explorationSpecFromInteraction(spec, [field('orders.status', 'string', { compatible: false })], command([{ field: 'orders.status', value: 'paid' }]), 'drill')).toMatchObject({ ok: false, code: 'unavailable_field' })
  expect(explorationSpecFromInteraction(spec, [field('orders.amount', 'decimal')], command([{ field: 'orders.amount', value: Number.NaN }]), 'drill')).toMatchObject({ ok: false, code: 'unsupported_value' })
  expect(explorationSpecFromInteraction(spec, [field('orders.status', 'string')], command([{ field: 'orders.status', grain: 'month', value: 'paid' }]), 'drill')).toMatchObject({ ok: false, code: 'invalid_grain' })
  expect(explorationSpecFromInteraction(spec, [field('orders.created_at', 'timestamp')], command([{ field: 'orders.created_at', grain: 'day', value: '2026-02-28T12:30:00Z' }]), 'drill')).toMatchObject({ ok: false, code: 'mismatched_grain' })
  expect(explorationSpecFromInteraction(spec, [field('orders.status', 'string')], command([], 'clear'), 'drill')).toMatchObject({ ok: false, code: 'empty_interaction' })
  expect(explorationSpecFromInteraction(spec, [field('orders.status', 'string')], command([{ field: 'orders.status', value: 'paid' }]), 'drill')).toMatchObject({ ok: false, code: 'missing_grain' })
})

test('converts a date bucket selection into a typed half-open range', () => {
  const spec = { ...baseSpec(), filters: [] }
  const result = explorationSpecFromInteraction(spec, [field('orders.created_day', 'date')], command([
    { field: 'orders.created_day', grain: 'month', value: '2024-02-01' },
  ]), 'explore_from_here')

  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.filters).toEqual([{
    field: 'orders.created_day', datasetId: 'orders', expression: {
      kind: 'range',
      lower: { value: { kind: 'date', value: '2024-02-01' }, inclusive: true },
      upper: { value: { kind: 'date', value: '2024-03-01' }, inclusive: false },
    },
  }])
})

test('handles leap-day date and quarter timestamp bucket boundaries', () => {
  const dateResult = explorationSpecFromInteraction({ ...baseSpec(), filters: [] }, [field('orders.created_day', 'date')], command([
    { field: 'orders.created_day', grain: 'day', value: '2024-02-29' },
  ]), 'explore_from_here')
  expect(dateResult.ok).toBe(true)
  if (dateResult.ok) expect(dateResult.spec.filters[0]?.expression).toMatchObject({ upper: { value: { kind: 'date', value: '2024-03-01' }, inclusive: false } })

  const quarterResult = explorationSpecFromInteraction({ ...baseSpec(), time: undefined, filters: [] }, [field('orders.created_at', 'timestamp')], command([
    { field: 'orders.created_at', grain: 'quarter', value: '2024-10-01T00:00:00Z' },
  ]), 'explore_from_here')
  expect(quarterResult.ok).toBe(true)
  if (quarterResult.ok) expect(quarterResult.spec.filters[0]?.expression).toMatchObject({ upper: { value: { kind: 'timestamp', value: '2025-01-01T00:00:00Z' }, inclusive: false } })
})

test('preserves timestamp offsets while advancing calendar bucket boundaries', () => {
  const spec = { ...baseSpec(), filters: [] }
  const result = explorationSpecFromInteraction(spec, [field('orders.created_at', 'timestamp')], command([
    { field: 'orders.created_at', grain: 'month', value: '2024-02-01T00:00:00-05:00' },
  ]), 'explore_from_here')

  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.filters[0]?.expression).toEqual({
    kind: 'range',
    lower: { value: { kind: 'timestamp', value: '2024-02-01T00:00:00-05:00' }, inclusive: true },
    upper: { value: { kind: 'timestamp', value: '2024-03-01T00:00:00-05:00' }, inclusive: false },
  })
})

test('supports week and year bucket edges and intersects authored predicates', () => {
  const created = field('orders.created_at', 'timestamp')
  const spec: ExplorationSpec = {
    ...baseSpec(),
    time: undefined,
    filters: [{ field: 'orders.created_at', datasetId: 'orders', expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'timestamp', value: '2024-01-01T00:00:00Z' } } }],
  }
  const result = explorationSpecFromInteraction(spec, [created], command([
    { field: 'orders.created_at', grain: 'week', value: '2024-02-04T00:00:00Z' },
  ]), 'explore_from_here')

  expect(result.ok).toBe(true)
  if (!result.ok) return
  expect(result.spec.filters).toHaveLength(2)
  expect(result.spec.filters[1]?.expression).toEqual({
    kind: 'range',
    lower: { value: { kind: 'timestamp', value: '2024-02-04T00:00:00Z' }, inclusive: true },
    upper: { value: { kind: 'timestamp', value: '2024-02-11T00:00:00Z' }, inclusive: false },
  })

  const year = explorationSpecFromInteraction({ ...baseSpec(), time: undefined, filters: [] }, [created], command([
    { field: 'orders.created_at', grain: 'year', value: '2024-01-01T00:00:00Z' },
  ]), 'explore_from_here')
  expect(year.ok).toBe(true)
  if (year.ok) expect(year.spec.filters[0]?.expression).toMatchObject({ upper: { value: { kind: 'timestamp', value: '2025-01-01T00:00:00Z' }, inclusive: false } })
})

test('fails closed for zoned timestamp bucket selections', () => {
  const result = explorationSpecFromInteraction({ ...baseSpec(), filters: [] }, [field('orders.event_at', 'DateTimeTz')], command([
    { field: 'orders.event_at', grain: 'day', value: '2024-02-01T00:00:00-05:00' },
  ]), 'explore_from_here')
  expect(result).toMatchObject({ ok: false, code: 'invalid_grain' })
})

test('rejects date subday buckets instead of constructing an empty range', () => {
  const result = explorationSpecFromInteraction({ ...baseSpec(), time: undefined, filters: [] }, [field('orders.created_day', 'date')], command([
    { field: 'orders.created_day', grain: 'hour', value: '2024-02-01' },
  ]), 'explore_from_here')
  expect(result).toMatchObject({ ok: false, code: 'invalid_grain' })
})

test('rejects malformed calendar, clock, and offset bucket values', () => {
  const created = field('orders.created_at', 'timestamp')
  for (const value of ['2024-02-30T00:00:00Z', '2024-02-01T24:00:00Z', '2024-02-01T00:00:00+24:00']) {
    const result = explorationSpecFromInteraction({ ...baseSpec(), time: undefined, filters: [] }, [created], command([
      { field: 'orders.created_at', grain: 'day', value },
    ]), 'explore_from_here')
    expect(result).toMatchObject({ ok: false, code: 'unsupported_value' })
  }
})

test('allows null checks on zoned timestamp mappings without deriving a bucket', () => {
  const result = explorationSpecFromInteraction({ ...baseSpec(), filters: [] }, [field('orders.event_at', 'DateTimeTz')], command([
    { field: 'orders.event_at', grain: 'day', value: null },
  ]), 'explore_from_here')
  expect(result.ok).toBe(true)
  if (result.ok) expect(result.spec.filters[0]?.expression).toEqual({ kind: 'null_check', operator: 'is_null' })
})
