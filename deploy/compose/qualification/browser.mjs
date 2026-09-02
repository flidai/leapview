import { chromium } from 'playwright'
import { readFile } from 'node:fs/promises'
import process from 'node:process'

const baseURL = process.env.QUALIFICATION_URL || 'https://localhost'
const credentialsPath = process.env.QUALIFICATION_CREDENTIALS || '/run/secrets/credentials.json'
const screenshotPath = process.env.QUALIFICATION_SCREENSHOT || '/evidence/browser-failure.png'
const credentials = JSON.parse(await readFile(credentialsPath, 'utf8'))

if (!credentials.email || !credentials.qualificationPassword || !credentials.publisherToken || !credentials.auditToken) {
  throw new Error('qualification credentials are incomplete')
}

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await context.newPage()

try {
  await page.goto(new URL('/login', baseURL).href, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByLabel('Email').fill(credentials.email)
  // Prove an invalid local credential remains on the branded, accessible
  // login surface before continuing with the valid qualification login.
  await page.getByLabel('Password').fill(`${credentials.qualificationPassword}-invalid`)
  await page.getByLabel('Password').press('Enter')
  await page.waitForURL(/\/login\?error=invalid_credentials(?:$|&)/, { timeout: 30_000 })
  await page.getByRole('heading', { name: /LeapView/i }).waitFor({ state: 'visible', timeout: 30_000 })
  await page.getByRole('alert').filter({ hasText: /Invalid email or password/i }).waitFor({ state: 'visible', timeout: 30_000 })

  await page.getByLabel('Email').fill(credentials.email)
  await page.getByLabel('Password').fill(credentials.qualificationPassword)
  await page.getByLabel('Password').press('Enter')

  const dashboard = page.getByRole('link', { name: /Five-minute Sales Evaluation/i })
  await dashboard.waitFor({ state: 'visible', timeout: 60_000 })
  const dashboardHref = await dashboard.getAttribute('href')
  if (!dashboardHref) {
    throw new Error('evaluation dashboard has no navigation target')
  }
  await page.goto(new URL(dashboardHref, baseURL).href, { waitUntil: 'domcontentloaded', timeout: 60_000 })

  await page.getByText('Governed order rows', { exact: true }).waitFor({ state: 'visible', timeout: 60_000 })
  await page.getByText('24', { exact: true }).first().waitFor({ state: 'visible', timeout: 30_000 })

  const state = page.getByRole('button', { name: /^State:/ })
  await state.click({ force: true })
  const stateOptions = page.getByRole('dialog', { name: 'State filter options', exact: true })
  await stateOptions.waitFor({ state: 'visible', timeout: 30_000 })
  await stateOptions.getByRole('checkbox', { name: 'SP', exact: true }).check()
  await page.keyboard.press('Escape')
  await stateOptions.waitFor({ state: 'hidden', timeout: 30_000 })
  await page.getByText('6', { exact: true }).first().waitFor({ state: 'visible', timeout: 30_000 })

  const table = page.locator('lv-report-table')
  await table.evaluate((element) => element.scrollIntoView({ block: 'center' }))
  const rows = table.locator('[role="rowgroup"] [role="row"]')
  await rows.first().waitFor({ state: 'visible', timeout: 30_000 })
  const stateCells = page.getByRole('cell', { name: 'State: SP', exact: true })
  await stateCells.first().waitFor({ state: 'visible', timeout: 30_000 })

  const denialRequestID = `qualification-denial-${Date.now()}`
  const projectPath = process.env.QUALIFICATION_PROJECT_ID || 'project:leapview-evaluation'
  const denial = await context.request.get(new URL(`/api/v1/projects/${projectPath}/grants`, baseURL).href, {
    headers: {
      Authorization: `Bearer ${credentials.publisherToken}`,
      'X-Request-ID': denialRequestID,
    },
  })
  if (denial.status() !== 403) {
    throw new Error(`restricted publisher request returned ${denial.status()}, expected 403`)
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
    throw new Error('restricted publisher denial was not recorded in the project audit stream')
  }
} catch (error) {
  await page.screenshot({ path: screenshotPath }).catch(() => {})
  throw error
} finally {
  await context.close()
  await browser.close()
}
