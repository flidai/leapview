import { expect, test } from 'bun:test'
import { readFile } from 'node:fs/promises'
import { blockingAxeViolations, formatAxeViolations, type AxeViolation } from './axe_accessibility'
import { hasMixedSpatialPrecision } from './spatial_precision_summary'

test('UI framework QA gives the managed dev task its full readiness budget', async () => {
  const source = await readFile('scripts/qa_ui_framework.ts', 'utf8')

  expect(source).toContain("LEAPVIEW_DEV_READY_ATTEMPTS: String(managedServerReadyAttempts)")
})

test('development startup reuses the bounded CI fixture supply', async () => {
  const source = await readFile('scripts/dev-server.sh', 'utf8')

  expect(source).toContain('ducklakeprepare --supply-out "$manifest"')
  expect(source).not.toContain('extensionsupply --out "$root"')
})

test('development MCP smoke queries an authored semantic metric', async () => {
  const source = await readFile('scripts/dev-server.sh', 'utf8')

  expect(source).toContain('metrics: [{field: "revenue"}]')
  expect(source).not.toContain('metrics: [{field: "sales_orders.revenue"}]')
})

test('browser QA uses canonical project resource IDs', async () => {
  const source = await readFile('scripts/datastar_lit_route_qa.ts', 'utf8')

  expect(source).toContain('/dashboards/dashboard:visual-showcase')
  expect(source).toContain("visualID === 'revenue'")
  expect(source).not.toContain("'/dashboards/visual-showcase")
  expect(source).not.toContain("visualID === 'revenue_by_month'")
})

test('WCAG route QA blocks only serious and critical axe violations', () => {
  const violations = [
    axeViolation('minor-rule', 'minor'),
    axeViolation('moderate-rule', 'moderate'),
    axeViolation('serious-rule', 'serious'),
    axeViolation('critical-rule', 'critical'),
  ]

  expect(blockingAxeViolations(violations).map((violation) => violation.id)).toEqual(['serious-rule', 'critical-rule'])
})

test('WCAG route QA failures identify the route, rule, element, and remediation', () => {
  const message = formatAxeViolations(
    { label: 'Sources', path: '/sources' },
    [{
      ...axeViolation('label', 'serious'),
      help: 'Form elements must have labels',
      helpUrl: 'https://deque.example/rules/label',
      nodes: [{
        target: [['lv-app-shell', 'lv-project-page'], 'input[name="q"]'],
        failureSummary: 'Fix any of the following: add an explicit label.',
      }],
    }],
  )

  expect(message).toContain('Sources (/sources)')
  expect(message).toContain('[serious] label: Form elements must have labels')
  expect(message).toContain('lv-app-shell >>> lv-project-page >>> input[name="q"]')
  expect(message).toContain('Remediation: Fix any of the following: add an explicit label.')
  expect(message).toContain('Guidance: https://deque.example/rules/label')
})

test('spatial route QA rejects only concurrently visible precision families', () => {
  expect(hasMixedSpatialPrecision('View visible map data (611 visible features: 610 raw points, 1 aggregate cell; 14833 total coordinates)')).toBe(true)
  expect(hasMixedSpatialPrecision('View visible map data (0 visible features: 0 raw points, 0 aggregate cells; 14833 total coordinates)')).toBe(false)
  expect(hasMixedSpatialPrecision('View visible map data (12 visible features: 0 raw points, 12 aggregate cells; 14833 total coordinates)')).toBe(false)
  expect(hasMixedSpatialPrecision('View visible map data (12 visible features: 12 raw points, 0 aggregate cells; 14833 total coordinates)')).toBe(false)
})

function axeViolation(id: string, impact: string): AxeViolation {
  return {
    id,
    impact,
    help: `${id} help`,
    helpUrl: `https://deque.example/rules/${id}`,
    nodes: [{ target: ['main'] }],
  }
}
