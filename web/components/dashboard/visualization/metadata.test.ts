import { describe, expect, test } from 'bun:test'
import type { VisualizationEnvelope } from '../../../generated/visualization'
import { resolveVisualizationMetadata } from './metadata'

function envelope(rows: unknown[][]): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'revenue', rendererID: 'echarts', specRevision: 'sha256:spec', dataRevision: 3,
    spec: {
      kind: 'cartesian', title: 'Revenue', subtitle: 'Current scope', mark: 'line',
      datasets: [
        { id: 'primary', fields: [
          { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
          { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
        ] },
        { id: 'context', fields: [
          { id: 'region', role: 'dimension', dataType: 'string', nullable: true, label: 'Region' },
          { id: 'target', role: 'metric', dataType: 'decimal', nullable: true, label: 'Target' },
        ] },
      ],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Revenue', description: 'Revenue by month', summary: 'Monthly trend' },
      interactions: [],
      metadataBindings: {
        title: { field: { dataset: 'context', field: 'region' }, reducer: 'first', prefix: 'Revenue — ', suffix: '', fallback: 'Revenue' },
        description: { field: { dataset: 'context', field: 'target' }, reducer: 'median', prefix: 'Target ', suffix: ' USD', fallback: 'Target unavailable' },
      },
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: {
      kind: 'inline', specRevision: 'sha256:spec', dataRevision: 3, generation: 1,
      datasets: [
        { id: 'primary', specRevision: 'sha256:spec', dataRevision: 3, generation: 1, columns: ['label', 'value'], rows: [['Jan', 42]], completeness: 'complete' },
        { id: 'context', specRevision: 'sha256:spec', dataRevision: 3, generation: 1, columns: ['region', 'target'], rows, completeness: rows.length ? 'complete' : 'empty' },
      ],
    },
    status: { kind: 'ready' }, diagnostics: [], selection: [],
  }
}

describe('resolveVisualizationMetadata', () => {
  test('reduces the current governed context frame', () => {
    expect(resolveVisualizationMetadata(envelope([['EMEA', 80], ['EMEA', 100]]))).toEqual({
      title: 'Revenue — EMEA',
      subtitle: 'Current scope',
      description: 'Target 90 USD',
      summary: 'Monthly trend',
    })
  })

  test('uses authored fallbacks for empty or incompatible data', () => {
    expect(resolveVisualizationMetadata(envelope([])).title).toBe('Revenue')
    expect(resolveVisualizationMetadata(envelope([['EMEA', 'restricted']])).description).toBe('Target unavailable')
    expect(resolveVisualizationMetadata(envelope([['   ', 80]])).title).toBe('Revenue')
  })
})
