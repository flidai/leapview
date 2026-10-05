import { chromium, expect, type Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
type AccessState = {
  groups: Array<{ id: string, name: string, provider: string }>
  principals: Array<{ id: string, email: string, displayName: string }>
  policyRevision: number
  roleAssignments: Array<{ bindingId: string, role: string, subjectId: string, subjectType: string }>
  rolePresets: Array<{ name: string, role: string }>
  selectedGroupId?: string
}
type ServiceAccountsState = {
  items: Array<{ id: string, displayName: string }>
  selectedId?: string
}

const useDevQuickLogin = Bun.env.LEAPVIEW_DEV_QUICK_LOGIN === 'true'
if (!useDevQuickLogin) {
  throw new Error('Set LEAPVIEW_DEV_QUICK_LOGIN=true to authorize the local smoke login.')
}
const managedPort = await readFile(resolve('.tmp/dev-server.port'), 'utf8').catch(() => '')
if (!Bun.env.LEAPVIEW_BASE_URL && !managedPort.trim()) {
  throw new Error('Set LEAPVIEW_BASE_URL or start the managed dev server so .tmp/dev-server.port is available.')
}
const baseURL = (Bun.env.LEAPVIEW_BASE_URL ?? `http://127.0.0.1:${managedPort.trim()}`).replace(/\/$/, '')
const baseAddress = new URL(baseURL)
if (baseAddress.protocol !== 'http:' || !['localhost', '127.0.0.1', '::1'].includes(baseAddress.hostname)) {
  throw new Error(`Refusing to run mutable access smoke outside a local HTTP address: ${baseAddress.origin}`)
}
const suffix = `${new Date().toISOString().replace(/[^0-9]/g, '').slice(0, 14)}-${crypto.randomUUID().slice(0, 8)}`
const groupName = `access-smoke-${suffix}`
const serviceAccountName = `access-smoke-${suffix}`

const browser = await chromium.launch()
const context = await browser.newContext({ viewport: { width: 1365, height: 900 } })
const page = await context.newPage()
page.on('dialog', (dialog) => dialog.accept())
page.on('pageerror', (error) => console.error(`Browser page error: ${error.message}`))

let createdGroupID = ''
let createdServiceAccountID = ''
let createdRoleBindingID = ''
let smokeFailed = false
const coverage: string[] = []

try {
  await signIn(page)

  await openAdminRoute(page, '/admin/access')
  await expect(page.getByRole('heading', { name: 'Access overview' })).toBeVisible()
  const overviewState = await accessState(page)
  expect(overviewState.rolePresets.length).toBeGreaterThan(0)
  coverage.push('access overview')

  await openAdminRoute(page, '/admin/principals')
  await expect(page.getByRole('heading', { name: 'Users', exact: true })).toBeVisible()
  const users = await accessState(page)
  expect(users.principals.length).toBeGreaterThan(0)
      const principal = users.principals[0]
  const principalName = principal.displayName || principal.email
  const adminPage = page.locator('lv-admin-page')
  const principalLink = adminPage.locator(`a[href="/admin/principals/${encodeURIComponent(principal.id)}"]`)
  await expect(principalLink).toBeVisible()
  await principalLink.click()
  await page.waitForURL((url) => url.pathname === `/admin/principals/${encodeURIComponent(principal.id)}`)
  await expect(page.getByRole('heading', { name: principalName, exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Access', exact: true })).toBeVisible()
  coverage.push('users list and detail')

  await openAdminRoute(page, '/admin/groups')
  await expect(page.getByRole('heading', { name: 'Groups', exact: true })).toBeVisible()
  const groupsBefore = await accessState(page)
  expect(groupsBefore.groups).toBeDefined()
  coverage.push('groups list')

  await page.getByRole('button', { name: 'Create group', exact: true }).click()
  const createGroupDialog = page.locator('dialog[data-access-create-dialog]')
  await createGroupDialog.getByLabel('Group name').fill(groupName)
  await createGroupDialog.getByRole('button', { name: 'Create group', exact: true }).click()
  await page.waitForURL((url) => url.pathname.startsWith('/admin/groups/'))
  const groupPathID = decodeURIComponent(new URL(page.url()).pathname.split('/').at(-1) ?? '')
  createdGroupID = groupPathID
  await expect(page.getByRole('heading', { name: groupName, exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Members', exact: true })).toBeVisible()
  await page.getByRole('link', { name: 'All groups', exact: true }).click()
  await page.waitForURL((url) => url.pathname === '/admin/groups')
  await expect(adminPage.getByRole('link', { name: groupName, exact: true })).toBeVisible()
  await adminPage.getByRole('link', { name: groupName, exact: true }).click()
  await page.waitForURL((url) => url.pathname === `/admin/groups/${encodeURIComponent(createdGroupID)}`)
  await expect(page.getByRole('heading', { name: groupName, exact: true })).toBeVisible()
  coverage.push('group detail')

  await openAdminRoute(page, '/admin/service-accounts')
  await expect(page.getByRole('heading', { name: 'Service accounts', exact: true })).toBeVisible()
  const initialServiceAccounts = await serviceAccountsState(page)
  expect(initialServiceAccounts?.items).toBeDefined()
  coverage.push('service accounts list')

  await page.getByRole('button', { name: 'Create service account', exact: true }).click()
  const createServiceDialog = page.locator('dialog[data-service-account-dialog="create"]')
  await createServiceDialog.getByLabel('Display name').fill(serviceAccountName)
  await createServiceDialog.getByRole('button', { name: 'Create service account', exact: true }).click()
  let recoveredCreate = false
  try {
    await expect.poll(async () => {
      const item = (await serviceAccountsState(page))?.items.find((candidate) => candidate.displayName === serviceAccountName)
      if (item) createdServiceAccountID = item.id
      return Boolean(item)
    }).toBe(true)
  } catch (error) {
    const alerts = await page.locator('lv-service-accounts').getByRole('alert').allTextContents()
    try {
      await openAdminRoute(page, '/admin/service-accounts')
      const recovered = (await serviceAccountsState(page))?.items.find((item) => item.displayName === serviceAccountName)
      if (recovered) {
        createdServiceAccountID = recovered.id
        recoveredCreate = true
      }
    } catch {
      // Preserve the create failure; the exact error is recorded below.
    }
    if (!createdServiceAccountID) {
      throw new Error(`Service account create did not appear in the signal; visible error: ${alerts.join(' | ') || 'none'}; ${errorMessage(error)}`)
    }
  }
  expect(createdServiceAccountID).not.toBe('')
  if (!recoveredCreate) await expect(page.getByRole('heading', { name: serviceAccountName, exact: true })).toBeVisible()

  await openAdminRoute(page, `/admin/service-accounts/${encodeURIComponent(createdServiceAccountID)}`)
  await expect(page.getByRole('heading', { name: serviceAccountName, exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Credentials', exact: true })).toBeVisible()
  expect((await serviceAccountsState(page))?.selectedId).toBe(createdServiceAccountID)
  coverage.push('service account detail')

  const serviceAccess = await accessState(page)
  const role = serviceAccess.rolePresets.find((preset) => preset.role === 'project_admin')
  expect(role).toBeDefined()
  expect(serviceAccess.policyRevision).toBeGreaterThan(0)
  const mutationUnavailable = await page.locator('lv-admin-page').evaluate((element: any) => element.signals?.adminAccess?.roleMutationUnavailableReason ?? '')
  expect(mutationUnavailable).toBe('')

  await grantRole(page, role!.name, serviceAccess.policyRevision - 1)
  await expect(page.locator('lv-service-accounts').getByRole('alert')).toBeVisible()
  coverage.push('service-account role grant conflict shown as visible error')

  await grantRole(page, role!.name)
  const serviceUI = page.locator('lv-service-accounts')
  await expect(serviceUI.getByRole('status').filter({ hasText: 'Role assigned.' })).toBeVisible()
  await expect(serviceUI.getByRole('table').getByRole('row').filter({ hasText: role!.name })).toBeVisible()
  let postGrantState = await accessState(page)
  const grant = postGrantState.roleAssignments.find((assignment) => assignment.subjectId === createdServiceAccountID && assignment.role === role!.role)
  expect(grant).toBeDefined()
  createdRoleBindingID = grant!.bindingId

  await openAdminRoute(page, `/admin/service-accounts/${encodeURIComponent(createdServiceAccountID)}`)
  await expect(serviceUI.getByRole('table').getByRole('row').filter({ hasText: role!.name })).toBeVisible()
  postGrantState = await accessState(page)
  expect(postGrantState.roleAssignments.some((assignment) => assignment.bindingId === createdRoleBindingID)).toBe(true)
  coverage.push('Project Admin grant visibly persisted after service-account detail reload')

  const roleRow = serviceUI.getByRole('table').getByRole('row').filter({ hasText: role!.name })
  await roleRow.getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(serviceUI.getByRole('status').filter({ hasText: 'Role removed.' })).toBeVisible()
  await expect.poll(async () => (await accessState(page)).roleAssignments.some((assignment) => assignment.bindingId === createdRoleBindingID)).toBe(false)
  createdRoleBindingID = ''
  coverage.push('service-account role revoke visibly removed persisted assignment')

  await openAdminRoute(page, '/admin/access')
  await expect(page.getByRole('heading', { name: 'Access overview' })).toBeVisible()
  expect((await accessState(page)).roleAssignments.some((assignment) => assignment.subjectId === createdServiceAccountID && assignment.role === role!.role)).toBe(false)

  console.log(`Access administration browser smoke passed at ${baseURL}: ${coverage.join('; ')}`)
} catch (error) {
  smokeFailed = true
  throw error
} finally {
  const cleanupFailures = await bestEffortCleanup(page)
  await context.close()
  await browser.close()
  if (cleanupFailures.length) {
    const summary = `Generated access-admin smoke records need cleanup: ${cleanupFailures.join('; ')}`
    if (smokeFailed) console.error(summary)
    else throw new Error(summary)
  }
}

async function signIn(page: Page): Promise<void> {
  await page.goto(`${baseURL}/admin/access`, { waitUntil: 'domcontentloaded' })
  if (new URL(page.url()).pathname === '/login') {
    await page.getByRole('button', { name: 'Continue as Local Developer', exact: true }).click()
    await page.waitForURL((url) => url.pathname !== '/login')
  }
  await openAdminRoute(page, '/admin/access')
}

async function openAdminRoute(page: Page, path: string): Promise<void> {
  const response = await page.goto(`${baseURL}${path}`, { waitUntil: 'domcontentloaded' })
  if (!response?.ok()) throw new Error(`${path} returned HTTP ${response?.status() ?? 'unknown'}`)
  if (new URL(page.url()).pathname !== path) throw new Error(`${path} redirected to ${new URL(page.url()).pathname}`)
  await page.locator('lv-admin-page').waitFor()
  await page.waitForFunction((expectedPath) => {
    const root = document.querySelector('lv-admin-page') as (HTMLElement & { signals?: Record<string, any> }) | null
    if (!root?.signals?.page) return false
    if (expectedPath.startsWith('/admin/service-accounts')) return root.signals.adminServiceAccounts?.loading === false
    return root.signals.adminAccess?.loading === false
  }, path)
}

async function accessState(page: Page): Promise<AccessState> {
  return page.locator('lv-admin-page').evaluate((element: any) => JSON.parse(JSON.stringify(element.signals?.adminAccess)))
}

async function serviceAccountsState(page: Page): Promise<ServiceAccountsState | null> {
  return page.locator('lv-admin-page').evaluate((element: any) => {
    const state = element.signals?.adminServiceAccounts
    return state === undefined ? null : JSON.parse(JSON.stringify(state))
  })
}

async function grantRole(page: Page, roleName: string, staleRevision?: number): Promise<void> {
  if (staleRevision !== undefined) {
    await page.evaluate(async (revision) => {
      const moduleURL = '/static/vendor/datastar-1.0.2.js?v=dev'
      const { mergePatch } = await import(moduleURL) as any
      mergePatch({ adminAccess: { policyRevision: revision } })
    }, staleRevision)
    await page.waitForFunction((revision) => {
      const root = document.querySelector('lv-admin-page') as (HTMLElement & { signals?: Record<string, any> }) | null
      return root?.signals?.adminAccess?.policyRevision === revision
    }, staleRevision)
  }
  await page.getByRole('button', { name: 'Grant role', exact: true }).click()
  await page.getByRole('radio', { name: new RegExp(escapeRegExp(roleName)) }).check()
  await page.getByRole('button', { name: 'Grant access', exact: true }).click()
}

async function bestEffortCleanup(page: Page): Promise<string[]> {
  const failures: string[] = []
  if (new URL(page.url()).origin !== baseURL) return failures
  try {
    if (createdServiceAccountID) {
      await openAdminRoute(page, `/admin/service-accounts/${encodeURIComponent(createdServiceAccountID)}`)
      const serviceRole = (await accessState(page)).roleAssignments.find((assignment) =>
        assignment.subjectId === createdServiceAccountID && (createdRoleBindingID === '' || assignment.bindingId === createdRoleBindingID),
      )
      if (serviceRole) {
        const row = page.locator('lv-service-accounts').getByRole('table').getByRole('row').filter({ hasText: humanizeRole(serviceRole.role) })
        await row.getByRole('button', { name: 'Remove', exact: true }).click()
        await expect.poll(async () => (await accessState(page)).roleAssignments.some((assignment) => assignment.bindingId === serviceRole.bindingId)).toBe(false)
      }
      createdRoleBindingID = ''
    }
  } catch (error) {
    failures.push(`revoke generated service-account role ${createdServiceAccountID}/${createdRoleBindingID}: ${errorMessage(error)}`)
  }
  try {
    if (createdGroupID) {
      await openAdminRoute(page, `/admin/groups/${encodeURIComponent(createdGroupID)}`)
      await page.getByText('More actions', { exact: true }).click()
      await page.getByRole('button', { name: 'Delete group', exact: true }).click()
      await expect.poll(async () => new URL(page.url()).pathname).toBe('/admin/groups')
      createdGroupID = ''
    }
  } catch (error) {
    failures.push(`delete generated group ${createdGroupID}: ${errorMessage(error)}`)
  }
  try {
    if (createdServiceAccountID) {
      await openAdminRoute(page, `/admin/service-accounts/${encodeURIComponent(createdServiceAccountID)}`)
      await page.getByRole('button', { name: 'Delete service account', exact: true }).first().click()
      await page.getByRole('dialog').getByRole('button', { name: 'I understand, delete this service account', exact: true }).click()
      await expect.poll(async () => (await serviceAccountsState(page))?.items.some((item) => item.id === createdServiceAccountID) ?? false).toBe(false)
      createdServiceAccountID = ''
    }
  } catch (error) {
    failures.push(`delete generated service account ${createdServiceAccountID}: ${errorMessage(error)}`)
  }
  return failures
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function humanizeRole(role: string): string {
  return role.split(/[_-]+/).filter(Boolean).map((part) => part[0].toUpperCase() + part.slice(1)).join(' ')
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
