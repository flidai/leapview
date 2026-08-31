import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { tableSignal } from './tanstack'

test('TanStack adapter derives semantic row interactions from the typed IR', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'orders', rendererID: 'tanstack', specRevision: 'sha256:test', dataRevision: 3,
    spec: {
      kind: 'table', title: 'Orders',
      datasets: [{ id: 'primary', fields: [
        { id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] }],
      dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, accessibility: { title: 'Orders', description: 'Orders' },
      interactions: [{ id: 'row_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['revenue'], mappings: [
        { source: { dataset: 'primary', field: 'order_id' }, targetFieldID: 'orders.order_id', targetDatasetID: 'orders', label: { dataset: 'primary', field: 'order_id' } },
      ] }],
      conditionalFormatting: [{
        id: 'revenue-health', target: 'cell_background', field: { dataset: 'primary', field: 'revenue' },
        rule: {
          kind: 'rules', rules: [{ operator: 'less_than', value: 50, style: { color: 'danger', icon: 'warning' } }],
          nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'circle' },
        },
      }],
      columns: [
        { field: { dataset: 'primary', field: 'order_id' }, label: 'Order', formatting: [] },
        { field: { dataset: 'primary', field: 'revenue' }, label: 'Revenue', formatting: [{ kind: 'data_bar', minimum: 0, maximum: 100, color: 'accent' }] },
      ],
      presentation: { rowHeight: 34, striped: true, showHeader: true },
    },
    dataState: {
      kind: 'windowed', specRevision: 'sha256:test', dataRevision: 3, generation: 1,
      schema: { id: 'primary', fields: [
        { id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] },
      cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 1000, chunkSize: 50, resetVersion: 2,
      sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }],
      blocks: { a: { id: 'a', start: 0, rows: [['o1', 42]], requestSeq: 4, resetVersion: 2, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } },
    },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  expect(tableSignal(envelope).interaction).toEqual({
    kind: 'row_selection', toggle: true, targets: ['revenue'],
    mappings: [{ field: 'orders.order_id', dataset: 'orders', value: 'order_id', label: 'order_id' }],
  })
  expect(tableSignal(envelope).columns[1]?.formatting).toEqual([{ kind: 'data_bar', min: 0, max: 100, color: 'accent' }])
  expect(tableSignal(envelope).columns[1]?.conditionalFormatting?.[0]).toMatchObject({
    id: 'revenue-health', target: 'cell_background',
  })
})

test('TanStack adapter leaves row interaction disabled when the IR declares none', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'orders', rendererID: 'tanstack', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'table', title: 'Orders', datasets: [{ id: 'primary', fields: [{ id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' }] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [],
      columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', formatting: [] }], presentation: { rowHeight: 34, striped: true, showHeader: true },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['order_id'], rows: [['o1']], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  expect(tableSignal(envelope).interaction).toBeUndefined()
})

test('TanStack adapter preserves sparse window block identities', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'orders', rendererID: 'tanstack', specRevision: 'sha256:test', dataRevision: 3,
    spec: {
      kind: 'table', title: 'Orders', datasets: [{ id: 'primary', fields: [{ id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' }] }],
      dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [],
      columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', formatting: [] }], presentation: { rowHeight: 28, striped: true, showHeader: true },
    },
    dataState: {
      kind: 'windowed', specRevision: 'sha256:test', dataRevision: 3, generation: 1,
      schema: { id: 'primary', fields: [{ id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' }] },
      cardinality: { kind: 'lower_bound', count: 600 }, availableRows: 1000, rowCap: 1000, chunkSize: 50, resetVersion: 2,
      sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }],
      blocks: { c: { id: 'c', start: 450, rows: [['o451']], requestSeq: 8, resetVersion: 2, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } },
    },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const table = tableSignal(envelope)
  expect(table.blocks.c).toMatchObject({ start: 450, requestSeq: 8, rows: [{ order_id: 'o451' }] })
  expect(table.blocks.a).toMatchObject({ start: 0, requestSeq: 0, rows: [] })
  expect(table.blocks.b).toMatchObject({ start: 50, requestSeq: 0, rows: [] })
})

test('TanStack matrix adapter renders dynamic window schema columns with compiled formatting', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'matrix', rendererID: 'tanstack', specRevision: 'sha256:matrix', dataRevision: 2,
    spec: {
      kind: 'matrix', title: 'Matrix', datasets: [{ id: 'primary', fields: [
        { id: 'state', role: 'dimension', dataType: 'string', nullable: true, label: 'State' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: true, label: 'Revenue' },
      ] }], dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, accessibility: { title: 'Matrix', description: 'Matrix' }, interactions: [],
      rows: [{ dataset: 'primary', field: 'state' }], columns: [], metrics: [{ dataset: 'primary', field: 'revenue' }],
      metricFormatting: { revenue: [{ kind: 'data_bar', minimum: 0, maximum: 100, color: 'accent' }] },
      presentation: { rowHeight: 34, striped: true, showHeader: true },
    },
    dataState: {
      kind: 'windowed', specRevision: 'sha256:matrix', dataRevision: 2, generation: 1,
      schema: { id: 'primary', fields: [
        { id: 'state', role: 'identity', dataType: 'string', nullable: true, label: 'State', grid: { formatting: [] } },
        { id: 'delivered__revenue', role: 'metric', dataType: 'decimal', nullable: true, label: 'Delivered revenue', grid: { group: 'Delivered', metric: 'revenue', columnValue: 'delivered', formatting: [] } },
      ] },
      cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 1000, chunkSize: 50, resetVersion: 1,
      sort: [{ field: { dataset: 'primary', field: 'state' }, direction: 'ascending' }],
      blocks: { a: { id: 'a', start: 0, rows: [['SP', 42]], requestSeq: 1, resetVersion: 1, sort: [{ field: { dataset: 'primary', field: 'state' }, direction: 'ascending' }] } },
    }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const table = tableSignal(envelope)
  expect(table.columns.map((column) => column.key)).toEqual(['state', 'delivered__revenue'])
	expect(table.columns[1]).toMatchObject({ group: 'Delivered', metric: 'revenue', columnValue: 'delivered', formatting: [{ kind: 'data_bar', min: 0, max: 100, color: 'accent' }] })
})
