import { describe, expect, test } from 'bun:test'
import { inspectUICommandSource } from './check_ui_command_boundaries'

describe('UI command boundary analysis', () => {
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

  test('fails closed when TypeScript syntax cannot be parsed', () => {
    expect(inspectUICommandSource('web/components/example.ts', 'const value: = 1')).toEqual([{
      file: 'web/components/example.ts',
      line: 1,
      message: 'UI boundary source could not be parsed safely',
    }])
  })
})
