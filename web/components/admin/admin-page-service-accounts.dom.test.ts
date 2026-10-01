import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('legacy service-account creation route opens the current creation dialog', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-service-accounts'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: {
          kind: 'admin', title: 'New service account', active: 'service-accounts-new',
          headerTitle: 'Create service account',
          headerDetail: 'Create a machine identity, then add a secret from its account page.',
        },
        adminServiceAccounts: { items: [], secrets: [], loading: false, hasMore: false },
      })
      const element = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const serviceAccounts = root.querySelector('lv-service-accounts') as any
      await serviceAccounts.updateComplete
      const serviceRoot = serviceAccounts.shadowRoot as ShadowRoot
      const dialog = serviceRoot.querySelector<HTMLDialogElement>('[data-service-account-dialog="create"]')
      return {
        hasGenericHeader: Boolean(root.querySelector('.main > .page-header')),
        pageHeading: serviceRoot.querySelector('.page-header h1')?.textContent?.trim(),
        dialogOpen: Boolean(dialog?.open),
        fieldLabel: dialog?.querySelector('input[name="displayName"]')?.getAttribute('aria-label'),
      }
    })

    expect(state).toEqual({
      hasGenericHeader: false,
      pageHeading: 'Service accounts',
      dialogOpen: true,
      fieldLabel: 'Display name',
    })
  } finally {
    await page.close()
  }
})

test('service-account detail route renders the selected account and list back link', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-service-accounts'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'admin', title: 'Service account', active: 'service-accounts-detail', headerTitle: 'Service accounts' },
        adminServiceAccounts: { items: [{ id: 'svc-1', displayName: 'Nightly build', kind: 'service_principal' }], selectedId: 'svc-1', secrets: [], loading: false, hasMore: false },
        adminAccess: { principals: [], groups: [], projects: [], sessions: [], roleAssignments: [], rolePresets: [], policyRevision: 1, activity: [], loading: false },
      })
      const element = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const serviceAccounts = root.querySelector('lv-service-accounts') as any
      await serviceAccounts.updateComplete
      const serviceRoot = serviceAccounts.shadowRoot as ShadowRoot
      return {
        hasGenericHeader: Boolean(root.querySelector('.main > .page-header')),
        title: serviceRoot.querySelector('.detail-surface h1')?.textContent?.trim(),
        backHref: serviceRoot.querySelector('.back-link')?.getAttribute('href'),
      }
    })
    expect(state).toEqual({ hasGenericHeader: false, title: 'Nightly build', backHref: '/admin/service-accounts' })
  } finally {
    await page.close()
  }
})
