import type { DashboardCompiledFilterBinding, DashboardCompiledFilterDefinition, DashboardFilterContract, DashboardFilterExpression, DashboardFilterPresentation } from '../web/generated/signals'

export const filterRegions = ['Europe', 'Asia Pacific', 'North America', 'Latin America and Caribbean — long label']

export const filterStyles: DashboardFilterPresentation['style'][] = ['dropdown', 'list', 'buttons', 'input', 'numeric_range', 'date_range', 'relative_period']
export function filterFixture(style: DashboardFilterPresentation['style'], disabled = false, empty = false, multiple = false) {
  const categorical = ['dropdown', 'list', 'buttons'].includes(style)
  const date = style === 'date_range' || style === 'relative_period'
  const definition: DashboardCompiledFilterDefinition = {
    id: 'preview-filter', field: categorical ? 'orders.region' : date ? 'orders.date' : 'orders.amount',
    label: categorical ? 'Region' : date ? 'Order date' : style === 'input' ? 'Customer name' : 'Order amount',
    valueKind: categorical || style === 'input' ? 'string' : date ? 'date' : 'decimal',
    predicates: [{ kind: categorical ? 'set' : style === 'input' ? 'comparison' : style === 'relative_period' ? 'relative_period' : 'range', operators: style === 'input' ? ['contains'] : [] }],
    options: { kind: categorical ? 'static' : 'none', limit: 50, includeNull: false, values: empty ? [] : filterRegions.map(label => ({ label, value: { kind: 'string', value: label } })) },
    timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
  }
  const binding: DashboardCompiledFilterBinding = {
    key: definition.id, id: definition.id, filter: definition.id, scope: 'page', pageID: 'playground',
    default: categorical ? { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'Europe' }] } : { kind: 'unfiltered' },
    selectionMode: multiple ? 'multiple' : 'single', maxSelectedValues: multiple ? 10 : 1, required: false,
    readerEditable: !disabled, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
  }
  const presentation: DashboardFilterPresentation = { style, search: categorical, selectAll: categorical && multiple, showCounts: false, showSummary: true, compact: false }
  return { definition, binding, presentation }
}

export type FilterScopes = 'both' | 'page' | 'report'

export function filterDockFixture(style: DashboardFilterPresentation['style'], mode: DashboardFilterContract['applicationMode'], scopes: FilterScopes, disabled = false, empty = false, multiple = false): DashboardFilterContract {
  const { definition, binding } = filterFixture(style, disabled, empty, multiple)
  return {
    applicationMode: mode,
    definitions: { [definition.id]: definition },
    bindings: {
      [binding.key]: { ...binding, paneVisible: scopes !== 'report' },
      'report-filter': { ...binding, key: 'report-filter', id: 'report-filter', scope: 'report', pageID: undefined, paneVisible: scopes !== 'page' },
    },
  }
}

export function isFixtureDate(value: unknown): value is string {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return false
  const parsed = new Date(`${value}T00:00:00Z`)
  return Number.isFinite(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value
}

/** Read only the expression variants produced by these deterministic fixtures. */
export function restoreFilterExpression(value: unknown, style: DashboardFilterPresentation['style'], multiple: boolean): DashboardFilterExpression | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const item = value as Record<string, unknown>
  if (item.kind === 'unfiltered') return { kind: 'unfiltered' }
  if (['dropdown', 'list', 'buttons'].includes(style) && item.kind === 'set' && (item.operator === 'in' || item.operator === 'not_in') && Array.isArray(item.values) && item.values.length <= (multiple ? 10 : 1)) {
    const values = item.values.map(value => {
      if (!value || typeof value !== 'object' || value.kind !== 'string' || !filterRegions.includes(value.value)) return undefined
      return { kind: 'string' as const, value: value.value as string }
    })
    if (values.every(value => value !== undefined)) return { kind: 'set', operator: item.operator, values }
  }
  if (style === 'input' && item.kind === 'comparison' && item.operator === 'contains' && item.value && typeof item.value === 'object') {
    const value = item.value as Record<string, unknown>
    if (value.kind === 'string' && typeof value.value === 'string' && value.value.length <= 1000) return { kind: 'comparison', operator: 'contains', value: { kind: 'string', value: value.value } }
  }
  if ((style === 'numeric_range' || style === 'date_range') && item.kind === 'range') {
    const result: Extract<DashboardFilterExpression, { kind: 'range' }> = { kind: 'range' }
    for (const key of ['lower', 'upper'] as const) {
      if (item[key] === undefined) continue
      const bound = item[key] as Record<string, unknown> | null
      if (!bound || typeof bound !== 'object' || typeof bound.inclusive !== 'boolean' || !bound.value || typeof bound.value !== 'object') return undefined
      const value = bound.value as Record<string, unknown>
      if (style === 'date_range' && value.kind === 'date' && isFixtureDate(value.value)) result[key] = { inclusive: bound.inclusive, value: { kind: 'date', value: value.value } }
      else if (style === 'numeric_range' && value.kind === 'decimal' && typeof value.value === 'string' && value.value.length <= 100 && /^-?\d+(\.\d+)?$/.test(value.value) && Number.isFinite(Number(value.value))) result[key] = { inclusive: bound.inclusive, value: { kind: 'decimal', value: value.value } }
      else return undefined
    }
    if (result.lower && result.upper && (style === 'date_range' ? String(result.lower.value.value) > String(result.upper.value.value) : Number(result.lower.value.value) > Number(result.upper.value.value))) return undefined
    return result
  }
  if (style === 'relative_period' && item.kind === 'relative_period' && ['previous', 'current', 'next'].includes(String(item.direction)) && ['minute', 'hour', 'day', 'week', 'month', 'quarter', 'year'].includes(String(item.unit)) && typeof item.count === 'number' && Number.isSafeInteger(item.count) && item.count > 0 && item.count <= 10000 && typeof item.includeCurrent === 'boolean' && item.anchor === 'current_time') {
    return { kind: 'relative_period', direction: item.direction as 'previous' | 'current' | 'next', unit: item.unit as 'minute' | 'hour' | 'day' | 'week' | 'month' | 'quarter' | 'year', count: item.count, includeCurrent: item.includeCurrent, anchor: 'current_time' }
  }
  return undefined
}
