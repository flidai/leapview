import { expect, test } from 'bun:test'
import type { ResourceAssetSummarySignal } from '../../generated/signals'
import { modelExploreHref } from './explore-model'

test('model entry opens the canonical explorer with a closed return context', () => {
  const href = modelExploreHref({ id: 'model:orders', key: 'orders', title: 'Orders', type: 'model', typeLabel: 'Model' } as ResourceAssetSummarySignal)
  expect(href).toBe('/explore?object=model%3Aorders&returnSurface=model&returnAsset=model%3Aorders&returnSection=data')
})

test('non-model assets have no model explorer entry', () => {
  expect(modelExploreHref({ id: 'source:orders', key: 'orders', title: 'Orders', type: 'source', typeLabel: 'Source' } as ResourceAssetSummarySignal)).toBeUndefined()
})
