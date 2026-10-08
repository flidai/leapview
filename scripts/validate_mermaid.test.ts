import { describe, expect, test } from 'bun:test'
import { extractMermaidBlocks, validateMermaidBlocks } from './validate_mermaid'

describe('documentation Mermaid validation', () => {
  test('extracts backtick and tilde fences with their authored line numbers', () => {
    const markdown = [
      '# Runtime',
      '',
      '```mermaid',
      'flowchart LR',
      '  Browser --> Server',
      '```',
      '',
      '~~~mermaid',
      'sequenceDiagram',
      '  Browser->>Server: Query',
      '~~~',
    ].join('\n')

    expect(extractMermaidBlocks('docs/runtime.md', markdown)).toEqual([
      {
        file: 'docs/runtime.md',
        line: 3,
        source: 'flowchart LR\n  Browser --> Server',
      },
      {
        file: 'docs/runtime.md',
        line: 8,
        source: 'sequenceDiagram\n  Browser->>Server: Query',
      },
    ])
  })

  test('reports invalid diagrams at the opening fence', async () => {
    const issues = await validateMermaidBlocks([
      {
        file: 'docs/runtime.md',
        line: 12,
        source: 'flowchart LR\n  accTitle: Runtime request\n  accDescr: A browser request reaches the server.\n  Browser -- Server',
      },
    ])

    expect(issues).toHaveLength(1)
    expect(issues[0]).toMatchObject({ file: 'docs/runtime.md', line: 12 })
    expect(issues[0].message.length).toBeGreaterThan(0)
  })

  test('requires an accessible title and description', async () => {
    const issues = await validateMermaidBlocks([
      {
        file: 'docs/runtime.md',
        line: 20,
        source: 'flowchart LR\n  Browser --> Server',
      },
    ])

    expect(issues).toEqual([
      { file: 'docs/runtime.md', line: 20, message: 'diagram must define accTitle' },
      { file: 'docs/runtime.md', line: 20, message: 'diagram must define accDescr' },
    ])
  })
})


test('diagram math keeps trust disabled when renderer options inherit a polluted prototype', async () => {
  const katex = (await import('katex' as string)).default
  const math = String.raw`\href{https://example.invalid}{unsafe}`
  expect(katex.renderToString(math, { trust: true })).toContain('href=')
  expect(katex.renderToString(math)).not.toContain('href=')
  const descriptor = Object.getOwnPropertyDescriptor(Object.prototype, 'trust')
  try {
    Object.defineProperty(Object.prototype, 'trust', { value: true, configurable: true, writable: true })
    expect(katex.renderToString(math)).not.toContain('href=')
  } finally {
    if (descriptor) Object.defineProperty(Object.prototype, 'trust', descriptor)
    else Reflect.deleteProperty(Object.prototype, 'trust')
  }
})
