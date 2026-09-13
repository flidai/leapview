import { expect, test } from 'bun:test'
import type { DashboardFilterContract } from '../../generated/signals'
import { canRequireFilter, filterControlChoices } from './builder-filter-settings'

test('boolean filters offer typed selections instead of text comparisons', () => {
  expect(filterControlChoices('boolean', 'singleSelect').map(([control]) => control)).toEqual(['multiSelect', 'singleSelect'])
  expect(filterControlChoices('string', 'singleSelect').map(([control]) => control)).toContain('text')
})

test('required filters need defaults on every binding while existing required settings remain removable', () => {
  const contract = (defaults: string[]) => ({ bindings: Object.fromEntries(defaults.map((kind, index) => [index, { filter: 'state', default: { kind } }])) }) as DashboardFilterContract
  const filter = { id: 'state', required: false }
  expect(canRequireFilter(filter, contract([]))).toBe(false)
  expect(canRequireFilter(filter, contract(['unfiltered']))).toBe(false)
  expect(canRequireFilter(filter, contract(['set', 'unfiltered']))).toBe(false)
  expect(canRequireFilter(filter, contract(['set', 'comparison']))).toBe(true)
  expect(canRequireFilter({ ...filter, required: true }, contract([]))).toBe(true)
})
