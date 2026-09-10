import { expect, test } from 'bun:test'
import { icons } from 'lucide'
import { generateLucideCatalogData, generateLucideCatalogSources } from './generate_lucide_icon_catalog'

const generated = generateLucideCatalogSources()

const pascal = (name: string) => name.split('-').map((part) => part ? part[0]!.toUpperCase() + part.slice(1) : '').join('')

test('every canonical icon node and alias resolves to the dependency export', () => {
  expect(generated.canonicalNames.length).toBeGreaterThan(1700)
  expect(new Set(generated.canonicalNames).size).toBe(generated.canonicalNames.length)

  for (const name of generated.canonicalNames) {
    const node = icons[pascal(name) as keyof typeof icons]
    expect(node).toBeDefined()
    expect(generated.nodes[name]).toBe(node)
  }

  for (const [canonical, aliases] of Object.entries(generated.aliases)) {
    for (const alias of aliases) {
      const node = icons[pascal(alias) as keyof typeof icons]
      expect(node).toBeDefined()
      expect(node).toBe(generated.nodes[canonical])
    }
  }
})

test('split sources preserve metadata API and keep node data out of metadata source', () => {
  expect(generated.catalogSource).toContain('canonicalLucideIconNames')
  expect(generated.catalogSource).toContain('lucideIconAliases')
  expect(generated.catalogSource).toContain("export { lucideIconNodes } from './lucide-icon-nodes'")
  expect(generated.catalogSource).not.toContain('export const lucideIconNodes')
  expect(generated.nodesSource).toContain('export const lucideIconNodes')
  expect(generated.nodesSource).not.toContain('canonicalLucideIconNames')
  expect(generated.nodesSource).not.toContain('lucideIconAliases')
})

test('generation is deterministic and metadata/node outputs are independently reproducible', () => {
  const data = generateLucideCatalogData()
  const first = generateLucideCatalogSources(data)
  const second = generateLucideCatalogSources(generateLucideCatalogData())
  expect(first.catalogSource).toBe(second.catalogSource)
  expect(first.nodesSource).toBe(second.nodesSource)
  expect(first.goSource).toBe(second.goSource)
  expect(first.canonicalNames).toEqual(second.canonicalNames)
  expect(first.aliases).toEqual(second.aliases)
})
