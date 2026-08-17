import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../generated/visualization'
import { interactionCommandForRowIndex, interactionOptions } from './interaction-command'

const envelope = {
  schemaVersion: 9, visualID: 'customers', rendererID: 'maplibre', specRevision: 'sha256:test', dataRevision: 7,
  spec: {
    kind: 'geographic', title: 'Customers',
    datasets: [{ id: 'primary', fields: [
      { id: 'customer_id', role: 'identity', dataType: 'string', nullable: false, label: 'Customer' },
      { id: 'state', role: 'identity', dataType: 'string', nullable: false, label: 'State' },
      { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
    ] }],
    dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Customers', description: 'Customers' },
    interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['states'], mappings: [
      { source: { dataset: 'primary', field: 'customer_id' }, targetFieldID: 'customers.customer_id', targetFactID: 'customers', label: { dataset: 'primary', field: 'revenue' } },
      { source: { dataset: 'primary', field: 'state' }, targetFieldID: 'customers.state', targetFactID: 'customers' },
    ] }],
    layers: [], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, roam: false },
  },
  dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 7, generation: 1, datasets: [{
    id: 'primary', specRevision: 'sha256:test', dataRevision: 7, generation: 1,
    columns: ['customer_id', 'state', 'revenue'], rows: [['c-1', 'SP', 42], ['c-1', 'SP', 42], ['c-2', 'RJ', 18]], completeness: 'complete',
  }] },
  selection: [], status: { kind: 'ready' }, diagnostics: [],
} as VisualizationEnvelope

test('row-index interaction translation validates the locator and compiled mappings', () => {
  expect(interactionCommandForRowIndex(envelope, 'primary', 0)).toEqual({
    sourceKind: 'visual', sourceId: 'customers', interactionKind: 'point_selection', action: 'set', toggle: true,
    mappings: [
      { field: 'customers.customer_id', fact: 'customers', value: 'c-1', label: '42' },
      { field: 'customers.state', fact: 'customers', value: 'SP', label: 'SP' },
    ],
  })
  expect(interactionCommandForRowIndex(envelope, 'primary', -1)).toBeUndefined()
  expect(interactionCommandForRowIndex(envelope, 'primary', 3)).toBeUndefined()
  expect(interactionCommandForRowIndex(envelope, 'forged', 0)).toBeUndefined()
  expect(interactionCommandForRowIndex(envelope, 'primary', 0.5)).toBeUndefined()
  const nullIdentity = structuredClone(envelope) as VisualizationEnvelope
  if (nullIdentity.dataState.kind === 'inline') nullIdentity.dataState.datasets[0]!.rows[0]![0] = null
  expect(interactionCommandForRowIndex(nullIdentity, 'primary', 0)).toBeUndefined()
})

test('keyboard interaction options collapse duplicate identity tuples and use composite labels', () => {
  expect(interactionOptions(envelope)).toEqual([
    { key: '[["customers.customer_id","customers",null,"c-1"],["customers.state","customers",null,"SP"]]', label: '42 · SP', command: interactionCommandForRowIndex(envelope, 'primary', 0), selected: false },
    { key: '[["customers.customer_id","customers",null,"c-2"],["customers.state","customers",null,"RJ"]]', label: '18 · RJ', command: interactionCommandForRowIndex(envelope, 'primary', 2), selected: false },
  ])

  const selected = {
    ...envelope,
    selection: [{ datum: { dataset: 'primary', dataRevision: 7, identity: { customer_id: 'c-2', state: 'RJ' } }, label: 'c-2' }],
  } as VisualizationEnvelope
  expect(interactionOptions(selected).map((option) => option.selected)).toEqual([false, true])
})
