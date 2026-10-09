import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

test('comparison evidence retains only bounded reports/provenance while the accepted-reference archive stays two files', () => {
  const workflow = parse(readFileSync('.github/workflows/artifacts.yml', 'utf8'))
  const steps = workflow.jobs['qualify-production-image'].steps
  const reference = steps.find((step: any) => step.name === 'Retain bounded performance evidence')
  expect(reference.with.path.trim().split('\n')).toEqual([
    '.tmp/qualification/production-image/performance-report.json',
    '.tmp/qualification/production-image/performance-identity.json',
  ])
  const comparison = steps.find((step: any) => step.name === 'Retain bounded serial comparison evidence')
  expect(comparison.with.path.trim().split('\n')).toEqual([
    '.tmp/qualification/production-image-comparison/reference/performance-report.json',
    '.tmp/qualification/production-image-comparison/protocol.json',
    '.tmp/qualification/production-image-comparison/build-inputs.json',
    '.tmp/qualification/production-image/comparison-mode.json',
  ])
  expect(comparison.if).toContain('always()')
  expect(comparison.with['retention-days']).toBe(90)
  expect(workflow.jobs['qualify-production-image'].permissions.actions).toBe('read')
})
