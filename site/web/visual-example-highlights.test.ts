import { expect, test } from 'bun:test'
import { visualExampleHighlightLines } from './visual-example-highlights'

const source = `visuals:
  revenue_line_step:
    type: line
    shape: category_series_value
    query:
      dimensions:
        - field: order_month
      metrics:
        - field: revenue
    options:
      smooth: false
      step: middle
      showSymbols: false
      dataZoom: true`

test('maps generated field paths to their exact YAML source lines', () => {
  expect(visualExampleHighlightLines(source, ['options.dataZoom', 'options.showSymbols', 'options.step'])).toEqual([12, 13, 14])
})

test('includes nested lines when a generated field identifies a YAML collection', () => {
  expect(visualExampleHighlightLines(source, ['shape', 'query.dimensions', 'query.metrics'])).toEqual([4, 6, 7, 8, 9])
})

test('ignores unknown paths instead of highlighting a similarly named key', () => {
  expect(visualExampleHighlightLines(source, ['dimensions', 'options.missing'])).toEqual([])
})
