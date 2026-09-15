import { expect, test } from 'bun:test'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { hasCompiledBuilderPreview } from './builder-preview-readiness'

const preview = (kind: string) => ({ spec: { kind } }) as VisualizationEnvelope

test('compiled authored series, point and geographic previews bypass creation defaults', () => {
  for (const [type, kind] of [['line', 'cartesian'], ['area', 'cartesian'], ['bar', 'cartesian'], ['column', 'cartesian'], ['scatter', 'point'], ['map', 'geographic']] as const) {
    expect(hasCompiledBuilderPreview({ type }, type, true, preview(kind))).toBe(true)
  }
})

test('stale or failed previews never hide missing-field validation', () => {
  expect(hasCompiledBuilderPreview({ type: 'map' }, 'map', false, preview('geographic'))).toBe(false)
  expect(hasCompiledBuilderPreview({ type: 'map', previewError: 'Missing latitude' }, 'map', true, preview('geographic'))).toBe(false)
  expect(hasCompiledBuilderPreview({ type: 'bar' }, 'map', true, preview('cartesian'))).toBe(false)
  expect(hasCompiledBuilderPreview({ type: 'map' }, 'map', true, preview('cartesian'))).toBe(false)
  expect(hasCompiledBuilderPreview({ type: 'map' }, 'map', true, undefined)).toBe(false)
  expect(hasCompiledBuilderPreview({ type: 'kpi' }, 'kpi', true, preview('kpi'))).toBe(false)
})
