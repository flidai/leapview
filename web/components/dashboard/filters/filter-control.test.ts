import { expect, test } from 'bun:test'
import type { DashboardFilterOptionItem, DashboardFilterValue } from '../../../generated/signals'
import { filterOptionKey, filterOptionToggleExpression } from './filter-control'

const value: DashboardFilterValue = { kind: 'string', value: 'paid' }
const option: DashboardFilterOptionItem = { value, label: 'Paid', null: false, selected: false, available: true }
const nullOption: DashboardFilterOptionItem = { label: '(null)', null: true, selected: false, available: true }

test('null option has a stable identity distinct from typed values', () => {
  expect(filterOptionKey(nullOption)).not.toBe(filterOptionKey(option))
})

test('null option replaces values and a typed choice replaces null deterministically', () => {
  const selected = { kind: 'set', operator: 'in', values: [value] } as const
  expect(filterOptionToggleExpression(selected, nullOption, true)).toEqual({ kind: 'null_check', operator: 'is_null' })
  expect(filterOptionToggleExpression({ kind: 'null_check', operator: 'is_null' }, option, true)).toEqual({
    kind: 'set', operator: 'in', values: [value],
  })
  expect(filterOptionToggleExpression({ kind: 'null_check', operator: 'is_null' }, nullOption, true)).toEqual({ kind: 'unfiltered' })
})
