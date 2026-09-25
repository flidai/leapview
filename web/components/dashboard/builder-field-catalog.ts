import type { DashboardBuilderDatasetSignal, DashboardBuilderFieldSignal } from '../../generated/signals'

export type BuilderCatalogField = {
  field: DashboardBuilderFieldSignal
  datasets: Array<{ id: string; title: string }>
  group: 'metric' | 'dimension' | 'time'
}

export function buildSemanticCatalog(datasets: DashboardBuilderDatasetSignal[]): BuilderCatalogField[] {
  const catalog = new Map<string, BuilderCatalogField>()
  for (const dataset of datasets) {
    const datasetFields = new Map<string, DashboardBuilderFieldSignal>()
    for (const field of dataset.fields) {
      const rolesKey = [...(field.roles ?? [])].sort().join(',')
      const fieldKey = `${field.kind}:${rolesKey}:${field.id}`
      const existing = datasetFields.get(fieldKey)
      if (!existing || fieldCatalogScore(field) > fieldCatalogScore(existing)) datasetFields.set(fieldKey, field)
    }
    for (const field of datasetFields.values()) {
      const rolesKey = [...(field.roles ?? [])].sort().join(',')
      const datasetKey = field.roles?.includes('detail') ? (field.datasetId ?? dataset.id) : ''
      const key = `${field.kind}:${rolesKey}:${field.id}:${datasetKey}`
      const existing = catalog.get(key)
      if (existing) {
        if (!existing.datasets.some((item) => item.id === dataset.id)) existing.datasets.push({ id: dataset.id, title: businessGroupTitle(dataset) })
        continue
      }
      catalog.set(key, {
        field,
        datasets: [{ id: dataset.id, title: businessGroupTitle(dataset) }],
        group: builderFieldCatalogGroup(field),
      })
    }
  }
  return Array.from(catalog.values()).sort((left, right) => {
    const groupOrder = { metric: 0, dimension: 1, time: 2 }
    const byGroup = groupOrder[left.group] - groupOrder[right.group]
    return byGroup || left.field.label.localeCompare(right.field.label)
  })
}

export function builderFieldCatalogGroup(field: DashboardBuilderFieldSignal): BuilderCatalogField['group'] {
  if (field.kind === 'metric') return 'metric'
  const dataType = field.dataType.toLowerCase()
  return dataType.includes('date') || dataType.includes('time') || dataType.includes('timestamp') ? 'time' : 'dimension'
}

function fieldCatalogScore(field: DashboardBuilderFieldSignal): number {
  let score = 0
  if (field.dataType.trim().toLowerCase() !== 'unknown') score += 4
  if (!field.id.includes('.')) score += 2
  if (field.description?.trim()) score += 1
  return score
}

function businessGroupTitle(dataset: DashboardBuilderDatasetSignal): string {
  const title = dataset.title.trim()
  const source = title.length > 0 && title.length <= 32 && !/[.!?]$/.test(title) ? title : dataset.id
  const normalized = source.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim()
  return normalized ? normalized.charAt(0).toLocaleUpperCase() + normalized.slice(1) : 'Semantic model'
}
