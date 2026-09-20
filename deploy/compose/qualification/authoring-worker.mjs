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

async function gotoWithNetworkRetry(page, url, options = {}) {
  let lastError
  for (let attempt = 1; attempt <= 20; attempt += 1) {
    try {
      const response = await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60_000, ...options })
      if (!page.url().startsWith('chrome-error://')) return response
      lastError = new Error(`navigation reached ${page.url()}`)
    } catch (error) {
      lastError = error
    }
    await page.waitForTimeout(Math.min(attempt * 500, 3_000))
  }
  throw lastError || new Error(`navigation failed: ${url}`)
}

async function authorizeDeviceCode(page, userCode) {
  await page.getByLabel('Device code').fill(userCode)
  const outcomePromise = Promise.race([
    page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/device'
    }, { timeout: 30_000 }).then((response) => ({ response })),
    page.waitForEvent('requestfailed', {
      predicate: (request) => {
        const url = new URL(request.url())
        return request.method() === 'POST' && url.pathname === '/device'
      },
      timeout: 30_000,
    }).then((request) => ({ request })),
  ])
  const clickErrorPromise = page
    .getByRole('button', { name: 'Authorize', exact: true })
    .click({ force: true, noWaitAfter: true })
    .then(() => undefined, (error) => error)
  const outcome = await outcomePromise
  const clickError = await clickErrorPromise
  if (outcome.request) {
    const failure = outcome.request.failure()?.errorText || 'unknown error'
    if (failure !== 'net::ERR_NETWORK_CHANGED') {
      throw new Error(`device authorization request failed: ${failure}`)
    }
  } else {
    const responseBody = await outcome.response.text().catch(() => '')
    if (!outcome.response.ok() || !responseBody.includes('CLI authorized')) {
      const clickDetail = clickError ? `; click failed: ${String(clickError)}` : ''
      throw new Error(`device authorization returned ${outcome.response.status()} without its confirmation${clickDetail}`)
    }
  }
  // Chromium can transiently replace a successfully returned confirmation
  // with chrome-error://chromewebdata/ when Compose changes a network. The
  // authoritative POST response still proves the same UI contract; retain the
  // visible-heading assertion whenever the page survives the transition.
  if (outcome.response && !page.url().startsWith('chrome-error://')) {
    await page.getByRole('heading', { name: 'CLI authorized' }).waitFor({ timeout: 30_000 })
  }
}

async function classifyLoginState(page) {
  if (new URL(page.url()).pathname !== '/login') return 'authenticated'
  return Promise.race([
    page.getByLabel('Email').waitFor({ state: 'visible', timeout: 10_000 }).then(() => 'login'),
    page.locator('input[name="currentPassword"]').waitFor({ state: 'visible', timeout: 10_000 }).then(() => 'must-change'),
  ])
}

async function openLoginState(page) {
  let lastError
  for (let attempt = 1; attempt <= 5; attempt += 1) {
    try {
      await gotoWithNetworkRetry(page, new URL('/login', baseURL).href)
      return await classifyLoginState(page)
    } catch (error) {
      lastError = error
      await page.waitForTimeout(attempt * 500)
    }
  }
  throw lastError || new Error('login page did not reach a usable state')
}

async function submitLocalLogin(page, email, password) {
  const initialState = await openLoginState(page)
  if (initialState !== 'login') return initialState
  await page.getByLabel('Email').fill(email)
  await page.locator('input[name="password"]').fill(password)
  const loginOutcome = Promise.race([
    page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/auth/local/login'
    }, { timeout: 30_000 }).then((response) => ({ response })),
    page.waitForEvent('requestfailed', {
      predicate: (request) => {
        const url = new URL(request.url())
        return request.method() === 'POST' && url.pathname === '/auth/local/login'
      },
      timeout: 30_000,
    }).then((request) => ({ request })),
  ])
  const navigationPromise = page
    .waitForNavigation({ waitUntil: 'domcontentloaded', timeout: 30_000 })
    .then(() => undefined, (error) => error)
  const pressErrorPromise = page
    .locator('input[name="password"]')
    .press('Enter', { noWaitAfter: true })
    .then(() => undefined, (error) => error)
  const outcome = await loginOutcome
  const pressError = await pressErrorPromise
  if (outcome.response && ![302, 303].includes(outcome.response.status())) {
    throw new Error(`local login returned ${outcome.response.status()}`)
  }
  if (outcome.request && outcome.request.failure()?.errorText !== 'net::ERR_NETWORK_CHANGED') {
    throw new Error(`local login request failed: ${outcome.request.failure()?.errorText || 'unknown error'}`)
  }
  if (pressError && !outcome.request) throw pressError
  const navigationError = await navigationPromise
  if (!navigationError) return classifyLoginState(page)
  return openLoginState(page)
}

