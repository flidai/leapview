import { chromium, expect, type Locator } from '@playwright/test'
import { mkdir } from 'node:fs/promises'

// Read-only browser checks against the assets actually deployed on this instance.
// LEAPVIEW_BASE_URL=http://localhost:8108 bun scripts/lineage_route_qa.ts
const baseURL = Bun.env.LEAPVIEW_BASE_URL ?? 'http://localhost:8108'
const output = '.artifacts/lineage'
const categories = ['sources', 'connections', 'semantic-models', 'pipelines', 'dashboards', 'models']
type Graph = {
  nodes: { id: string; label: string; href?: string; selected?: boolean }[]
  edges: { source: string; target: string }[]
}
type Result = {
  category: string; route: string; nodes?: number; edges?: number
  upstream?: number; downstream?: number; narrow?: boolean; error?: string
}
const results: Result[] = []
const errors: { route: string; message: string }[] = []
const checkedLinks = new Set<string>()
const browser = await chromium.launch()
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
const page = await context.newPage()
page.on('pageerror', error => errors.push({ route: page.url(), message: error.message }))
page.on('console', message => {
  if (message.type() === 'error') errors.push({ route: page.url(), message: message.text() })
})
await mkdir(output, { recursive: true })

function reachable(graph: Graph, id: string, direction: 'upstream' | 'downstream'): Set<string> {
  const result = new Set<string>()
  const pending = [id]
  while (pending.length) {
    const current = pending.pop()!
    for (const edge of graph.edges) {
      const adjacent = direction === 'upstream'
        ? edge.target === current ? edge.source : undefined
        : edge.source === current ? edge.target : undefined
      if (adjacent && adjacent !== id && !result.has(adjacent)) {
        result.add(adjacent)
        pending.push(adjacent)
      }
    }
  }
  return result
}

async function verifyDirections(graph: Locator, data: Graph, id: string) {
  const counts = { upstream: reachable(data, id, 'upstream').size, downstream: reachable(data, id, 'downstream').size }
  for (const direction of ['upstream', 'downstream'] as const) {
    const label = direction === 'upstream' ? 'Upstream' : 'Downstream'
    const button = graph.getByRole('button', { name: new RegExp(`^${label}`) })
    await expect(button).toHaveText(`${label} (${counts[direction]})`)
    if (!counts[direction]) await expect(button).toBeDisabled()
    else {
      await expect(button).toBeEnabled()
      await button.click()
      await expect(button).toHaveAttribute('aria-pressed', 'true')
      await expect(graph.locator('.react-flow__node')).toHaveCount(counts[direction] + 1)
    }
  }
  return counts
}

async function verifyZoom(graph: Locator) {
  await graph.getByRole('button', { name: 'Focus selected', exact: true }).click()
  const zoom = graph.getByRole('status', { name: 'Zoom level' })
  await expect(zoom).toHaveText('100%')
  await graph.getByRole('button', { name: 'Zoom in', exact: true }).click()
  await expect(zoom).toHaveText('125%')
  await graph.getByRole('button', { name: 'Zoom out', exact: true }).click()
  await expect(zoom).toHaveText('100%')
  await graph.getByRole('button', { name: 'Zoom in', exact: true }).click()
  await graph.getByRole('button', { name: 'Actual size (100%)', exact: true }).click()
  await expect(zoom).toHaveText('100%')
  const fit = graph.getByRole('button', { name: 'Fit graph', exact: true })
  if (await fit.isEnabled()) await fit.click()
  await expect(fit).toBeDisabled()
}

