import type { VisualizationEnvelope } from '../../generated/visualization'

/** Chat envelopes are still carried as reactive objects. Datastar hydrates
 * null array cells as empty strings; restore missing numeric cells using the
 * authored field schema before giving them to a renderer. Text stays intact. */
export function chatVisualsFromSignals(visuals: Record<string, VisualizationEnvelope>): Record<string, VisualizationEnvelope> {
  return Object.fromEntries(Object.entries(visuals).map(([id, envelope]) => {
    if (envelope.dataState.kind !== 'inline') return [id, envelope]
    const datasets = envelope.dataState.datasets.map(dataset => {
      const fields = envelope.spec.datasets.find(spec => spec.id === dataset.id)?.fields ?? []
      const nullableNumbers = new Set(fields
        .filter(field => field.nullable && ['integer', 'decimal', 'float'].includes(field.dataType))
        .map(field => dataset.columns.indexOf(field.id)))
      if (!dataset.rows.some(row => row.some((value, index) => value === '' && nullableNumbers.has(index)))) return dataset
      return { ...dataset, rows: dataset.rows.map(row => row.map((value, index) => value === '' && nullableNumbers.has(index) ? null : value)) }
    })
    return [id, { ...envelope, dataState: { ...envelope.dataState, datasets } }]
  }))
}
