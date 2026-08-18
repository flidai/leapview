import { writeFile } from 'node:fs/promises'
import process from 'node:process'
import readline from 'node:readline'
import { chromium } from 'playwright'

const baseURL = process.env.QUALIFICATION_URL || 'https://localhost'
const evidenceRoot = process.env.QUALIFICATION_EVIDENCE_ROOT || '/evidence'
const projectID = process.env.QUALIFICATION_PROJECT_ID || 'project:leapview-evaluation'
const screenshotPath = `${evidenceRoot}/authoring-browser-failure.png`

async function requireJSON(response, description) {
  if (!response.ok()) {
    throw new Error(`${description} returned ${response.status()}: ${await response.text()}`)
  }
  return response.json()
}

async function signIn(page, email, temporaryPassword, password) {
  await page.goto(new URL('/login', baseURL).href, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(temporaryPassword)
  await page.getByLabel('Password').press('Enter')
  await page.getByLabel('Temporary password').waitFor({ state: 'visible', timeout: 30_000 })
  await page.getByLabel('Temporary password').fill(temporaryPassword)
  await page.getByLabel('New password').fill(password)
  await page.getByLabel('New password').press('Enter')
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 30_000 })
}

async function issueToken(context, page, capabilities) {
  const challenge = await requireJSON(
    await context.request.post(
      new URL('/oauth/device/code', baseURL).href,
      {
        form: {
          client_id: 'leapview-cli',
          project_id: projectID,
          scope: capabilities.join(' '),
        },
      },
    ),
    `device authorization for ${capabilities.join(', ')}`,
  )
  const deviceURL = new URL(challenge.verification_uri_complete, baseURL)
  await page.goto(deviceURL.href, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByRole('heading', { name: 'Authorize LeapView CLI' }).waitFor()
  await page.getByLabel('Device code').fill(challenge.user_code)
  await page.getByRole('button', { name: 'Authorize', exact: true }).click({ force: true })
  await page.getByRole('heading', { name: 'CLI authorized' }).waitFor({ timeout: 30_000 })
  const tokens = await requireJSON(
    await context.request.post(
      new URL('/oauth/token', baseURL).href,
      {
        form: {
          client_id: 'leapview-cli',
          grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
          device_code: challenge.device_code,
        },
      },
    ),
    `device token exchange for ${capabilities.join(', ')}`,
  )
  return { accessToken: tokens.access_token }
}

const browser = await chromium.launch({ headless: true })
const administratorContext = await browser.newContext({ ignoreHTTPSErrors: true })
const administratorPage = await administratorContext.newPage()
let reviewerContext
let reviewerPage

const methods = {
  async signInAdministrator(params) {
    await signIn(
      administratorPage,
      params.email,
      params.temporaryPassword,
      params.password,
    )
    return { authenticated: true }
  },

  async issueAdministratorToken(params) {
    return issueToken(
      administratorContext,
      administratorPage,
      params.capabilities,
    )
  },

  async createReviewer(params) {
    await administratorPage.goto(new URL('/admin/principals', baseURL).href, {
      waitUntil: 'domcontentloaded',
      timeout: 60_000,
    })
    await administratorPage.getByRole('button', { name: 'Create local user', exact: true }).click()
    await administratorPage.getByLabel('Email', { exact: true }).fill(params.email)
    await administratorPage.getByLabel('Display name', { exact: true }).fill(params.displayName)
    await administratorPage.getByRole('button', { name: 'Create user', exact: true }).click()
    const temporaryPassword = await administratorPage.locator('code.password-value').textContent({ timeout: 30_000 })
    if (!temporaryPassword?.trim()) {
      throw new Error(`create reviewer ${params.email} returned no temporary password`)
    }
    return {
      principal: { id: params.principalId },
      temporaryPassword: temporaryPassword.trim(),
    }
  },

  async createAdministratorAPIToken(params) {
    await administratorPage.goto(new URL('/admin/api-tokens', baseURL).href, {
      waitUntil: 'domcontentloaded',
      timeout: 60_000,
    })
    await administratorPage.locator('#token-name').fill(params.name)
    await administratorPage.locator('#token-expiry').fill(params.expiresAt.slice(0, 16))
    await administratorPage.getByRole('button', { name: 'Add permissions', exact: true }).click()
    for (const capability of params.capabilities) {
      await administratorPage.locator(`input[type="checkbox"][value="${capability}"]`).check()
    }
    await administratorPage.getByRole('button', { name: 'Close permission picker', exact: true }).click()
    await administratorPage.getByRole('button', { name: 'Create token', exact: true }).click()
    const token = await administratorPage.getByRole('status').locator('code').textContent({ timeout: 30_000 })
    if (!token?.trim()) {
      throw new Error(`create administrator API token ${params.name} returned no token`)
    }
    return { token: token.trim() }
  },

  async signInReviewer(params) {
    reviewerContext ??= await browser.newContext({ ignoreHTTPSErrors: true })
    reviewerPage ??= await reviewerContext.newPage()
    await signIn(
      reviewerPage,
      params.email,
      params.temporaryPassword,
      params.password,
    )
    return { authenticated: true }
  },

  async issueReviewerToken(params) {
    if (!reviewerContext || !reviewerPage) {
      throw new Error('reviewer must sign in before requesting a token')
    }
    return issueToken(reviewerContext, reviewerPage, params.capabilities)
  },

  async authorizeCLI(params) {
    const deviceURL = new URL(params.verificationUrl, baseURL)
    deviceURL.searchParams.set('user_code', params.userCode)
    await administratorPage.goto(
      deviceURL.href,
      { waitUntil: 'domcontentloaded', timeout: 60_000 },
    )
    await administratorPage.getByRole('heading', { name: 'Authorize LeapView CLI' }).waitFor()
    await administratorPage.getByLabel('Device code').fill(params.userCode)
    await administratorPage.getByRole('button', { name: 'Authorize', exact: true }).click({ force: true })
    await administratorPage.getByRole('heading', { name: 'CLI authorized' }).waitFor({ timeout: 30_000 })
    return { authorized: true }
  },

  async verifyPreview(params) {
    const previewURL = new URL(params.previewUrl, baseURL)
    if (!previewURL.pathname.startsWith('/candidates/')) {
      throw new Error(`CLI returned a non-candidate preview URL: ${previewURL.href}`)
    }
    await administratorPage.goto(
      previewURL.href,
      { waitUntil: 'domcontentloaded', timeout: 60_000 },
    )
    await administratorPage.waitForURL(
      (url) => url.pathname.startsWith(`${previewURL.pathname}/dashboards/`),
      { timeout: 60_000 },
    )
    const dashboardURL = new URL(
      `${previewURL.pathname}/dashboards/sales-overview`,
      baseURL,
    )
    await administratorPage.goto(
      dashboardURL.href,
      { waitUntil: 'domcontentloaded', timeout: 60_000 },
    )
    await administratorPage
      .getByText('Governed order rows', { exact: true })
      .waitFor({ state: 'visible', timeout: 60_000 })
    await administratorPage
      .getByText('24', { exact: true })
      .first()
      .waitFor({ state: 'visible', timeout: 30_000 })
    return {
      candidateId: params.candidateId,
      governedOrderRows: 24,
      previewUrl: previewURL.href,
    }
  },

  async close() {
    return { closed: true }
  },
}

const lines = readline.createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
  terminal: false,
})

try {
  for await (const line of lines) {
    if (!line.trim()) continue
    let request
    try {
      request = JSON.parse(line)
      if (request.jsonrpc !== '2.0' || request.id === undefined || typeof request.method !== 'string') {
        throw new Error('invalid JSON-RPC 2.0 request')
      }
      const method = methods[request.method]
      if (!method) {
        throw new Error(`unsupported browser worker method ${request.method}`)
      }
      const result = await method(request.params || {})
      process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: request.id, result })}\n`)
      if (request.method === 'close') break
    } catch (error) {
      await writeFile(
        `${evidenceRoot}/authoring-browser-failure.json`,
        `${JSON.stringify({
          error: String(error),
          method: request?.method || '',
          title: await administratorPage.title().catch(() => ''),
          url: administratorPage.url(),
        })}\n`,
        { mode: 0o644 },
      ).catch(() => {})
      await administratorPage.screenshot({ path: screenshotPath }).catch(() => {})
      process.stdout.write(`${JSON.stringify({
        jsonrpc: '2.0',
        id: request?.id || 0,
        error: {
          code: -32603,
          message: String(error),
        },
      })}\n`)
    }
  }
} finally {
  await reviewerContext?.close()
  await administratorContext.close()
  await browser.close()
}
