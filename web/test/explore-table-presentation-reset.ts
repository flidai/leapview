import { expect } from 'bun:test'
import type { Page } from '@playwright/test'

export async function assertExploreTablePresentationReset(page: Page): Promise<void> {
  await page.waitForFunction(() => customElements.get('lv-data-explorer'))
  await page.evaluate(async () => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
    ;(window as any).__exploreResetMergePatch = mergePatch
    const object = { key: 'orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columns: [{ key: 'status', label: 'Status' }] }
    const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100, table: { columns: [{ field: 'orders.status', width: 220 }] } }
    const command = { spec, requestSeq: 5, resetVersion: 2, action: 'run', columnWidths: {} }
    mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, savedExplorations: {
      enabled: true, current: { id: 'saved-orders', title: 'Orders', visibility: 'private', status: 'active', detached: false, revision: { revisionId: 'revision-1' }, spec },
    }, dataExplorer: {
      objects: [object], selectedObject: object, selectedKey: object.key,
      command: { mode: 'explore', objectKey: object.key, explore: command },
      explore: { command, semanticModels: [], datasets: [], fields: [{ id: 'orders.status', kind: 'dimension', label: 'Status', datasetId: 'orders', compatible: true }],
        status: { state: 'success', requestSeq: 5, loading: false, stale: false },
        result: { columns: object.columns, rows: [{ status: 'paid' }], rowsReturned: 1, requestSeq: 5, durationMs: 1, truncated: false, warnings: [] } },
    } })
    const explorer = document.createElement('lv-data-explorer') as any
    explorer.embedded = true
    explorer.id = 'reset-table'
    document.body.append(explorer)
  })
  const explorer = page.locator('#reset-table')
  const header = explorer.locator('lv-data-explore-table [role="columnheader"]').first()
  await page.waitForFunction(() => {
    const explorer = document.querySelector('#reset-table') as any
    const table = explorer?.shadowRoot?.querySelector('lv-data-explore-table')
    const grid = table?.shadowRoot?.querySelector('lv-windowed-table')
    return explorer?.hasUpdated && !explorer.isUpdatePending && grid?.hasUpdated && !grid.isUpdatePending
  })
  const drag = async () => {
    const resizer = explorer.locator('lv-data-explore-table .column-resizer').first()
    const bounds = (await resizer.boundingBox())!
    const before = (await header.boundingBox())!.width
    await page.mouse.move(bounds.x + 1, bounds.y + bounds.height / 2)
    await page.mouse.down()
    await page.mouse.move(bounds.x + 1 + 100, bounds.y + bounds.height / 2, { steps: 4 })
    await page.waitForFunction((width) => {
      const explorer = document.querySelector('#reset-table') as any
      return explorer?.shadowRoot?.querySelector('lv-data-explore-table')?.shadowRoot?.querySelector('lv-windowed-table')?.shadowRoot?.querySelector('[role="columnheader"]')?.getBoundingClientRect().width > width
    }, before + 90)
    await page.mouse.up()
    await page.waitForFunction(() => Object.keys((document.querySelector('#reset-table') as any).exploreColumnWidths).length > 0)
    expect((await header.boundingBox())!.width).toBeGreaterThan(before + 90)
  }
  expect((await header.boundingBox())!.width).toBe(220)
  await drag()
  await explorer.evaluate((element: any) => {
    const mergePatch = (window as any).__exploreResetMergePatch
    const spec = { ...element.dataExplorer.explore.command.spec, table: { columns: [{ field: 'orders.status', width: 180 }] } }
    mergePatch({ savedExplorations: { current: { revision: { revisionId: 'revision-2' }, spec } }, dataExplorer: { explore: { command: { spec } } } })
  })
  await page.waitForFunction(() => {
    const explorer = document.querySelector('#reset-table') as any
    return explorer.savedExplorations.current.revision.revisionId === 'revision-2' && Object.keys(explorer.exploreColumnWidths).length === 0 && !explorer.isUpdatePending
  })
  expect((await header.boundingBox())!.width).toBe(180)

  await drag()
  await explorer.getByRole('button', { name: 'Return to all table columns', exact: true }).click()
  await page.waitForFunction(() => Object.keys((document.querySelector('#reset-table') as any).exploreColumnWidths).length === 0)
  expect((await header.boundingBox())!.width).toBe(180)

  await drag()
  await explorer.evaluate(() => {
    const mergePatch = (window as any).__exploreResetMergePatch
    mergePatch({ dataExplorer: { explore: { result: { error: 'Query failed' }, status: { state: 'error' } } } })
  })
  await explorer.getByRole('button', { name: 'Reset query', exact: true }).click()
  expect(await explorer.evaluate((node: any) => node.exploreColumnWidths)).toEqual({})
}
