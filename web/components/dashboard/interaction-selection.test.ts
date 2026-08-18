import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../generated/visualization'
import {
  visualizationHighlightStates,
  visualizationSelectionEntries,
  type CanonicalInteractionSelection,
} from './interaction-selection'

test('canonical visual selections project back to renderer-independent datum references', () => {
  const envelope = {
    visualID: 'customers', dataRevision: 7,
    spec: { interactions: [{
      id: 'point_selection', kind: 'select',
      mappings: [
        { source: { dataset: 'primary', field: 'customer_id' }, targetFieldID: 'customers.customer_id', targetDatasetID: 'customers' },
        { source: { dataset: 'primary', field: 'state' }, targetFieldID: 'customers.state', targetDatasetID: 'customers' },
      ],
    }] },
  } as VisualizationEnvelope
  const selections: CanonicalInteractionSelection[] = [{
    sourceKind: 'visual', sourceId: 'customers', interactionKind: 'point_selection',
    entries: [{ label: 'Customer 2', mappings: [
      { field: 'customers.state', dataset: 'customers', value: 'RJ' },
      { field: 'customers.customer_id', dataset: 'customers', value: 'c-2' },
    ] }],
  }]

  expect(visualizationSelectionEntries(envelope, selections)).toEqual([{
    datum: { dataset: 'primary', dataRevision: 7, identity: { customer_id: 'c-2', state: 'RJ' } },
    label: 'Customer 2',
  }])
  expect(visualizationSelectionEntries(envelope, [{ ...selections[0]!, sourceId: 'other' }])).toEqual([])
})

test('optimistic cross-highlights follow only explicit highlight edges and preserve spatial geometry', () => {
  const target = { visualID: 'target', spec: { kind: 'table', interactions: [] } } as unknown as VisualizationEnvelope
  const source = {
    visualID: 'source',
    spec: { kind: 'cartesian', interactions: [{
      id: 'point_selection', kind: 'select', mappings: [],
      targets: [
        { visualID: 'target', effect: 'highlight' },
        { visualID: 'filtered', effect: 'filter' },
        { visualID: 'unchanged', effect: 'none' },
      ],
    }] },
  } as unknown as VisualizationEnvelope
  const spatialSource = {
    visualID: 'map',
    spec: { kind: 'geographic', interactions: [], spatialInteractions: [{
      id: 'area', gestures: ['box'],
      latitude: { source: { dataset: 'primary', field: 'latitude' }, targetFieldID: 'customers.latitude' },
      longitude: { source: { dataset: 'primary', field: 'longitude' }, targetFieldID: 'customers.longitude' },
      targets: [{ visualID: 'target', effect: 'highlight' }],
    }] },
  } as unknown as VisualizationEnvelope
  const selections: CanonicalInteractionSelection[] = [{
    sourceKind: 'visual', sourceId: 'source', interactionKind: 'point_selection', label: 'São Paulo',
    entries: [{ label: 'São Paulo', mappings: [{ field: 'customers.state', dataset: 'orders', value: 'SP', label: 'São Paulo' }] }],
  }]
  const geometry = { kind: 'box', bounds: { west: -50, south: -25, east: -40, north: -15 } } as const

  const highlights = visualizationHighlightStates(target, { source, map: spatialSource, target }, selections, [{
    visualID: 'map', interactionID: 'area', geometry,
  }])

  expect(highlights).toEqual([
    {
      sourceVisualID: 'source', interactionID: 'point_selection', label: 'São Paulo',
      entries: [{
        label: 'São Paulo',
        mappings: [{ targetFieldID: 'customers.state', targetDatasetID: 'orders', value: 'SP', label: 'São Paulo' }],
      }],
    },
    {
      sourceVisualID: 'map', interactionID: 'area', entries: [], spatialGeometry: geometry,
      spatialLatitudeFieldID: 'customers.latitude', spatialLongitudeFieldID: 'customers.longitude', label: 'Spatial selection',
    },
  ])
  expect(visualizationHighlightStates(
    { ...target, visualID: 'filtered' },
    { source, map: spatialSource },
    selections,
    [],
  )).toEqual([])
})
