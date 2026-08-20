import { expect, test } from 'bun:test'
import { ReportTableColumnController, ReportTableFormattingController, ReportTableSelectionController, ReportTableVirtualizationController } from './report-table-controller'

test('report table virtualization computes bounded windows and block starts', () => {
  const controller = new ReportTableVirtualizationController()
  controller.setViewport(320, 128)
  expect(controller.visibleRange(1000, 32)).toEqual({ first: 8, last: 16 })
  expect(controller.desiredStarts(300, 1000, 100)).toEqual([200, 300, 400])
  expect(controller.allBlockStarts(300, 100)).toEqual([200, 300, 400])
})

test('report table column sizing preserves configured minimums', () => {
  const controller = new ReportTableColumnController(() => ({ order_id: 80, revenue: 180 }))
  expect(controller.pixelWidth({ key: 'order_id', label: 'Order ID' })).toBe(160)
  expect(controller.pixelWidth({ key: 'revenue', label: 'Revenue', align: 'right' })).toBe(180)
  expect(controller.tableWidth([{ key: 'order_id', label: 'Order ID' }, { key: 'revenue', label: 'Revenue' }])).toBe(340)
})

test('report table selection controller derives labels and actions', () => {
  const controller = new ReportTableSelectionController()
  expect(controller.action(false, 1, { metaKey: false, ctrlKey: false })).toEqual({ action: 'replace', toggle: false })
  expect(controller.action(true, 1, { metaKey: false, ctrlKey: false })).toEqual({ action: 'set', toggle: true })
  expect(controller.count([{ label: 'Order 1' }])).toBe(1)
  expect(controller.labels([{ label: 'Order 1' }])).toEqual(['Order 1'])
})

test('report table formatting controller keeps governed tone and scale semantics', () => {
  const controller = new ReportTableFormattingController()
  const column = { key: 'revenue', label: 'Revenue', formatting: [
    { kind: 'background_scale' as const, min: 0, max: 100, highColor: 'warning' },
    { kind: 'data_bar' as const, min: 0, max: 100, color: 'accent' },
  ] }
  expect(controller.percent(50, column.formatting[0])).toBe(50)
  expect(controller.background(50, column)?.highColor).toBe('warning')
  expect(controller.dataBar(column)?.color).toBe('accent')
  expect(controller.color('danger')).toBe('var(--lv-fg-danger)')
})
