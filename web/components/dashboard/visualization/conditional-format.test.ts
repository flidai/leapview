import { expect, test } from 'bun:test'
import type { VisualizationConditionalFormat } from '../../../generated/visualization'
import { conditionalStyleColor, contrastTextColor, resolveConditionalFormat } from './conditional-format'

const gradient = {
  id: 'revenue-gradient',
  target: 'mark_fill',
  field: { dataset: 'primary', field: 'revenue' },
  rule: {
    kind: 'gradient', minimum: 0, maximum: 100,
    low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
  },
} as VisualizationConditionalFormat

test('conditional formatting resolves deterministic gradients with explicit null and invalid outcomes', () => {
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', 25])).toEqual({
    style: { gradient: { low: 'danger', high: 'success', ratio: 0.25 } },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', 150])).toEqual({
    style: { gradient: { low: 'danger', high: 'success', ratio: 1 } },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', '25.00'])).toEqual({
    style: { gradient: { low: 'danger', high: 'success', ratio: 0.25 } },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', null])).toEqual({
    style: { color: 'neutral' },
    outcome: 'null',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', 'secret'])).toEqual({
    style: { color: 'neutral' },
    outcome: 'invalid',
    diagnostic: 'conditional formatting "revenue-gradient" expected a finite numeric value',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', '1e2'])).toEqual({
    style: { color: 'neutral' },
    outcome: 'invalid',
    diagnostic: 'conditional formatting "revenue-gradient" expected a finite numeric value',
  })
  expect(resolveConditionalFormat(gradient, ['label', 'revenue'], ['A', `0.${'0'.repeat(400)}1`])).toEqual({
    style: { color: 'neutral' },
    outcome: 'invalid',
    diagnostic: 'conditional formatting "revenue-gradient" expected a finite numeric value',
  })
})

test('conditional formatting uses authored first-match rule order and redundant cues', () => {
  const format = {
    id: 'health-rules',
    target: 'mark_fill',
    field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules',
      rules: [
        { operator: 'greater_or_equal', value: 0, style: { color: 'warning', icon: 'circle' } },
        { operator: 'greater_or_equal', value: 80, style: { color: 'success', icon: 'arrow_up' } },
      ],
      nullStyle: { icon: 'warning' },
      defaultStyle: { color: 'danger', icon: 'arrow_down' },
    },
  } as VisualizationConditionalFormat
  expect(resolveConditionalFormat(format, ['value'], [90])).toEqual({
    style: { color: 'warning', icon: 'circle' },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(format, ['value'], ['90.00'])).toEqual({
    style: { color: 'warning', icon: 'circle' },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(format, ['value'], [-1])).toEqual({
    style: { color: 'danger', icon: 'arrow_down' },
    outcome: 'default',
  })
  expect(resolveConditionalFormat(format, ['value'], ['90.25'])).toEqual({
    style: { color: 'warning', icon: 'circle' },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(format, ['value'], ['1e2'])).toEqual({
    style: { icon: 'warning' },
    outcome: 'invalid',
    diagnostic: 'conditional formatting "health-rules" expected a finite numeric value',
  })
})

test('numeric rules accept canonical Decimal transport strings', () => {
  const format = {
    id: 'variance-rules',
    target: 'mark_fill',
    field: { dataset: 'primary', field: 'variance' },
    rule: {
      kind: 'rules',
      rules: [
        { operator: 'greater_or_equal', value: 0, style: { color: 'success', icon: 'arrow_up' } },
        { operator: 'less_than', value: 0, style: { color: 'danger', icon: 'arrow_down' } },
      ],
      nullStyle: { color: 'neutral', icon: 'square' },
      defaultStyle: { color: 'neutral', icon: 'circle' },
    },
  } as VisualizationConditionalFormat

  expect(resolveConditionalFormat(format, ['variance'], ['90029.70']).style.color).toBe('success')
  expect(resolveConditionalFormat(format, ['variance'], ['-3150255.69']).style.color).toBe('danger')
  expect(resolveConditionalFormat(format, ['variance'], ['1e3']).outcome).toBe('invalid')
})

test('bound-field formatting never interprets field values as renderer colors', () => {
  const format = {
    id: 'status-values',
    target: 'cell_background',
    field: { dataset: 'primary', field: 'revenue' },
    rule: {
      kind: 'field',
      source: { dataset: 'primary', field: 'status' },
      values: { late: { color: 'danger', icon: 'warning' } },
      nullStyle: { icon: 'warning' },
      defaultStyle: { color: 'neutral', icon: 'circle' },
    },
  } as VisualizationConditionalFormat
  expect(resolveConditionalFormat(format, ['status', 'revenue'], ['late', 10])).toEqual({
    style: { color: 'danger', icon: 'warning' },
    outcome: 'matched',
  })
  expect(resolveConditionalFormat(format, ['status', 'revenue'], ['#ff0000', 10])).toEqual({
    style: { color: 'neutral', icon: 'circle' },
    outcome: 'default',
  })
  expect(resolveConditionalFormat(format, ['status', 'revenue'], [null, 10])).toEqual({
    style: { icon: 'warning' },
    outcome: 'null',
  })
})

test('conditional colors interpolate resolved theme colors and choose readable foregrounds', () => {
  const resolve = (intent: string) => ({ danger: '#cc0000', success: '#00cc00' })[intent] ?? '#777777'
  expect(conditionalStyleColor({ gradient: { low: 'danger', high: 'success', ratio: 0.25 } }, resolve)).toBe('rgb(153 51 0)')
  expect(contrastTextColor('#ffffff', ['#777777', '#111111'])).toBe('#111111')
  expect(contrastTextColor('#111111', ['#777777', '#ffffff'])).toBe('#ffffff')
})
