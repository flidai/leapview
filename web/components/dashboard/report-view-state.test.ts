import { expect, test } from 'bun:test'

import { clampFittedScale } from './report-view-state'

test('fitted report views never upscale past native resolution', () => {
  expect(clampFittedScale(0.75)).toBe(0.75)
  expect(clampFittedScale(1)).toBe(1)
  expect(clampFittedScale(1.263)).toBe(1)
})
