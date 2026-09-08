import { expect, test } from 'bun:test'
import { visualDataActionNotice, visualDataSummary, visualDataToDelimited } from './visual-modal-actions'

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

test('delimited visual data neutralizes formula-like strings but preserves numeric values', () => {
  expect(visualDataToDelimited({
    columns: [{ key: 'value', label: 'Metric' }],
    rows: [
      { value: '=SUM(A1:A2)' },
      { value: '+1' },
      { value: '-1' },
      { value: '@cmd' },
      { value: ' \t=indirect' },
      { value: -42 },
      { value: '-42' },
      { value: null },
    ],
  }, ',')).toBe([
    'Metric',
    "'=SUM(A1:A2)",
    "'+1",
    "'-1",
    "'@cmd",
    "' \t=indirect",
    '-42',
    "'-42",
    '',
  ].join('\n'))
})

test('delimited visual data quotes CSV boundaries and sanitizes control-prefixed TSV cells', () => {
  expect(visualDataToDelimited({
    columns: [{ key: 'text', label: 'Text' }, { key: 'empty', label: '\r=Empty' }],
    rows: [{ text: 'comma, newline\nvalue "quoted"', empty: null }],
  }, ',')).toBe(`Text,"'\r=Empty"
"comma, newline
value ""quoted""",`)

  expect(visualDataToDelimited({
    columns: [{ key: 'value', label: '\t=Header' }],
    rows: [{ value: '\r+formula\nnext' }],
  }, '\t')).toBe("' =Header\n' +formula next")
})
