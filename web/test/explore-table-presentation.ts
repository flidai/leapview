import { expect } from 'bun:test'
import type { Page } from '@playwright/test'

export async function assertExploreTablePresentation(page: Page): Promise<void> {
  await page.waitForFunction(() => customElements.get('lv-data-explore-table'))
  await page.evaluate(() => {
    const table = document.createElement('lv-data-explore-table') as any
    table.id = 'authored-table'
    table.style.cssText = 'width:1000px;height:400px;--lv-bg-app:white;--lv-bg-panel-muted:black;--lv-table-stripe:black'
    table.command = { spec: {
      version: 1, mode: 'records', modelId: 'sales', datasetId: 'orders',
      dimensions: [{ field: 'orders.name', alias: 'customer' }, { field: 'orders.amount', alias: 'revenue' }, { field: 'orders.id' }], metrics: [],
      table: { columns: [
        { field: 'orders.amount', label: 'Order total', width: 240, format: { kind: 'currency', currency: 'USD' } },
        { field: 'customer', label: 'Buyer', width: 180 },
      ], density: 'comfortable', striped: true, showHeader: true },
    }, columnWidths: { revenue: 280 } }
    const rows = [{ customer: 'Ada', revenue: '1234.50', id: 7 }, { customer: 'Ada', revenue: '1234.50', id: 7 }]
    table.result = { columns: [{ key: 'customer', label: 'Name', type: 'string' }, { key: 'revenue', label: 'Amount', type: 'decimal' }, { key: 'id', label: 'ID', type: 'int' }], rows: [],
      window: { totalRows: 2, availableRows: 2, chunkSize: 100, resetVersion: 1, blocks: { a: { start: 0, requestSeq: 1, resetVersion: 1, sort: {}, rows } } } }
    document.body.append(table)
  })
  const table = page.locator('#authored-table')
  const headers = table.locator('[role="columnheader"]')
  await headers.first().waitFor()
  expect(await headers.locator('.header-label').allTextContents()).toEqual(['Order total', 'Buyer'])
  expect(await headers.nth(0).evaluate(node => node.getBoundingClientRect().width)).toBe(280)
  expect(await headers.nth(1).evaluate(node => node.getBoundingClientRect().width)).toBe(180)
  const rows = table.locator('.row:not([aria-busy])')
  expect(await rows.count()).toBe(2)
  expect(await rows.nth(0).locator('[role="cell"]').evaluateAll(nodes => nodes.map(node => node.textContent?.trim()))).toEqual(['$1,234.50', 'Ada'])
  expect(await rows.nth(1).locator('[role="cell"]').evaluateAll(nodes => nodes.map(node => node.textContent?.trim()))).toEqual(['$1,234.50', 'Ada'])
  expect(await rows.nth(1).evaluate(node => (node as HTMLElement).style.top)).toBe('36px')
  expect(await rows.nth(0).evaluate(node => getComputedStyle(node).backgroundColor)).not.toBe(await rows.nth(1).evaluate(node => getComputedStyle(node).backgroundColor))
  expect(await table.evaluate((node: any) => node.shadowRoot.querySelector('lv-windowed-table').table.blocks === node.result.window.blocks)).toBe(true)

  await table.evaluate((node: any) => {
    node.command = { ...node.command, spec: { ...node.command.spec, table: { ...node.command.spec.table, rowHeight: 42, striped: false, showHeader: false } } }
  })
  await headers.first().waitFor({ state: 'detached' })
  expect(await rows.nth(1).evaluate(node => (node as HTMLElement).style.top)).toBe('42px')
  expect(await rows.nth(0).evaluate(node => getComputedStyle(node).backgroundColor)).toBe(await rows.nth(1).evaluate(node => getComputedStyle(node).backgroundColor))

  await table.evaluate((node: any) => {
    node.command = { ...node.command, spec: { ...node.command.spec, table: { density: 'compact' }, visualization: { kind: 'table', columns: [{ field: 'orders.amount', format: { kind: 'number', minimumFractionDigits: 1, maximumFractionDigits: 1 } }] } } }
  })
  await headers.first().waitFor()
  expect(await headers.locator('.header-label').allTextContents()).toEqual(['Amount'])
  expect(await rows.nth(0).locator('[role="cell"]').evaluateAll(nodes => nodes.map(node => node.textContent?.trim()))).toEqual(['1,234.5'])
  expect(await rows.nth(1).evaluate(node => (node as HTMLElement).style.top)).toBe('28px')

  await table.evaluate((node: any) => {
    node.command = { ...node.command, spec: { ...node.command.spec, table: { columns: [{ field: 'removed.field' }] }, visualization: undefined } }
  })
  await page.waitForFunction(() => document.querySelector('#authored-table')?.shadowRoot?.querySelector('lv-windowed-table')?.shadowRoot?.querySelectorAll('[role="columnheader"]').length === 3)
  expect(await headers.locator('.header-label').allTextContents()).toEqual(['Name', 'Amount', 'ID'])
  expect(await rows.nth(1).evaluate(node => (node as HTMLElement).style.top)).toBe('32px')
}
