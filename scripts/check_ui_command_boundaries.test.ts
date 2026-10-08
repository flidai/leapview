import { describe, expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { checkUICommandBoundaries, inspectUICommandSource } from './check_ui_command_boundaries'

describe('UI command boundary analysis', () => {
  test('ignores test-only browser fixtures while rejecting production Datastar mutations', () => {
    const root = mkdtempSync(join(tmpdir(), 'lv-ui-boundaries-'))
    try {
      const components = join(root, 'web/components')
      const generated = join(root, 'api/gen')
      mkdirSync(components, { recursive: true })
      mkdirSync(generated, { recursive: true })
      writeFileSync(join(generated, 'json-ir.json'), JSON.stringify({ endpoints: [] }))
      const source = `const command = "@post('/chats/turns')"`
      writeFileSync(join(components, 'chat.test-fixture.ts'), source)
      expect(checkUICommandBoundaries(root)).toEqual([])
      writeFileSync(join(components, 'chat.ts'), source)
      expect(checkUICommandBoundaries(root)).toEqual([{
        file: 'web/components/chat.ts',
        line: 1,
        message: 'direct Datastar mutation expression bypasses a classified UI action helper',
      }])
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  test('rejects direct mutating fetches and operation headers', () => {
    const violations = inspectUICommandSource('web/components/example.ts', `
      fetch('/save', { method: 'POST' })
      const headers = { 'X-LeapView-Operation-ID': 'saveThing' }
    `)
    expect(violations.map((violation) => violation.message)).toEqual([
      'direct mutating fetch bypasses the generated UI command transport',
      'operation identity headers must be authored by the shared command transport',
    ])
  })

  test('allows read-only fetches and the centralized command transport', () => {
    expect(inspectUICommandSource('web/components/example.ts', `fetch('/things', { method: 'GET' })`)).toEqual([])
    expect(inspectUICommandSource('web/components/shared/command.ts', `
      const headers = { 'X-LeapView-Operation-ID': 'saveThing' }
      window.LeapViewCommand = { headers }
      fetch('/save', { method: 'POST', headers: headers('saveThing') })
    `)).toEqual([])
  })

  test('rejects non-literal fetch methods because their safety cannot be proven', () => {
    const violations = inspectUICommandSource('web/components/example.ts', `fetch('/things', { method })`)
    expect(violations).toHaveLength(1)
  })

	test('rejects dynamic fetch options because their method cannot be proven', () => {
	  const violations = inspectUICommandSource('web/components/example.ts', `fetch('/things', options)`)
	  expect(violations).toHaveLength(1)
	})
})
