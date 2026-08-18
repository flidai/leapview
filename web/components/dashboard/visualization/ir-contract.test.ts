import { expect, test } from 'bun:test'

import type { VisualizationDataState, VisualizationEnvelope, VisualizationSpec } from '../../../generated/visualization'
import validateEnvelope from '../../../generated/visualization/validate'
import visualDocumentation from '../../../../docs/visuals/examples.gen.json'

function specificationKind(spec: VisualizationSpec): string {
  switch (spec.kind) {
    case 'cartesian':
      return `${spec.kind}:${spec.mark}`
    case 'proportional':
      return `${spec.kind}:${spec.mark}`
    case 'hierarchy':
      return `${spec.kind}:${spec.mark}`
    case 'polar':
      return `${spec.kind}:${spec.mark}`
    case 'table':
      return `${spec.kind}:${spec.columns.length}`
    case 'matrix':
      return `${spec.kind}:${spec.metrics.length}`
    case 'pivot':
      return `${spec.kind}:${spec.metrics.length}`
    case 'kpi':
      return `${spec.kind}:${spec.value.field}`
    case 'geographic':
      return `${spec.kind}:${spec.layers.length}`
    case 'custom':
      return `${spec.kind}:${spec.engine}`
    default: {
      const unsupported: never = spec
      return unsupported
    }
  }
}

function dataStateKind(state: VisualizationDataState): string {
  switch (state.kind) {
    case 'inline':
      return `${state.kind}:${state.datasets.length}`
    case 'windowed':
      return `${state.kind}:${Object.keys(state.blocks).length}`
    default: {
      const unsupported: never = state
      return unsupported
    }
  }
}

test('generated visualization unions decode the shared conformance fixtures', async () => {
  const fixtures = [
    ['cartesian-inline.json', 'cartesian:line', 'inline:1'],
    ['table-windowed.json', 'table:2', 'windowed:1'],
  ] as const

  for (const [name, expectedSpec, expectedState] of fixtures) {
    const path = new URL(`../../../../api/visualization/conformance/${name}`, import.meta.url)
    const envelope = (await Bun.file(path).json()) as VisualizationEnvelope
    expect(specificationKind(envelope.spec)).toBe(expectedSpec)
    expect(dataStateKind(envelope.dataState)).toBe(expectedState)
    expect(envelope.dataState.specRevision).toBe(envelope.specRevision)
    expect(envelope.dataState.dataRevision).toBe(envelope.dataRevision)
  }
})

test('the generated contract is a closed union of renderer-independent specifications', () => {
  const kinds = [
    'cartesian',
    'proportional',
    'hierarchy',
    'polar',
    'table',
    'matrix',
    'pivot',
    'kpi',
    'geographic',
    'custom',
  ] as const satisfies ReadonlyArray<VisualizationSpec['kind']>

  expect(kinds).toHaveLength(10)
})

test('standalone JSON Schema validation fails closed', async () => {
  const path = new URL('../../../../api/visualization/conformance/cartesian-inline.json', import.meta.url)
  const envelope = await Bun.file(path).json()
  expect(validateEnvelope(envelope)).toBe(true)
  expect(validateEnvelope({ ...envelope, schemaVersion: 1 })).toBe(false)
  expect(validateEnvelope({ ...envelope, schemaVersion: 2 })).toBe(false)
  expect(validateEnvelope({ ...envelope, schemaVersion: 3 })).toBe(false)
  expect(validateEnvelope({ ...envelope, legacyOptions: {} })).toBe(false)
  expect(validateEnvelope({ ...envelope, spec: { ...envelope.spec, kind: 'unknown' } })).toBe(false)
})

test('every generated visual documentation envelope satisfies the public contract', () => {
  for (const [document, examples] of Object.entries(visualDocumentation.documents)) {
    for (const example of examples) {
      expect(validateEnvelope(example), `${document}/${example.visualID}`).toBe(true)
    }
  }
})