async function verifyExpansion(graph: Locator, narrow: boolean, screenshot: string) {
  const selection = await graph.getByRole('combobox', { name: 'Find asset' }).inputValue()
  const direction = await graph.getByRole('group', { name: 'Trace dependencies in this view' }).locator('[aria-pressed="true"]').textContent()
  const expand = graph.getByRole('button', { name: 'Expand to full page', exact: true })
  await expand.click()
  const dialog = graph.getByRole('dialog', { name: 'Full-page lineage explorer' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Exit full page', exact: true })).toBeFocused()
  if (narrow) await page.setViewportSize({ width: 390, height: 844 })
  const size = page.viewportSize()!
  expect(await dialog.boundingBox()).toEqual({ x: 0, y: 0, ...size })
  await verifyZoom(graph)
  await page.screenshot({ path: screenshot, fullPage: true })
  if (narrow) await dialog.getByRole('button', { name: 'Exit full page', exact: true }).press('Escape')
  else await dialog.getByRole('button', { name: 'Exit full page', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  await expect(expand).toBeFocused()
  await expect(graph.getByRole('combobox', { name: 'Find asset' })).toHaveValue(selection)
  await expect(graph.getByRole('group', { name: 'Trace dependencies in this view' }).locator('[aria-pressed="true"]')).toHaveText(direction!)
  expect(await page.evaluate(() => document.documentElement.style.overflow)).toBe('')
  if (narrow) {
    const bounds = await graph.getByRole('group', { name: 'Graph view' }).boundingBox()
    expect(bounds!.x).toBeGreaterThanOrEqual(0)
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390)
    await page.screenshot({ path: screenshot.replace('.png', '-embedded.png'), fullPage: true })
  }
}

try {
  for (const category of categories) {
    await page.goto(`${baseURL}/${category}`)
    const detailLinks = page.locator(`a[href^="/${category}/"][href$="/details"]`)
    await expect(detailLinks.first()).toBeVisible()
    const details = [...new Set(await detailLinks.evaluateAll(links => links.map(link => link.getAttribute('href')!)))]
    for (const [index, detail] of details.entries()) {
      const result: Result = { category, route: detail }
      results.push(result)
      try {
        await page.setViewportSize({ width: 1440, height: 1000 })
        await page.goto(`${baseURL}${detail}`)
        const lineage = page.getByRole('link', { name: 'Lineage', exact: true })
        await expect(lineage).toBeVisible()
        result.route = (await lineage.getAttribute('href'))!
        await lineage.click()
        const graph = page.locator('lv-asset-lineage-graph')
        const picker = graph.getByRole('combobox', { name: 'Find asset' })
        await expect(picker).toBeVisible()
        const data: Graph = await graph.evaluate((element: any) => element.graph)
        result.nodes = data.nodes.length
        result.edges = data.edges.length
        expect(data.nodes.length).toBeGreaterThan(0)
        const initial = await picker.inputValue()
        expect(data.nodes.find(node => node.id === initial)?.selected).toBe(true)
        const counts = await verifyDirections(graph, data, initial)
        Object.assign(result, counts)
        await graph.getByRole('button', { name: 'Show all', exact: true }).click()
        await expect(graph.locator('.react-flow__node')).toHaveCount(data.nodes.length)
        // Exercise every selectable asset and verify its destination and counts.
        for (const node of data.nodes) {
          await picker.selectOption(node.id)
          await expect(graph.locator('.asset-lineage-selection strong')).toHaveText(node.label)
          if (node.href) await expect(graph.getByRole('link', { name: 'Open asset', exact: true })).toHaveAttribute('href', node.href)
          for (const direction of ['upstream', 'downstream'] as const) {
            const label = direction === 'upstream' ? 'Upstream' : 'Downstream'
            await expect(graph.getByRole('button', { name: new RegExp(`^${label}`) })).toHaveText(`${label} (${reachable(data, node.id, direction).size})`)
          }
        }
        await graph.getByRole('button', { name: 'Show all', exact: true }).click()
        const selectable = graph.locator('.asset-lineage-node').first()
        await selectable.focus()
        await selectable.press('Enter')
        await expect(selectable).toHaveAttribute('aria-pressed', 'true')
        await picker.selectOption(initial)
        await verifyZoom(graph)
        // Read-only checks of graph and dependency destinations, once per URL.
        const links = [...data.nodes.flatMap(node => node.href ? [node.href] : []), ...await page.locator('a[href$="/details"]').evaluateAll(anchors => anchors.map(anchor => anchor.getAttribute('href')!))]
        for (const href of links) {
          if (checkedLinks.has(href)) continue
          const response = await context.request.get(`${baseURL}${href}`)
          expect(response.status(), href).toBe(200)
          checkedLinks.add(href)
        }
        await verifyDirections(graph, data, initial)
        const slug = `${category}-${index}`
        await verifyExpansion(graph, false, `${output}/cross-route-${slug}-desktop.png`)
        if (index === 0) {
          await verifyExpansion(graph, true, `${output}/cross-route-${slug}-narrow.png`)
          result.narrow = true
        }
        console.log(`PASS ${result.route}: ${data.nodes.length} nodes, ${data.edges.length} edges, upstream ${counts.upstream}, downstream ${counts.downstream}${result.narrow ? ', narrow checked' : ''}`)
      } catch (error) {
        result.error = String(error)
        console.error(`FAIL ${result.route}: ${error}`)
        await page.screenshot({ path: `${output}/cross-route-failure-${category}-${index}.png`, fullPage: true }).catch(() => {})
      }
    }
  }
} finally {
  await Bun.write(`${output}/cross-route-report.json`, JSON.stringify({ baseURL, checkedAt: new Date().toISOString(), results, checkedLinks: [...checkedLinks], errors }, null, 2))
  await browser.close()
}
expect(results.filter(result => result.error), 'Route failures').toEqual([])
expect(errors, 'Browser errors').toEqual([])
console.log(`Lineage QA passed: ${results.length} routes, ${checkedLinks.size} unique destinations, ${results.filter(result => result.narrow).length} narrow samples.`)