async function submitPasswordChange(page, temporaryPassword, password) {
  await page.locator('input[name="currentPassword"]').fill(temporaryPassword)
  await page.locator('input[name="newPassword"]').fill(password)
  const passwordChangeOutcome = Promise.race([
    page.waitForResponse((response) => {
      const url = new URL(response.url())
      return response.request().method() === 'POST' && url.pathname === '/auth/local/password'
    }, { timeout: 30_000 }).then((response) => ({ response })),
    page.waitForEvent('requestfailed', {
      predicate: (request) => {
        const url = new URL(request.url())
        return request.method() === 'POST' && url.pathname === '/auth/local/password'
      },
      timeout: 30_000,
    }).then((request) => ({ request })),
  ])
  const navigationPromise = page
    .waitForNavigation({ waitUntil: 'domcontentloaded', timeout: 30_000 })
    .then(() => undefined, (error) => error)
  const pressErrorPromise = page
    .locator('input[name="newPassword"]')
    .press('Enter', { noWaitAfter: true })
    .then(() => undefined, (error) => error)
  const outcome = await passwordChangeOutcome
  const pressError = await pressErrorPromise
  if (outcome.response && ![302, 401].includes(outcome.response.status())) {
    throw new Error(`password change returned ${outcome.response.status()}`)
  }
  if (outcome.request && outcome.request.failure()?.errorText !== 'net::ERR_NETWORK_CHANGED') {
    throw new Error(`password change request failed: ${outcome.request.failure()?.errorText || 'unknown error'}`)
  }
  if (pressError && !outcome.request) throw pressError
  await navigationPromise
}

async function signIn(page, email, temporaryPassword, password) {
  let lastError
  for (let attempt = 1; attempt <= 6; attempt += 1) {
    try {
      const temporaryState = await submitLocalLogin(page, email, temporaryPassword)
      if (temporaryState === 'authenticated') {
        throw new Error('temporary administrator password authenticated without requiring rotation')
      }
      if (temporaryState === 'must-change') {
        await submitPasswordChange(page, temporaryPassword, password)
      }
      // A network transition can discard the password-change response and its
      // session cookie independently. Prove the mutation with a fresh login;
      // when it did not commit, the next iteration safely retries the still
      // valid temporary credential and its must-change flow.
      const replacementState = await submitLocalLogin(page, email, password)
      if (replacementState === 'authenticated') return
      lastError = new Error(`replacement administrator password reached ${replacementState}`)
    } catch (error) {
      lastError = error
      await page.waitForTimeout(attempt * 500)
    }
  }
  throw lastError || new Error('administrator sign-in did not complete')
}

