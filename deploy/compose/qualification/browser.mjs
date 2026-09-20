import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'
import process from 'node:process'

const baseURL = process.env.QUALIFICATION_URL || 'https://localhost'
const credentialsPath = process.env.QUALIFICATION_CREDENTIALS || '/run/secrets/credentials.json'
const screenshotPath = process.env.QUALIFICATION_SCREENSHOT || '/evidence/browser-failure.png'
const credentials = JSON.parse(await readFile(credentialsPath, 'utf8'))

if (!credentials.email || !credentials.qualificationPassword || !credentials.workloadToken || !credentials.auditToken) {
  throw new Error('qualification credentials are incomplete')
}

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await context.newPage()

async function gotoWithNetworkRetry(url) {
  let lastError
  for (let attempt = 1; attempt <= 5; attempt += 1) {
    try {
      const response = await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60_000 })
      if (!page.url().startsWith('chrome-error://')) return response
      lastError = new Error(`navigation reached ${page.url()}`)
    } catch (error) {
      lastError = error
    }
    await page.waitForTimeout(attempt * 500)
  }
  throw lastError || new Error(`navigation failed: ${url}`)
}

async function waitForDashboardStatus(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const status = await page.locator('lv-dashboard-page').evaluate((element) => ({
      generation: Number(element.status?.generation || 0),
      loading: Boolean(element.status?.loading),
    }))
    if (predicate(status)) return status
    await page.waitForTimeout(25)
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for dashboard status`)
}

async function applyStateFilterWithGenerationRetry(dashboardURL) {
  let lastError
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      if (attempt > 1) {
        await gotoWithNetworkRetry(dashboardURL)
        await page.getByText('Governed order rows', { exact: true }).waitFor({ state: 'visible', timeout: 60_000 })
        await waitForDashboardStatus((status) => status.generation > 0 && !status.loading, 60_000)
      }
      const generation = await page.locator('lv-dashboard-page').evaluate(
        (element) => Number(element.status?.generation || 0),
      )
      await page.getByRole('button', { name: /^State:/ }).click({ force: true })
      const options = page.getByRole('dialog', { name: 'State filter options', exact: true })
      await options.waitFor({ state: 'visible', timeout: 30_000 })
      await options.getByRole('checkbox', { name: 'SP', exact: true }).check()
      await page.keyboard.press('Escape')
      await options.waitFor({ state: 'hidden', timeout: 30_000 })
      await waitForDashboardStatus(
        (status) => status.generation > generation && !status.loading,
        15_000,
      )
      return
    } catch (error) {
      lastError = error
      await page.waitForTimeout(attempt * 500)
    }
  }
  throw lastError || new Error('state filter did not publish a new dashboard generation')
}

try {
  await gotoWithNetworkRetry(new URL('/login', baseURL).href)
  await page.getByLabel('Email').fill(credentials.email)
  // Prove an invalid local credential remains on the branded, accessible
  // login surface before continuing with the valid qualification login.
  await page.locator('input[name="password"]').fill(`${credentials.qualificationPassword}-invalid`)
  await page.locator('input[name="password"]').press('Enter')
  await page.waitForURL(/\/login\?error=invalid_credentials(?:$|&)/, { timeout: 30_000 })
  await page.getByRole('heading', { name: 'Welcome back', exact: true }).waitFor({ state: 'visible', timeout: 30_000 })
  await page.getByRole('alert').filter({ hasText: /Invalid email or password/i }).waitFor({ state: 'visible', timeout: 30_000 })

  await page.getByLabel('Email').fill(credentials.email)
  await page.locator('input[name="password"]').fill(credentials.qualificationPassword)
  await page.locator('input[name="password"]').press('Enter')

  const dashboard = page.getByRole('link', { name: /Five-minute Sales Evaluation/i })
  await dashboard.waitFor({ state: 'visible', timeout: 60_000 })
  const dashboardHref = await dashboard.getAttribute('href')
  if (!dashboardHref) {
    throw new Error('evaluation dashboard has no navigation target')
  }
  await gotoWithNetworkRetry(new URL(dashboardHref, baseURL).href)

  await page.getByText('Governed order rows', { exact: true }).waitFor({ state: 'visible', timeout: 60_000 })
  await page.getByText('24', { exact: true }).first().waitFor({ state: 'visible', timeout: 30_000 })

  await applyStateFilterWithGenerationRetry(new URL(dashboardHref, baseURL).href)
  await page.getByText('6', { exact: true }).first().waitFor({ state: 'visible', timeout: 30_000 })

  const table = page.locator('lv-report-table')
  await table.evaluate((element) => element.scrollIntoView({ block: 'center' }))
  const interactiveCells = table.locator('.row:not(.skeleton-row) button.cell-action')
  await interactiveCells.first().waitFor({ state: 'visible', timeout: 30_000 })
  const stateActions = table.locator('button.cell-action[aria-label="state: SP"]')
  await stateActions.first().waitFor({ state: 'visible', timeout: 30_000 })

  const denialRequestID = `qualification-denial-${Date.now()}`
  const projectPath = process.env.QUALIFICATION_PROJECT_ID || 'project:leapview-evaluation'
  const denial = await context.request.get(new URL(`/api/v1/projects/${projectPath}/role-bindings`, baseURL).href, {
    headers: {
      Authorization: `Bearer ${credentials.workloadToken}`,
      'X-Request-ID': denialRequestID,
    },
  })
  if (denial.status() !== 403) {
    throw new Error(`restricted workload request returned ${denial.status()}, expected 403`)
  }
  const auditResponse = await context.request.get(
    new URL(`/api/v1/projects/${projectPath}/audit-events?action=authorization.denied&limit=200`, baseURL).href,
    { headers: { Authorization: `Bearer ${credentials.auditToken}` } },
  )
  if (!auditResponse.ok()) {
    throw new Error(`audit event lookup returned ${auditResponse.status()}`)
  }
  const audit = await auditResponse.json()
  const recorded = audit.items?.some((event) =>
    event.requestId === denialRequestID &&
    event.action === 'authorization.denied' &&
    event.status === 'denied' &&
    event.capability === 'PROJECT_ADMIN'
  )
  if (!recorded) {
    throw new Error('restricted workload denial was not recorded in the project audit stream')
  }
} catch (error) {
  await page.screenshot({ path: screenshotPath }).catch(() => {})
  const tableDiagnostics = await page.locator('lv-report-table').evaluateAll((tables) => tables.slice(0, 4).map((table) => {
    const root = table.shadowRoot
    return {
      skeletonRows: root?.querySelectorAll('.row.skeleton-row').length ?? 0,
      renderedRows: root?.querySelectorAll('.row:not(.skeleton-row)').length ?? 0,
      cellLabels: Array.from(root?.querySelectorAll('button.cell-action') ?? [])
        .slice(0, 24)
        .map((button) => button.getAttribute('aria-label')),
    }
  })).catch(() => [])
  const message = error instanceof Error ? error.message : String(error)
  throw new Error(`${message}; table diagnostics=${JSON.stringify(tableDiagnostics)}`)
} finally {
  await context.close()
  await browser.close()
}
