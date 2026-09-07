import { expect, test } from 'bun:test'
import { readFile } from 'node:fs/promises'
import { blockingAxeViolations, formatAxeViolations, type AxeViolation } from './axe_accessibility'
import { hasMixedSpatialPrecision } from './spatial_precision_summary'

test('UI framework QA gives the managed dev task its full readiness budget', async () => {
  const source = await readFile('scripts/qa_ui_framework.ts', 'utf8')

  expect(source).toContain("LEAPVIEW_DEV_READY_ATTEMPTS: String(managedServerReadyAttempts)")
})

test('UI framework QA waits for asynchronous publication activation', async () => {
  const source = await readFile('scripts/qa_ui_framework.ts', 'utf8')

  expect(source).toContain('await waitForProjectReady(started)')
  expect(source).toContain("new URL('/explore', baseURL)")
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

test('development readiness files are published only after PostgreSQL bootstrap', async () => {
  const source = await readFile('scripts/dev-server.sh', 'utf8')

  expect(source).toContain('rm -f "$PORT_FILE" "$PID_FILE"')
  const bootstrap = source.indexOf('bootstrap_local_physical_pool')
  const readiness = source.lastIndexOf('echo "$port" > "$PORT_FILE"')
  expect(bootstrap).toBeGreaterThanOrEqual(0)
  expect(readiness).toBeGreaterThan(bootstrap)
  expect(source).toContain('Publish the readiness contract only after the final server')
})

test('maintained headless workflows use the managed PostgreSQL dev lifecycle', async () => {
  const [agent, capture, taskfile] = await Promise.all([
    readFile('scripts/agent_e2e.sh', 'utf8'),
    readFile('scripts/capture_site_product.ts', 'utf8'),
    readFile('Taskfile.yml', 'utf8'),
  ])

  expect(agent).toContain('./scripts/postgres-dev.sh up')
  expect(agent).toContain('./scripts/dev-server.sh start')
  expect(agent).not.toContain('"$BIN" serve')
  expect(agent).toContain('^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
  expect(capture).toContain("run(['task', 'postgres:dev:up'])")
  expect(capture).toContain("'./scripts/dev-server.sh', 'start'")
  expect(capture).not.toContain('Bun.spawn([binary]')
  expect(taskfile).toContain('agent:e2e:')
  expect(taskfile).toContain('site:product-screenshot:')
})

test('PostgreSQL helper preserves generated credentials across repeated up runs', async () => {
  const source = await readFile('scripts/postgres-dev.sh', 'utf8')

  expect(source).toContain('generated env file is the durable source for local credentials')
  expect(source).toContain('LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD |')
  expect(source).toContain('LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD)')
  expect(source).toContain('LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD=%s')
  expect(source).toContain('chmod 600 "$ENV_FILE"')

  const composeUp = source.indexOf('compose up --detach --wait')
  const isolationCheck = source.indexOf('check_isolation', composeUp)
  const runtimeEnvWrite = source.indexOf('write_runtime_env', composeUp)
  expect(composeUp).toBeGreaterThanOrEqual(0)
  expect(isolationCheck).toBeGreaterThan(composeUp)
  expect(runtimeEnvWrite).toBeGreaterThan(isolationCheck)
})

test('managed development keeps bypass HTTP loopback-only and preserves caller overrides', async () => {
  const [server, agent, capture] = await Promise.all([
    readFile('scripts/dev-server.sh', 'utf8'),
    readFile('scripts/agent_e2e.sh', 'utf8'),
    readFile('scripts/capture_site_product.ts', 'utf8'),
  ])

  expect(server).toContain('LEAPVIEW_ADDR="127.0.0.1:$port"')
  expect(server).toContain('if [[ -z "${!name:-}" ]]')
  expect(agent).toContain('LEAPVIEW_ADDR="127.0.0.1:$PORT"')
  expect(capture).toContain('LEAPVIEW_ADDR: `127.0.0.1:${port}`')
})

test('browser QA uses canonical project resource IDs', async () => {
  const source = await readFile('scripts/datastar_lit_route_qa.ts', 'utf8')

  expect(source).toContain('/dashboards/dashboard:visual-showcase')
  expect(source).toContain("visualID === 'revenue'")
  expect(source).not.toContain("'/dashboards/visual-showcase")
  expect(source).not.toContain("visualID === 'revenue_by_month'")
})

test('visual regression QA covers stable representative states, themes, and viewports', async () => {
  const source = await readFile('scripts/visual_regression.spec.ts', 'utf8')

  expect(source).toContain('/dashboards/dashboard:executive-sales/pages/overview')
  expect(source).toContain('/dashboards/dashboard:visual-showcase/pages/overview')
  expect(source).toContain('/dashboards/dashboard:visual-showcase/pages/tables')
  expect(source).toContain("{ name: 'desktop', width: 1440, height: 900 }")
  expect(source).toContain("{ name: 'compact', width: 600, height: 900 }")
  expect(source).toContain("const modes = ['light', 'dark'] as const")
  expect(source).toContain("document.fonts.load('400 16px \"Inter Variable\"'")
  expect(source).toContain("document.fonts.load('600 16px \"Inter Variable\"'")
  expect(source).toContain("page.screenshot({ animations: 'disabled', caret: 'hide', scale: 'css' })")
  expect(source).toContain("expect(screenshot).toMatchSnapshot")
  expect(source).toContain('isDenseDesktopTable ? 0.009 : isDesktop ? 0.003 : 0.001')
  expect(source).toContain('threshold: isDesktop ? 0.5 : 0.2')
})

test('visual regression QA has an explicit baseline update and failure-artifact workflow', async () => {
  const [runner, config, taskfile, mergeWorkflow] = await Promise.all([
    readFile('scripts/qa_ui_framework.ts', 'utf8'),
    readFile('scripts/playwright.visual.config.ts', 'utf8'),
    readFile('Taskfile.yml', 'utf8'),
    readFile('.github/workflows/merge-validation.yml', 'utf8'),
  ])

  expect(runner).toContain("await run([...visualCommand, '--update-snapshots'], visualEnv)")
  expect(runner).toContain('await run(visualCommand, visualEnv)')
  expect(runner).toContain("qaScope !== 'all' && qaScope !== 'visual'")
  expect(config).toContain("trace: 'retain-on-failure'")
  expect(config).toContain("video: 'retain-on-failure'")
  expect(config).toContain("screenshot: 'only-on-failure'")
  expect(taskfile).toContain('qa:ui-framework:visual:update:')
  expect(taskfile).toContain('LEAPVIEW_UI_QA_SCOPE=visual LEAPVIEW_UPDATE_VISUAL_BASELINES=1')
  expect(mergeWorkflow).toContain('leapview-ui-visual-regression-failure')
  expect(mergeWorkflow).toContain('scripts/visual-regression-baselines')
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
