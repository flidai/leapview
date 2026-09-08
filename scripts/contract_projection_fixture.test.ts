import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'bun:test'

const fixtureRoot = 'internal/project/contractprojection/testdata'

// This deliberately small independent fixture implementation is test-only.
// Production canonicalization remains exclusively in the sealed Go boundary.
function canonicalFixture(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalFixture).join(',')}]`
  if (value !== null && typeof value === 'object') {
    return `{${Object.entries(value as Record<string, unknown>)
      .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
      .map(([key, item]) => `${JSON.stringify(key)}:${canonicalFixture(item)}`)
      .join(',')}}`
  }
  return JSON.stringify(value)
}

describe('leapview.contract/v1 cross-language corpus', () => {
  test('matches the Go RFC 8785 bytes and digest', () => {
    const input = JSON.parse(readFileSync(`${fixtureRoot}/cross-language-projection.input.json`, 'utf8'))
    const expected = readFileSync(`${fixtureRoot}/cross-language-projection.canonical.json`, 'utf8').trim()
    const canonical = canonicalFixture(input)

    expect(canonical).toBe(expected)
    expect(`sha256:${createHash('sha256').update(canonical).digest('hex')}`).toBe(
      'sha256:c922a716464ecf5fe5dd0f022ad067c0d095b3ec6387e525d5e30a071c230205',
    )
  })

  test('matches the RFC 8785 number corpus', () => {
    const input = JSON.parse(readFileSync(`${fixtureRoot}/rfc8785-numbers.input.json`, 'utf8'))
    const expected = readFileSync(`${fixtureRoot}/rfc8785-numbers.canonical.json`, 'utf8').trim()

    expect(canonicalFixture(input)).toBe(expected)
  })

  test('orders object keys by UTF-16 code units', () => {
    const input = {
      '\uFFFF': 'ffff',
      '\u{1D11E}': 'musical symbol',
      '\uE000': 'private use',
      a: 'ascii',
      '\u{10FFFF}': 'maximum code point',
    }
    const order = ['a', '\u{1D11E}', '\u{10FFFF}', '\uE000', '\uFFFF']
    const expected = `{${order.map((key) => `${JSON.stringify(key)}:${JSON.stringify(input[key as keyof typeof input])}`).join(',')}}`

    expect(canonicalFixture(input)).toBe(expected)
  })
})
