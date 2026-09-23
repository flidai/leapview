import { expect, test } from 'bun:test'
import { explorationSpecFromCommand } from './data-explorer-spec'

test('canonical exploration specs omit absent optional signal members', () => {
  const spec = explorationSpecFromCommand({
    spec: {
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      dimensions: [],
      metrics: [],
      filters: [],
      sort: [],
      limit: 100,
    },
    semanticModelId: 'semantic-model:sales',
    datasetId: '',
    dimensions: [],
    metrics: [],
    filters: [],
    sort: [],
    limit: 100,
    requestSeq: 0,
    resetVersion: 0,
    columnWidths: {},
  })

  expect(Object.hasOwn(spec, 'datasetId')).toBe(false)
  expect(Object.hasOwn(spec, 'time')).toBe(false)
  expect(JSON.stringify(spec)).not.toContain('"time"')
})