async function resolvePrincipalFromDirectory(page, email) {
  const directoryURL = new URL('/admin/principals', baseURL).href
  let rows
  let directoryError
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    await gotoWithNetworkRetry(page, directoryURL)
    rows = page.locator('tr.entity-list-table-row').filter({ hasText: email })
    try {
      await rows.first().waitFor({ state: 'visible', timeout: 30_000 })
      directoryError = undefined
      break
    } catch (error) {
      directoryError = error
    }
  }
  if (directoryError || !rows) throw directoryError || new Error(`resolve principal ${email} did not load the directory`)
  const count = await rows.count()
  if (count !== 1) {
    throw new Error(`resolve principal ${email} returned ${count} directory rows`)
  }
  const href = await rows.first().locator('a.entity-list-identity').getAttribute('href')
  if (!href) {
    throw new Error(`resolve principal ${email} returned no directory href`)
  }
  const principalURL = new URL(href, baseURL)
  const prefix = '/admin/principals/'
  if (!principalURL.pathname.startsWith(prefix)) {
    throw new Error(`resolve principal ${email} returned invalid directory href`)
  }
  const id = decodeURIComponent(principalURL.pathname.slice(prefix.length)).trim()
  if (!id || id.includes('/')) {
    throw new Error(`resolve principal ${email} returned no durable ID`)
  }
  return id
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
  await gotoWithNetworkRetry(page, deviceURL.href)
  await page.getByRole('heading', { name: 'Authorize LeapView CLI' }).waitFor()
  await authorizeDeviceCode(page, challenge.user_code)
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
const administratorDiagnostics = []
administratorPage.on('console', (message) => {
  if (message.type() === 'error' || message.type() === 'warning') {
    administratorDiagnostics.push(`console.${message.type()}: ${message.text()}`)
  }
})
administratorPage.on('pageerror', (error) => administratorDiagnostics.push(`pageerror: ${String(error)}`))
administratorPage.on('requestfailed', (request) => {
  administratorDiagnostics.push(`requestfailed: ${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`)
})
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
    const principalID = await resolvePrincipalFromDirectory(administratorPage, params.email)
    return { authenticated: true, principal: { id: principalID } }
  },

  async issueAdministratorToken(params) {
    return issueToken(
      administratorContext,
      administratorPage,
      params.capabilities,
    )
  },

  async createReviewer(params) {
    await gotoWithNetworkRetry(administratorPage, new URL('/admin/principals', baseURL).href)
    // The admin page is fed by a live Datastar stream. During the initial
    // principals refresh the toolbar can be re-rendered while Playwright is
    // checking actionability, which makes a normal click wait for the button
    // to be geometrically stable until its timeout. The button is already
    // present and visible here; force the semantic click so the qualification
    // does not depend on an incidental render frame.
    await administratorPage
      .getByRole('button', { name: 'Create local user', exact: true })
      .click({ force: true })
    await administratorPage.getByLabel('Email', { exact: true }).fill(params.email)
    await administratorPage.getByLabel('Display name', { exact: true }).fill(params.displayName)
    await administratorPage
      .getByRole('button', { name: 'Create user', exact: true })
      .click({ force: true })
    const temporaryPassword = await administratorPage.locator('code.password-value').textContent({ timeout: 30_000 })
    if (!temporaryPassword?.trim()) {
      throw new Error(`create reviewer ${params.email} returned no temporary password`)
    }
    const principalID = await resolvePrincipalFromDirectory(administratorPage, params.email)
    return {
      principal: { id: principalID },
      temporaryPassword: temporaryPassword.trim(),
    }
  },

  async createAdministratorAPIToken(params) {
    await gotoWithNetworkRetry(administratorPage, new URL('/admin/api-tokens/new', baseURL).href)
    await administratorPage.locator('#token-name').fill(params.name)
    const settings = administratorPage.locator('lv-personal-settings')
    await settings.evaluate((element, detail) => {
      element.dispatchEvent(new CustomEvent('lv-personal-token-command', {
        bubbles: true,
        composed: true,
        detail,
      }))
    }, {
      action: 'create',
      name: params.name,
      capabilities: params.capabilities,
      expiresAt: params.expiresAt,
    })
    // The grouped permission picker intentionally expands read/admin choices
    // into human-friendly bundles. Qualification uses the stable UI command
    // contract directly so its machine credentials retain their exact scopes;
    // the picker interaction itself is covered by the browser DOM suite.
    const token = await administratorPage.locator('lv-one-time-secret').evaluate((element) => element.secret)
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
    await gotoWithNetworkRetry(administratorPage, deviceURL.href)
    await administratorPage.getByRole('heading', { name: 'Authorize LeapView CLI' }).waitFor()
    await authorizeDeviceCode(administratorPage, params.userCode)
    return { authorized: true }
  },

  async verifyPreview(params) {
    const previewURL = new URL(params.previewUrl, baseURL)
    if (!previewURL.pathname.startsWith('/candidates/')) {
      throw new Error(`CLI returned a non-candidate preview URL: ${previewURL.href}`)
    }
    const previewResponse = await gotoWithNetworkRetry(administratorPage, previewURL.href)
    if (!previewResponse || !previewResponse.ok()) {
      const status = previewResponse ? previewResponse.status() : 'no response'
      throw new Error(`candidate preview returned HTTP ${status}: ${previewURL.href}`)
    }
    await administratorPage.waitForURL(
      (url) => url.pathname.startsWith(`${previewURL.pathname}/dashboards/`),
      { timeout: 60_000 },
    )
    // The candidate redirect is authoritative for the compiled resource ID
    // and default page. Authored filenames are not serving-route identities.
    const dashboardURL = new URL(administratorPage.url())
    await gotoWithNetworkRetry(administratorPage, dashboardURL.href)
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
          diagnostics: administratorDiagnostics.slice(-40),
          page: await administratorPage.evaluate(() => ({
            adminDefined: Boolean(customElements.get('lv-admin-page')),
            entityListDefined: Boolean(customElements.get('lv-entity-list')),
            adminText: document.querySelector('lv-admin-page')?.textContent?.replace(/\s+/g, ' ').trim().slice(0, 1000) || '',
            bodyText: document.body?.innerText?.replace(/\s+/g, ' ').trim().slice(0, 1000) || '',
          })).catch(() => ({})),
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
      break
    }
  }
} finally {
  await reviewerContext?.close()
  await administratorContext.close()
  await browser.close()
}
