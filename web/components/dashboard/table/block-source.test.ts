import { describe, expect, test } from 'bun:test'

import { emptyTable, normalizeTable, preserveCardinality } from './block-source'

describe('progressive table cardinality', () => {
  test('normalizes an unknown total without hiding the available window', () => {
    const table = normalizeTable({
      ...emptyTable,
      cardinality: { kind: 'unknown', value: 0 },
      availableRows: 10_000,
    })

    expect(table.cardinality).toEqual({ kind: 'unknown', value: 0 })
    expect(table.availableRows).toBe(10_000)
  })

  test('preserves an exact total across a later scrolling block', () => {
    const previous = normalizeTable({
      ...emptyTable,
      resetVersion: 4,
      cardinality: { kind: 'exact', value: 1_234 },
      availableRows: 1_234,
    })
    const incoming = normalizeTable({
      ...emptyTable,
      resetVersion: 4,
      cardinality: { kind: 'lower_bound', value: 100 },
      availableRows: 10_000,
    })

    const merged = preserveCardinality(previous, incoming)
    expect(merged.cardinality).toEqual({ kind: 'exact', value: 1_234 })
    expect(merged.availableRows).toBe(1_234)
  })

  test('does not carry old cardinality into a reset generation', () => {
    const previous = normalizeTable({ ...emptyTable, resetVersion: 4, cardinality: { kind: 'exact', value: 1_234 } })
    const incoming = normalizeTable({ ...emptyTable, resetVersion: 5, cardinality: { kind: 'lower_bound', value: 50 } })

    expect(preserveCardinality(previous, incoming).cardinality).toEqual({ kind: 'lower_bound', value: 50 })
  })

  test('keeps the strongest evidence and the largest equal-strength bound', () => {
    const first = normalizeTable({ ...emptyTable, resetVersion: 4, cardinality: { kind: 'lower_bound', value: 50 } })
    const second = normalizeTable({ ...emptyTable, resetVersion: 4, cardinality: { kind: 'lower_bound', value: 150 } })
    const exact = normalizeTable({ ...emptyTable, resetVersion: 4, cardinality: { kind: 'exact', value: 75 }, availableRows: 75 })

    expect(preserveCardinality(first, second).cardinality.value).toBe(150)
    expect(preserveCardinality(exact, second).cardinality).toEqual({ kind: 'exact', value: 75 })
  })

  test('accepts a newer exact total after a same-reset filter refresh', () => {
    const previous = normalizeTable({ ...emptyTable, resetVersion: 1, cardinality: { kind: 'exact', value: 24 }, availableRows: 24 })
    const incoming = normalizeTable({ ...emptyTable, resetVersion: 1, cardinality: { kind: 'exact', value: 6 }, availableRows: 6 })

    const merged = preserveCardinality(previous, incoming)
    expect(merged.cardinality).toEqual({ kind: 'exact', value: 6 })
    expect(merged.availableRows).toBe(6)
  })
})
