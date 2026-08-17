import { expect, test } from 'bun:test'
import {
  UI_ROW_SELECTION_FIELD,
  buildRowSelectionCommand,
  rowClickSelectionAction,
  rowIsSelected,
  rowSelectionFromEntries,
  selectionLabels,
  selectedRowCount,
} from './selection'
import type { InteractionConfig, InteractionSelectionEntry, TableRow } from './types'

const semanticInteraction: InteractionConfig = {
  kind: 'row_selection',
  toggle: true,
  mappings: [
    { field: 'orders.order_id', value: 'order_id' },
    { field: 'orders.status', value: 'status', label: 'status_label' },
  ],
}

test('buildRowSelectionCommand emits configured semantic mappings', () => {
  const command = buildRowSelectionCommand({
    sourceId: 'orders_table',
    interaction: semanticInteraction,
    key: 'o1',
    row: { order_id: 'o1', status: 'delivered', status_label: 'Delivered' },
    selectionAction: { action: 'replace', toggle: false },
  })

  expect(command).toEqual({
    sourceKind: 'visual',
    sourceId: 'orders_table',
    interactionKind: 'row_selection',
    action: 'replace',
    toggle: false,
    mappings: [
      { field: 'orders.order_id', value: 'o1', label: 'o1' },
      { field: 'orders.status', value: 'delivered', label: 'Delivered' },
    ],
  })
})

test('buildRowSelectionCommand preserves typed values and mapping dataset and grain', () => {
  const command = buildRowSelectionCommand({
    sourceId: 'ratings_table',
    interaction: {
      kind: 'row_selection',
      mappings: [
        { field: 'activity_date', grain: 'month', value: 'month', label: 'month_label' },
        { field: 'ratings.rating_bucket', dataset: 'ratings', value: 'bucket', label: 'bucket_label' },
        { field: 'ratings.is_verified', dataset: 'ratings', value: 'verified' },
      ],
    },
    key: 'rating-1',
    row: {
      month: '2026-07-01',
      month_label: 'July 2026',
      bucket: 0,
      bucket_label: 'No rating',
      verified: false,
    },
    selectionAction: { action: 'replace', toggle: false },
  })

  expect(command?.mappings).toEqual([
    { field: 'activity_date', grain: 'month', value: '2026-07-01', label: 'July 2026' },
    { field: 'ratings.rating_bucket', dataset: 'ratings', value: 0, label: 'No rating' },
    { field: 'ratings.is_verified', dataset: 'ratings', value: false, label: 'false' },
  ])
})

test('buildRowSelectionCommand emits UI-only row key mappings without semantic mappings', () => {
  const command = buildRowSelectionCommand({
    sourceId: 'orders_table',
    interaction: { kind: 'row_selection', toggle: true, mappings: [] },
    key: 'row-42',
    row: { label: 'ignored' },
    selectionAction: { action: 'set', toggle: true },
  })

  expect(command).toEqual({
    sourceKind: 'visual',
    sourceId: 'orders_table',
    interactionKind: 'row_selection',
    action: 'set',
    toggle: true,
    mappings: [{ field: UI_ROW_SELECTION_FIELD, value: 'row-42', label: 'row-42' }],
  })
})

test('buildRowSelectionCommand rejects incomplete semantic mapping payloads', () => {
  const command = buildRowSelectionCommand({
    sourceId: 'orders_table',
    interaction: semanticInteraction,
    key: 'o1',
    row: { order_id: 'o1', status_label: 'Delivered' },
    selectionAction: { action: 'replace', toggle: false },
  })

  expect(command).toBe(null)
})

