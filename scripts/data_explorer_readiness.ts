import type { Locator, Page } from '@playwright/test'

/** Select a browse-mode object and wait for its complete command response. */
export async function selectDataExplorerObject(page: Page, object: Locator): Promise<void> {
  // The preview can render optimistically before the authoritative SSE signal
  // patch settles, so callers can safely inject test state afterward.
  const selectionResponsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return url.pathname === '/explore/command' && response.request().method() === 'POST'
  })
  await object.click()
  const selectionResponse = await selectionResponsePromise
  await selectionResponse.body()
  if (!selectionResponse.ok()) {
    throw new Error(`/explore recovery selection: status ${selectionResponse.status()}`)
  }
}
