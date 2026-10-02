import type { DashboardCompiledFilterBinding, DashboardCompiledFilterDefinition, DashboardFilterPresentation } from '../web/generated/signals'

export const filterStyles: DashboardFilterPresentation['style'][] = ['dropdown', 'list', 'buttons', 'input', 'numeric_range', 'date_range', 'relative_period']
export function filterFixture(style: DashboardFilterPresentation['style'], disabled = false, empty = false, multiple = false) {
  const categorical = ['dropdown', 'list', 'buttons'].includes(style)
  const date = style === 'date_range' || style === 'relative_period'
  const definition: DashboardCompiledFilterDefinition = {
    id: 'preview-filter', field: categorical ? 'orders.region' : date ? 'orders.date' : 'orders.amount',
    label: categorical ? 'Region' : date ? 'Order date' : style === 'input' ? 'Customer name' : 'Order amount',
    valueKind: categorical || style === 'input' ? 'string' : date ? 'date' : 'decimal',
    predicates: [{ kind: categorical ? 'set' : style === 'input' ? 'comparison' : style === 'relative_period' ? 'relative_period' : 'range', operators: style === 'input' ? ['contains'] : [] }],
    options: { kind: categorical ? 'static' : 'none', limit: 50, includeNull: false, values: empty ? [] : ['Europe', 'Asia Pacific', 'North America', 'Latin America and Caribbean — long label'].map(label => ({ label, value: { kind: 'string', value: label } })) },
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
