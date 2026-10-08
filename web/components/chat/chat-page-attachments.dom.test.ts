import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

for (const viewport of [
  { name: 'desktop', width: 1280, height: 820 },
  { name: 'narrow desktop', width: 700, height: 820 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`new chat opens the file picker and manages attachments on ${viewport.name}`, async () => {
    const page = await fixture.browser.newPage({ viewport })
    try {
      await page.goto(`${fixture.baseURL}/new`)
      await page.waitForFunction(() => customElements.get('lv-chat-composer'))
      const attach = page.getByRole('button', { name: 'Add files', exact: true })
      expect(await attach.isVisible()).toBe(true)
      expect(await attach.isEnabled()).toBe(true)

      const chooserPromise = page.waitForEvent('filechooser')
      await attach.click()
      const chooser = await chooserPromise
      await chooser.setFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('Revenue increased this month.') })
      const remove = page.getByRole('button', { name: 'Remove notes.txt', exact: true })
      await remove.waitFor()
      expect(await page.locator('lv-chat-composer').getByText('notes.txt', { exact: true }).count()).toBe(1)
      await remove.click()
      expect(await page.locator('lv-chat-composer').getByText('notes.txt', { exact: true }).count()).toBe(0)
    } finally {
      await page.close()
    }
  })
}
