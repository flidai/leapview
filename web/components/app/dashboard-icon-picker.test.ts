import { expect, test } from 'bun:test'
import { canonicalLucideIconNames, lucideIconAliases } from '../../generated/lucide-icon-catalog'

test('Lucide picker catalog has canonical names exactly once and aliases only as search metadata', () => {
  expect(canonicalLucideIconNames.length).toBeGreaterThan(1700)
  expect(new Set(canonicalLucideIconNames).size).toBe(canonicalLucideIconNames.length)
  expect(canonicalLucideIconNames).toContain('house')
  expect(canonicalLucideIconNames).not.toContain('home' as never)
  expect(lucideIconAliases.house).toContain('home')
})
