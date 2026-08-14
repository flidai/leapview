import { expect, test } from 'bun:test'
import { readFile } from 'node:fs/promises'
import { hasMixedSpatialPrecision } from './spatial_precision_summary'

test('UI framework QA gives the managed dev task its full readiness budget', async () => {
  const source = await readFile('scripts/qa_ui_framework.ts', 'utf8')

  expect(source).toContain("LEAPVIEW_DEV_READY_ATTEMPTS: String(managedServerReadyAttempts)")
})

test('spatial route QA rejects only concurrently visible precision families', () => {
  expect(hasMixedSpatialPrecision('View visible map data (611 visible features: 610 raw points, 1 aggregate cell; 14833 total coordinates)')).toBe(true)
  expect(hasMixedSpatialPrecision('View visible map data (0 visible features: 0 raw points, 0 aggregate cells; 14833 total coordinates)')).toBe(false)
  expect(hasMixedSpatialPrecision('View visible map data (12 visible features: 0 raw points, 12 aggregate cells; 14833 total coordinates)')).toBe(false)
  expect(hasMixedSpatialPrecision('View visible map data (12 visible features: 12 raw points, 0 aggregate cells; 14833 total coordinates)')).toBe(false)
})
