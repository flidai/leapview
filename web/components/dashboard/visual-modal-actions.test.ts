import { expect, test } from 'bun:test'
import { visualDataActionNotice, visualDataSummary } from './visual-modal-actions'

test('data action notices identify partial and truncated previews', () => {
  expect(visualDataActionNotice({ dataStatus: 'Source data is partial. No data rows are available.' }, 'copy-data'))
    .toContain('Source data is partial.')
  expect(visualDataActionNotice({ dataStatus: 'Data is truncated; showing 100 rows.' }, 'export-csv'))
    .toContain('Data is truncated; showing 100 rows.')
  expect(visualDataActionNotice({ truncated: true }, 'export-csv'))
    .toContain('may be partial or truncated')
})

test('empty visual data still reports supplied completeness status', () => {
  expect(visualDataSummary({ rows: [], totalRows: 0, dataStatus: 'Source data is partial. No data rows are available.' }))
    .toBe('Source data is partial. No data rows are available.')
  expect(visualDataSummary({ rows: [], totalRows: 0, dataStatus: 'Source data is truncated. No data rows are available.' }))
    .toContain('Source data is truncated.')
})