test('rowClickSelectionAction keeps the table gesture matrix server-driven', () => {
  expect(rowClickSelectionAction({ selected: false, selectedCount: 0, metaKey: false, ctrlKey: false })).toEqual({
    action: 'replace',
    toggle: false,
  })
  expect(rowClickSelectionAction({ selected: true, selectedCount: 1, metaKey: false, ctrlKey: false })).toEqual({
    action: 'set',
    toggle: true,
  })
  expect(rowClickSelectionAction({ selected: true, selectedCount: 3, metaKey: false, ctrlKey: false })).toEqual({
    action: 'replace',
    toggle: false,
  })
  expect(rowClickSelectionAction({ selected: false, selectedCount: 1, metaKey: false, ctrlKey: true })).toEqual({
    action: 'set',
    toggle: true,
  })
  expect(rowClickSelectionAction({ selected: false, selectedCount: 1, metaKey: true, ctrlKey: false })).toEqual({
    action: 'set',
    toggle: true,
  })
})

test('rowSelectionFromEntries projects semantic selection entries to loaded rows', () => {
  const rows: Array<{ row: TableRow; key: string }> = [
    { key: 'o1', row: { order_id: 'o1', status: 'delivered' } },
    { key: 'o2', row: { order_id: 'o2', status: 'shipped' } },
  ]
  const selection: InteractionSelectionEntry[] = [
    {
      mappings: [
        { field: 'orders.order_id', value: 'o2', label: 'o2' },
        { field: 'orders.status', value: 'shipped', label: 'Shipped' },
      ],
      label: 'o2',
    },
  ]

  expect(rowSelectionFromEntries(rows, semanticInteraction, selection)).toEqual({ o2: true })
  expect(rowIsSelected(rows[1].row, rows[1].key, semanticInteraction, selection)).toBe(true)
})

test('row selection projection includes dataset, grain, and scalar type in mapping identity', () => {
  const interaction: InteractionConfig = {
    mappings: [
      { field: 'activity_date', grain: 'month', value: 'month' },
      { field: 'rating_bucket', dataset: 'ratings', value: 'bucket' },
    ],
  }
  const row = { month: '2026-07-01', bucket: 1 }

  expect(rowIsSelected(row, 'row-1', interaction, [{ mappings: [
    { field: 'activity_date', grain: 'month', value: '2026-07-01' },
    { field: 'rating_bucket', dataset: 'ratings', value: 1 },
  ] }])).toBe(true)
  expect(rowIsSelected(row, 'row-1', interaction, [{ mappings: [
    { field: 'activity_date', grain: 'month', value: '2026-07-01' },
    { field: 'rating_bucket', dataset: 'ratings', value: '1' },
  ] }])).toBe(false)
  expect(rowIsSelected(row, 'row-1', interaction, [{ mappings: [
    { field: 'activity_date', grain: 'day', value: '2026-07-01' },
    { field: 'rating_bucket', dataset: 'ratings', value: 1 },
  ] }])).toBe(false)
  expect(rowIsSelected(row, 'row-1', interaction, [{ mappings: [
    { field: 'activity_date', grain: 'month', value: '2026-07-01' },
    { field: 'rating_bucket', dataset: 'tags', value: 1 },
  ] }])).toBe(false)
})

test('rowSelectionFromEntries projects UI-only row-key entries to loaded rows', () => {
  const rows: Array<{ row: TableRow; key: string }> = [
    { key: 'row-1', row: { label: 'First' } },
    { key: 'row-2', row: { label: 'Second' } },
  ]
  const selection: InteractionSelectionEntry[] = [
    { mappings: [{ field: UI_ROW_SELECTION_FIELD, value: 'row-1', label: 'row-1' }], label: 'row-1' },
  ]

  expect(rowSelectionFromEntries(rows, { kind: 'row_selection', mappings: [] }, selection)).toEqual({ 'row-1': true })
  expect(rowIsSelected(rows[1].row, rows[1].key, { kind: 'row_selection', mappings: [] }, selection)).toBe(false)
})

test('selected count and labels come only from server selection entries', () => {
  const selection: InteractionSelectionEntry[] = [
    { label: 'Delivered' },
    { mappings: [{ field: 'orders.order_id', value: 'o2', label: 'o2' }] },
  ]

  expect(selectedRowCount(selection)).toBe(2)
  expect(selectionLabels(selection)).toEqual(['Delivered', 'o2'])
  expect(selectedRowCount(undefined)).toBe(0)
  expect(selectionLabels(undefined)).toEqual([])
})
