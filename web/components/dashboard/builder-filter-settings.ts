import type { DashboardBuilderFilterSignal, DashboardFilterContract } from '../../generated/signals'

type Control = DashboardBuilderFilterSignal['controlType']
const labels: Record<Control, string> = {
  singleSelect: 'Single select', multiSelect: 'Multi select', text: 'Text search',
  numericRange: 'Numeric range', dateRange: 'Date range', relativePeriod: 'Relative period',
}

export function filterControlLabel(control: Control): string {
  return labels[control]
}

export function filterControlChoices(dataType: string, current: Control): Array<[Control, string]> {
  const type = dataType.toLowerCase()
  let controls: Control[]
  if (type.includes('bool')) controls = ['multiSelect', 'singleSelect']
  else if (type.includes('date') || type.includes('time')) controls = ['relativePeriod', 'dateRange', 'singleSelect']
  else if (['number', 'integer', 'decimal', 'float'].some(value => type.includes(value))) controls = ['numericRange', 'singleSelect', 'multiSelect']
  else controls = ['multiSelect', 'singleSelect', 'text']
  // Keep an existing authored control visible so it can be repaired explicitly.
  if (!controls.includes(current)) controls.push(current)
  return controls.map(control => [control, labels[control]])
}

export function canRequireFilter(filter: Pick<DashboardBuilderFilterSignal, 'id' | 'required'>, contract: DashboardFilterContract): boolean {
  if (filter.required) return true // An invalid existing setting must remain removable.
  const bindings = Object.values(contract.bindings).filter(binding => binding.filter === filter.id)
  return bindings.length > 0 && bindings.every(binding => binding.default.kind !== 'unfiltered')
}
