import { expect, test } from 'bun:test'

import { clampFittedScale, layoutStorageKey, reportViewPathname, zoomScaleStorageKey, zoomStorageKey } from './report-view-state'

test('fitted report views never upscale past native resolution', () => {
  expect(clampFittedScale(0.75)).toBe(0.75)
  expect(clampFittedScale(1)).toBe(1)
  expect(clampFittedScale(1.263)).toBe(1)
})

test('view preference keys are safe when no browser location exists', () => {
  const pathname = reportViewPathname()
  expect(layoutStorageKey()).toBe(`leapview-report-layout:${pathname}`)
  expect(zoomStorageKey()).toBe(`leapview-report-zoom:${pathname}`)
  expect(zoomScaleStorageKey()).toBe(`leapview-report-zoom-scale:${pathname}`)
})
