import { expect, test } from 'bun:test'
import { parse } from 'yaml'
import { beforeYAML, afterYAML, dashboardScenarios, dashboardScenario, inspectDashboard, matchingCompilerEvidence, sourceDigest } from './dashboard-contract-fixtures'

test('the corrected guide is structurally valid under both schemas', () => {
 for (const source of [beforeYAML, afterYAML]) {
  expect(inspectDashboard(source, true).valid).toBe(true)
  expect(inspectDashboard(source, false).valid).toBe(true)
 }
 expect(afterYAML).toContain('dimension: purchase_date')
 expect(afterYAML).toContain('grain: month')
})

test('invalid span is accepted by baseline but rejected with precise location after tightening', () => {
 const source = afterYAML.replace('columnSpan: 12', 'columnSpan: 0')
 expect(inspectDashboard(source, true).valid).toBe(true)
 const result = inspectDashboard(source, false)
 expect(result.valid).toBe(false)
 expect(result.issues).toContainEqual({path:'/spec/pages/0/components/0/placement/columnSpan',message:'must be >= 1'})
})

test('YAML parse errors and duplicate keys are rejected without throwing', () => {
 expect(inspectDashboard('spec: [', false).valid).toBe(false)
 expect(inspectDashboard('kind: Dashboard\nkind: Dashboard', false).valid).toBe(false)
})

test('both versions keep closed structural validation', () => {
 for (const baseline of [true,false]) {
  expect(inspectDashboard(afterYAML.replace('type: aggregate','type: sql'),baseline).valid).toBe(false)
  expect(inspectDashboard(afterYAML.replace('semanticModel: sales','semanticModel: sales\n  unknown: true'),baseline).valid).toBe(false)
 }
})

 test('the after source stays identical to the public guide resource', async () => {
  const guide = await Bun.file(new URL('../docs/articles/build/dashboard.md', import.meta.url)).text()
  expect(guide.split('```yaml\n')[1]?.split('```')[0]).toBe(afterYAML)
 })

test('a numeric error does not conceal independent structural failures', () => {
 const source = afterYAML.replace('columnSpan: 12','columnSpan: 0').replace('  id: dashboard:executive-sales\n','')
 const result = inspectDashboard(source,false)
 expect(result.valid).toBe(false)
 expect(result.issues.some(issue => issue.path === '/metadata' && issue.message.includes('id'))).toBe(true)
})


test('all recorded compiler fixtures have matching exact-input digests', async () => {
  expect(dashboardScenarios.length).toBeGreaterThanOrEqual(11)
  expect(dashboardScenario('corrected-monthly').source).toBe(afterYAML)
  expect(dashboardScenario('original-guide').source).toBe(beforeYAML)
  for (const fixture of dashboardScenarios) {
    expect(await sourceDigest(fixture.source)).toBe(fixture.sourceDigest)
    expect(fixture.sourceRootDigest).toMatch(/^sha256:[a-f0-9]{64}$/)
    expect((await matchingCompilerEvidence(fixture.source, fixture.id))?.id).toBe(fixture.id)
    expect(await matchingCompilerEvidence(fixture.source + '\n# changed bytes\n', fixture.id)).toBeUndefined()
  }
  expect(await matchingCompilerEvidence(dashboardScenario('corrected-monthly').source, 'untrusted')).toBeUndefined()
})

test('compiler evidence separates structural acceptance from contextual rejection', () => {
  for (const id of ['original-guide', 'out-of-grid', 'overlap', 'missing-visual', 'duplicate-visual-id']) {
    const fixture = dashboardScenario(id)
    expect(inspectDashboard(fixture.source, false).valid).toBe(true)
    expect(fixture.valid).toBe(false)
    expect(fixture.issues.length).toBeGreaterThan(0)
  }
  for (const id of ['corrected-monthly', 'omitted-defaults', 'explicit-defaults', 'reordered-definitions', 'confined-fragments']) {
    const fixture = dashboardScenario(id)
    expect(fixture.valid).toBe(true)
    expect(fixture.resolvedIntent).toBeDefined()
  }
})

test('exact recorded source lookup is immediate and needs no asynchronous browser crypto', () => {
  const fixture = dashboardScenario('corrected-monthly')
  expect(matchingCompilerEvidence(fixture.source, fixture.id)).toBe(fixture)
  expect(matchingCompilerEvidence(fixture.source + '\n# edited\n', fixture.id)).toBeUndefined()
  expect(matchingCompilerEvidence(fixture.source, 'untrusted')).toBeUndefined()
})


test('large invalid documents retain all distinct diagnostics in both schemas', () => {
  const document = parse(afterYAML)
  document.spec.pages[0].components = Array(2500).fill(null)
  const source = JSON.stringify(document)
  for (const baseline of [false, true]) {
    const result = inspectDashboard(source, baseline)
    expect(result.valid).toBe(false)
    expect(result.issues).toHaveLength(5001)
    expect(new Set(result.issues.map(issue => JSON.stringify([issue.path, issue.message]))).size).toBe(result.issues.length)
    expect(result.issues.some(issue => issue.path === '/spec/pages/0/components/2499')).toBe(true)
  }
})


test('both schemas reject nonfinite YAML numbers', () => {
  for (const baseline of [false, true]) {
    for (const value of ['.inf', '-.inf', '.nan']) {
      expect(inspectDashboard(afterYAML.replace('gap: 16', `gap: ${value}`), baseline).valid).toBe(false)
      expect(inspectDashboard(afterYAML.replace('columnSpan: 12', `columnSpan: ${value}`), baseline).valid).toBe(false)
    }
  }
})
