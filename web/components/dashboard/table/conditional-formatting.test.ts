import { expect, test } from 'bun:test'
import type { TableColumn } from './types'
import { conditionalCellAppearance } from './conditional-formatting'

test('table conditional formatting uses safe background tokens and redundant accessible cues', () => {
  const column = {
    key: 'revenue', label: 'Revenue',
    conditionalFormatting: [
      {
        id: 'status-background', target: 'cell_background', field: { dataset: 'primary', field: 'revenue' },
        rule: {
          kind: 'field', source: { dataset: 'primary', field: 'status' },
          values: { late: { color: 'danger', icon: 'warning' } },
          nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'circle' },
        },
      },
      {
        id: 'revenue-foreground', target: 'cell_foreground', field: { dataset: 'primary', field: 'revenue' },
        rule: {
          kind: 'rules', rules: [{ operator: 'less_than', value: 50, style: { color: 'warning', icon: 'arrow_down' } }],
          nullStyle: { icon: 'warning' }, defaultStyle: { color: 'ink', icon: 'arrow_up' },
        },
      },
    ],
  } as TableColumn

  expect(conditionalCellAppearance({ status: 'late', revenue: 35 }, column)).toEqual({
    background: 'var(--lv-bg-danger-muted)',
    foreground: 'var(--lv-fg-default)',
    iconLabel: 'warning',
  })
})

test('table gradients retain a continuous governed domain without raw CSS input', () => {
  const column = {
    key: 'revenue', label: 'Revenue',
    conditionalFormatting: [{
      id: 'revenue-gradient', target: 'cell_background', field: { dataset: 'primary', field: 'revenue' },
      rule: {
        kind: 'gradient', minimum: 0, maximum: 100,
        low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
      },
    }],
  } as TableColumn

  expect(conditionalCellAppearance({ revenue: 25 }, column)).toEqual({
    background: 'color-mix(in srgb, color-mix(in srgb, var(--lv-fg-danger) 75%, var(--lv-fg-success)) 20%, transparent)',
    foreground: 'var(--lv-fg-default)',
  })
})
