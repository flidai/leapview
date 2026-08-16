import { chromium, expect, type Page } from '@playwright/test'
import { hasMixedSpatialPrecision } from './spatial_precision_summary'

type RouteExpectation = {
  path: string
  root: string
  shell: boolean
}

const baseURL = Bun.env.LEAPVIEW_BASE_URL ?? 'http://localhost:8195'
const dashboardPath = '/dashboards/dashboard:visual-showcase/pages/overview'
const routes: RouteExpectation[] = [
  { path: '/', root: 'lv-catalog-page', shell: true },
  { path: dashboardPath, root: 'lv-dashboard-page', shell: true },
  { path: '/explore', root: 'lv-data-explorer', shell: true },
  { path: '/data', root: 'lv-project-page', shell: true },
  { path: '/models', root: 'lv-project-page', shell: true },
  { path: '/semantic-models', root: 'lv-project-page', shell: true },
  { path: '/pipelines', root: 'lv-pipelines-page', shell: true },
  { path: '/connections', root: 'lv-connections-page', shell: true },
  { path: '/admin', root: 'lv-admin-page', shell: true },
  { path: '/chats', root: 'lv-chat-page', shell: true },
  { path: '/login', root: 'lv-login-page', shell: false },
]

const browser = await chromium.launch()
try {
  for (const route of routes) {
    await verifyRoute(route)
  }
  await verifyEChartsFirstNavigation()
  await verifyDashboardCommandDoesNotReopenUpdates()
  await verifyFilterShowcase()
  await verifySpatialShowcaseMaps()
  await verifySpatialMapWindowing()
  console.log(`DatastarLit route QA passed for ${routes.length} routes at ${baseURL}`)
} finally {
  await browser.close()
}

async function verifyRoute(route: RouteExpectation): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)
  const updates: string[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/updates') updates.push(request.url())
  })

  try {
    const response = await page.goto(new URL(route.path, baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) {
      throw new Error(`${route.path}: status ${response?.status() ?? 'unknown'}`)
    }
    await page.waitForSelector(route.root)
    await page.waitForFunction((expectedRoot) => {
      if (expectedRoot === 'lv-chat-page') return true
      const root = document.querySelector(expectedRoot)
      return (root?.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim().length ?? 0) > 0
    }, route.root, { timeout: 5000 })
    await waitForUpdatesRequest(route.path, updates)
    const state = await page.evaluate((expectedRoot) => {
      const root = document.querySelector(expectedRoot)
      return {
        root: root?.localName ?? '',
        shell: Boolean(document.querySelector('lv-app-shell')),
        shadowText: root?.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
        datastarScriptCount: document.querySelectorAll('script[src*="datastar-1.0.2"]').length,
      }
    }, route.root)

    if (state.root !== route.root) throw new Error(`${route.path}: mounted ${state.root || 'no root'}, want ${route.root}`)
    if (state.shell !== route.shell) throw new Error(`${route.path}: shell=${state.shell}, want ${route.shell}`)
    if (state.shadowText.length === 0 && route.root !== 'lv-chat-page') throw new Error(`${route.path}: route root rendered no shadow text`)
    if (state.datastarScriptCount !== 1) throw new Error(`${route.path}: Datastar script count=${state.datastarScriptCount}, want 1`)
    if (updates.length !== 1) throw new Error(`${route.path}: /updates request count=${updates.length}, want 1`)
    assertNoBlockingConsoleMessages(route.path, messages)
  } finally {
    await page.close()
  }
}

async function verifyEChartsFirstNavigation(): Promise<void> {
  const catalogPath = '/'
  const dashboardHref = '/dashboards/dashboard:visual-showcase'
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)

  try {
    const response = await page.goto(new URL(catalogPath, baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`${catalogPath}: status ${response?.status() ?? 'unknown'}`)
    await page.locator(`a[href="${dashboardHref}"]`).click()
    await page.waitForURL(`**${dashboardPath}`)
    try {
      await page.waitForFunction(() => {
        const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
        const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }>
        const chart = hosts.find((host) => host.envelope?.visualID === 'revenue')
        const renderer = chart?.shadowRoot?.querySelector('.renderer')
        const canvas = renderer?.querySelector('canvas') as HTMLCanvasElement | null
        const context = canvas?.getContext('2d', { willReadFrequently: true })
        const pixels = context && canvas ? context.getImageData(0, 0, canvas.width, canvas.height).data : undefined
        let dataPixels = 0
        if (pixels) {
          for (let offset = 0; offset < pixels.length; offset += 16) {
            const red = pixels[offset] ?? 0
            const green = pixels[offset + 1] ?? 0
            const blue = pixels[offset + 2] ?? 0
            const alpha = pixels[offset + 3] ?? 0
            if (alpha > 0 && blue > red + 50 && blue > green + 35) dataPixels++
          }
        }
        return chart?.envelope?.status?.kind === 'ready'
          && renderer?.getAttribute('aria-hidden') === 'false'
          && canvas?.localName === 'canvas'
          && canvas.width > 0
          && canvas.height > 0
          && dataPixels > 500
      }, undefined, { timeout: 30_000, polling: 100 })
    } catch (error) {
      const state = await page.evaluate(() => {
        const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
        return Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).map((candidate) => {
          const host = candidate as HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }
          const renderer = host.shadowRoot?.querySelector('.renderer')
          const canvas = renderer?.querySelector('canvas') as HTMLCanvasElement | null
          const context = canvas?.getContext('2d', { willReadFrequently: true })
          const pixels = context && canvas ? context.getImageData(0, 0, canvas.width, canvas.height).data : undefined
          let dataPixels = 0
          if (pixels) {
            for (let offset = 0; offset < pixels.length; offset += 16) {
              const red = pixels[offset] ?? 0
              const green = pixels[offset + 1] ?? 0
              const blue = pixels[offset + 2] ?? 0
              const alpha = pixels[offset + 3] ?? 0
              if (alpha > 0 && blue > red + 50 && blue > green + 35) dataPixels++
            }
          }
          return {
            visualID: host.envelope?.visualID,
            status: host.envelope?.status,
            dataRevision: host.envelope?.dataRevision,
            rows: host.envelope?.dataState?.datasets?.map((dataset: any) => dataset.rows?.length),
            ariaHidden: renderer?.getAttribute('aria-hidden'),
            canvas: canvas ? { width: canvas.width, height: canvas.height } : null,
            dataPixels,
            alert: host.shadowRoot?.querySelector('[role="alert"]')?.textContent,
          }
        })
      })
      throw new Error(`ECharts first navigation did not paint: ${JSON.stringify(state)}; ${String(error)}`)
    }
    assertNoBlockingConsoleMessages('ECharts first navigation', messages)
  } finally {
    await page.close()
  }
}

async function waitForUpdatesRequest(label: string, updates: string[]): Promise<void> {
  const deadline = Date.now() + 5000
  while (updates.length === 0) {
    if (Date.now() > deadline) throw new Error(`${label}: timed out waiting for /updates request`)
    await new Promise((resolve) => setTimeout(resolve, 25))
  }
}

async function verifyDashboardCommandDoesNotReopenUpdates(): Promise<void> {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)
  const updates: string[] = []
  const commands: string[] = []
  const failedResponses: Array<Promise<string>> = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/updates') updates.push(request.url())
    if (url.pathname.includes('/commands/')) commands.push(`${request.method()} ${url.pathname}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400) {
      failedResponses.push(response.text()
        .catch(() => '<response body unavailable>')
        .then((body) => `${response.status()} ${response.url()}: ${body.trim()} request=${response.request().postData() ?? ''}`))
    }
  })

  try {
    await page.goto(new URL(dashboardPath, baseURL).toString(), { waitUntil: 'domcontentloaded' })
    await page.waitForSelector('lv-dashboard-page')
    await page.waitForTimeout(1000)
    const beforeUpdates = updates.length
    await page.evaluate(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const bindingKey = Object.keys(dashboard?.filterContract?.bindings ?? {})[0]
      if (!bindingKey) throw new Error('dashboard exposes no compiled filter binding')
      const baseRevision = dashboard?.canonicalFilterState?.revision
      if (typeof baseRevision !== 'number') throw new Error('dashboard exposes no canonical filter revision')
      dashboard.dispatchEvent(new CustomEvent('lv-filter-command', {
        bubbles: true,
        composed: true,
        detail: {
          kind: 'mutate',
          operation: 'clear',
          bindingKey,
          baseRevision,
          clientMutationID: 'route-qa-filter-command',
        },
      }))
    })
    await page.waitForTimeout(1000)

    if (beforeUpdates !== 1) throw new Error(`dashboard command: initial /updates count=${beforeUpdates}, want 1`)
    if (updates.length !== 1) throw new Error(`dashboard command reopened /updates: count=${updates.length}`)
    if (!commands.includes('POST /dashboards/dashboard:visual-showcase/commands/filter')) {
      throw new Error(`dashboard command requests=${JSON.stringify(commands)}, want filter POST`)
    }
    if (failedResponses.length > 0) {
      throw new Error(`dashboard command: failed responses=${JSON.stringify(await Promise.all(failedResponses))}`)
    }
    assertNoBlockingConsoleMessages('dashboard command', messages)
  } finally {
    await page.close()
  }
}

async function verifyFilterShowcase(): Promise<void> {
  const path = '/dashboards/dashboard:visual-showcase/pages/filters'
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } })
  const messages = collectBlockingConsoleMessages(page)

  try {
    const response = await page.goto(new URL(path, baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`${path}: status ${response?.status() ?? 'unknown'}`)
    await page.waitForSelector('lv-dashboard-page')
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
      const slicers = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-slicer') ?? []) as any[]
      return (dashboard as any)?.status?.loading === false
        && slicers.length === 8
        && slicers.every((slicer) => slicer.shadowRoot?.querySelector('lv-filter-leaf')?.shadowRoot?.querySelector('fieldset'))
        && slicers
          .filter((slicer) => slicer.definition?.id === 'order_status')
          .every((slicer) => (slicer.options?.items?.length ?? 0) > 0)
    }, undefined, { timeout: 30_000 })

    const matrix = await page.evaluate(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
      return Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-slicer') ?? []).map((candidate) => {
        const slicer = candidate as any
        return {
          binding: slicer.binding?.id,
          definition: slicer.definition?.id,
          valueKind: slicer.definition?.valueKind,
          style: slicer.presentation?.style,
        }
      })
    })
    const expectedMatrix = [
      'purchase_date:date:date_range',
      'purchase_time:timestamp:relative_period',
      'state:string:dropdown',
      'category_text:string:input',
      'order_status:string:list',
      'delivered:boolean:buttons',
      'delivery_days:integer:numeric_range',
      'revenue_amount:decimal:numeric_range',
    ]
    const actualMatrix = matrix
      .map((item) => `${item.definition}:${item.valueKind}:${item.style}`)
      .sort()
    if (JSON.stringify(actualMatrix) !== JSON.stringify([...expectedMatrix].sort())) {
      throw new Error(`${path}: filter matrix=${JSON.stringify(matrix)}, want ${JSON.stringify(expectedMatrix)}`)
    }

    const mutationCases: Array<{
      label: string
      bindingID: string
      mutate: () => Promise<void>
      expressionKind: string
    }> = [
      {
        label: 'state dropdown',
        bindingID: 'state',
        mutate: async () => {
          const dropdown = page.getByRole('combobox', { name: 'Filter by customer state' })
          await dropdown.focus()
          await page.waitForFunction(() => {
            const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
            const slicer = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-slicer') ?? [])
              .find((candidate: any) => candidate.binding?.id === 'state') as any
            return (slicer?.options?.items?.length ?? 0) > 0
          }, undefined, { timeout: 30_000 })
          await dropdown.selectOption({ index: 1 })
        },
        expressionKind: 'set',
      },
      {
        label: 'status list',
        bindingID: 'status_list',
        mutate: async () => {
          await page.getByRole('checkbox').first().check()
        },
        expressionKind: 'set',
      },
      {
        label: 'category input',
        bindingID: 'category_input',
        mutate: async () => {
          const input = page.getByRole('textbox', { name: 'Filter by category text' })
          await input.fill('bed')
          await input.press('Tab')
        },
        expressionKind: 'comparison',
      },
      {
        label: 'boolean buttons',
        bindingID: 'delivered_buttons',
        mutate: async () => {
          await page.getByRole('button', { name: 'Delivered', exact: true }).click()
        },
        expressionKind: 'set',
      },
      {
        label: 'date range',
        bindingID: 'purchase_date',
        mutate: async () => {
          const control = page.getByRole('region', { name: 'Filter by purchase date range' })
          const from = control.getByLabel('Start date')
          const to = control.getByLabel('End date')
          await from.fill('2017-01-01')
          await to.fill('2018-12-31')
          // Native date inputs expose platform-specific internal Tab stops.
          // Enter is the filter control's deterministic compound-commit boundary.
          await to.press('Enter')
        },
        expressionKind: 'range',
      },
      {
        label: 'integer range',
        bindingID: 'delivery_days_range',
        mutate: async () => {
          const control = page.getByRole('region', { name: 'Filter by delivery days' })
          const minimum = control.getByLabel('Minimum')
          const maximum = control.getByLabel('Maximum')
          await minimum.fill('0')
          await minimum.press('Tab')
          await maximum.fill('60')
          await maximum.press('Tab')
        },
        expressionKind: 'range',
      },
      {
        label: 'decimal range',
        bindingID: 'revenue_range',
        mutate: async () => {
          const control = page.getByRole('region', { name: 'Filter by order revenue' })
          const minimum = control.getByLabel('Minimum')
          const maximum = control.getByLabel('Maximum')
          await minimum.fill('1.25')
          await minimum.press('Tab')
          await maximum.fill('1000.50')
          await maximum.press('Tab')
        },
        expressionKind: 'range',
      },
      {
        label: 'relative period',
        bindingID: 'purchase_time_relative',
        mutate: async () => {
          const control = page.getByRole('region', { name: 'Filter by relative purchase period' })
          await control.getByLabel('Period unit').selectOption('year')
          const count = control.getByLabel('Period count')
          await count.fill('10')
          await count.press('Tab')
        },
        expressionKind: 'relative_period',
      },
    ]

    for (const mutation of mutationCases) {
      console.log(`${path}: testing ${mutation.label}`)
      const previousRevision = await currentFilterRevision(page)
      await mutation.mutate()
      await page.waitForFunction(({ revision, bindingID, expressionKind }) => {
        const dashboard = document.querySelector('lv-dashboard-page') as any
        const state = dashboard?.canonicalFilterState
        const bindings = Object.values(dashboard?.filterContract?.bindings ?? {}) as any[]
        const binding = bindings.find((candidate) =>
          candidate.id === bindingID
          && (candidate.scope === 'report' || candidate.pageID === 'filters'))
        const expression = binding ? state?.appliedControls?.[binding.key]?.expression : undefined
        return state?.revision > revision
          && expression?.kind === expressionKind
          && dashboard?.filterController?.pending === false
      }, {
        revision: previousRevision,
        bindingID: mutation.bindingID,
        expressionKind: mutation.expressionKind,
      }, { timeout: 30_000 })
      const synchronized = await page.evaluate((bindingID) => {
        const dashboard = document.querySelector('lv-dashboard-page') as any
        const slicer = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-slicer') ?? [])
          .find((candidate: any) => candidate.binding?.id === bindingID) as any
        const dock = dashboard?.shadowRoot?.querySelector('lv-filter-dock') as any
        const pane = Array.from(dock?.shadowRoot?.querySelectorAll('lv-filter-pane-card') ?? [])
          .find((candidate: any) => candidate.binding?.key === slicer?.binding?.key) as any
        return pane && JSON.stringify(pane.expression) === JSON.stringify(slicer?.expression)
      }, mutation.bindingID)
      if (!synchronized) throw new Error(`${path}: ${mutation.label} did not synchronize pane and slicer state`)
    }

    const finalRevision = await currentFilterRevision(page)
    if (finalRevision < mutationCases.length) {
      throw new Error(`${path}: final filter revision=${finalRevision}, want at least ${mutationCases.length}`)
    }
    const canonicalURLParams = await page.evaluate(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      return dashboard?.signal('urlParams', {}) as Record<string, string | string[]>
    })
    const actualURLParams = new URL(page.url()).searchParams
    for (const [key, value] of Object.entries(canonicalURLParams)) {
      const expected = Array.isArray(value) ? value : [value]
      const actual = actualURLParams.getAll(key)
      if (JSON.stringify(actual) !== JSON.stringify(expected)) {
        throw new Error(`${path}: URL parameter ${key}=${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`)
      }
    }
    if ([...actualURLParams.keys()].some((key) => !(key in canonicalURLParams))) {
      throw new Error(`${path}: URL contains parameters outside canonical state: ${page.url()}`)
    }
    assertNoBlockingConsoleMessages(path, messages)
  } finally {
    await page.close()
  }
}

async function currentFilterRevision(page: Page): Promise<number> {
  return page.evaluate(() => {
    const dashboard = document.querySelector('lv-dashboard-page') as any
    return Number(dashboard?.canonicalFilterState?.revision ?? 0)
  })
}

type SpatialTileSnapshot = {
  status: string
  message: string
  dataRevision: number
  cardinality: number
  featureCap: number
  maximumTileBytes: number
  tileURL: string
  zoomControlWidth: number
  zoomControlHeight: number
}

async function verifySpatialShowcaseMaps(): Promise<void> {
  const path = '/dashboards/dashboard:visual-showcase/pages/chart-map'
  const visualIDs = ['customer_point_map', 'customer_revenue_heat_map', 'customer_density_map']
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } })
  const messages = collectBlockingConsoleMessages(page)
  const pageErrors: string[] = []
  const tileZooms = new Map<string, number[]>()
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.on('response', (response) => {
    const url = new URL(response.url())
    const match = /\/visuals\/([^/]+)\/tiles\/[^/]+\/(\d+)\/\d+\/\d+\.mvt$/.exec(url.pathname)
    if (!match || !visualIDs.includes(match[1]!) || response.status() !== 200) return
    const zooms = tileZooms.get(match[1]!) ?? []
    zooms.push(Number(match[2]))
    tileZooms.set(match[1]!, zooms)
  })

  try {
    const response = await page.goto(new URL(path, baseURL).toString(), { waitUntil: 'domcontentloaded', timeout: 120_000 })
    if (!response?.ok()) throw new Error(`${path}: status ${response?.status() ?? 'unknown'}`)
    await page.waitForSelector('lv-dashboard-page')
    await page.waitForFunction((expectedVisualIDs) => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }>
      return expectedVisualIDs.every((visualID) => {
        const host = hosts.find((candidate) => candidate.envelope?.visualID === visualID)
        const envelope = host?.envelope
        const text = host?.shadowRoot?.textContent ?? ''
        return envelope?.dataState?.kind === 'spatial_tiled'
          && envelope?.status?.kind === 'ready'
          && !envelope?.status?.message
          && Boolean(host?.shadowRoot?.querySelector('canvas'))
          && !text.includes('Cannot read properties')
          && !text.includes('Map data could not be loaded')
      })
    }, visualIDs, { timeout: 120_000 })
    await expect.poll(() => visualIDs.every((visualID) => (tileZooms.get(visualID)?.some((zoom) => zoom > 0) ?? false)), {
      timeout: 120_000,
      message: `${path}: tiled showcase maps did not fit their governed extent and request visible non-world tiles`,
    }).toBe(true)
    for (let step = 0; step < 6; step++) {
      await page.evaluate((visualID) => {
        const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
        const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }>
        const host = hosts.find((candidate) => candidate.envelope?.visualID === visualID)
        const zoomIn = host?.shadowRoot?.querySelector('button.maplibregl-ctrl-zoom-in') as HTMLButtonElement | null
        zoomIn?.click()
      }, visualIDs[0])
      await page.waitForTimeout(500)
      const summary = await page.evaluate((visualID) => {
        const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
        const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }>
        return hosts.find((candidate) => candidate.envelope?.visualID === visualID)?.shadowRoot?.querySelector('[data-map-data-table] > summary')?.textContent ?? ''
      }, visualIDs[0])
      if (hasMixedSpatialPrecision(summary)) {
        throw new Error(`${path}: point map mixed raw and aggregate granularity after zoom step ${step + 1}: ${summary.trim()}`)
      }
    }
    if (pageErrors.length > 0) throw new Error(`${path}: page errors:\n${pageErrors.join('\n')}`)
    assertNoBlockingConsoleMessages(path, messages)
  } finally {
    await page.close()
  }
}

async function verifySpatialMapWindowing(): Promise<void> {
  const path = '/dashboards/dashboard:visual-showcase/pages/chart-map-scale'
  const origin = new URL(baseURL).origin
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const messages = collectBlockingConsoleMessages(page)
  const updates: string[] = []
  const tiles: Array<{ load: number; url: string; status: number; bytes: number; features: number; precision: string; cache: string }> = []
  const basemapResponses: Array<{ url: string; status: number; cacheControl: string; acceptRanges: string }> = []
  let load = 0
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/updates') updates.push(request.url())
  })
  page.on('response', async (response) => {
    const url = new URL(response.url())
    if (url.pathname.endsWith('.mvt') && url.pathname.includes('/visuals/')) {
      const headers = response.headers()
      let bytes = Number(headers['content-length'] ?? 0)
      if (!bytes) {
        try { bytes = (await response.body()).byteLength } catch { bytes = 0 }
      }
      tiles.push({ load, url: response.url(), status: response.status(), bytes, features: Number(headers['x-leapview-tile-features'] ?? 0), precision: headers['x-leapview-tile-precision'] ?? '', cache: headers['x-leapview-tile-cache'] ?? '' })
    }
    if (!url.pathname.endsWith('.pmtiles')) return
    const headers = response.headers()
    basemapResponses.push({ url: response.url(), status: response.status(), cacheControl: headers['cache-control'] ?? '', acceptRanges: headers['accept-ranges'] ?? '' })
  })

  try {
    const readiness: number[] = []
    const initialBytes: number[] = []
    let initial: SpatialTileSnapshot | undefined
    for (load = 1; load <= 10; load++) {
      const tileStart = tiles.length
      const readyStarted = performance.now()
      const response = await page.goto(new URL(`${path}?qaLoad=${load}`, baseURL).toString(), { waitUntil: 'domcontentloaded', timeout: 120_000 })
      if (!response?.ok()) throw new Error(`${path}: status ${response?.status() ?? 'unknown'}`)
      await page.waitForSelector('lv-dashboard-page')
      await waitForUpdatesRequest(path, updates)
      await page.waitForFunction(() => {
        const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
        const host = dashboard?.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any }
        const envelope = host?.envelope
        return envelope?.dataState?.kind === 'spatial_tiled'
          && envelope.dataState.cardinality?.kind === 'exact'
          && envelope.dataState.cardinality.count > 0
          && envelope.status?.kind !== 'loading'
          && !envelope.status?.message
      }, undefined, { timeout: 120_000 })
      await expect.poll(() => tiles.length, { timeout: 120_000, message: `${path}: no MVT response observed` }).toBeGreaterThan(tileStart)
      await page.waitForTimeout(250)
      readiness.push(performance.now() - readyStarted)
      initialBytes.push(tiles.slice(tileStart).reduce((sum, tile) => sum + tile.bytes, 0))
      initial = await spatialTileSnapshot(page)
      assertSpatialTiles(path, initial)
    }
    const readyP95 = percentile(readiness, 0.95)
    if (readyP95 > 10_000) throw new Error(`${path}: initial map readiness p95 ${Math.round(readyP95)}ms exceeds 10000ms`)
    if (Math.max(...initialBytes) >= 768 * 1024) throw new Error(`${path}: initial tile transfer ${Math.max(...initialBytes)} bytes does not reduce the 3MiB baseline by 75%`)
    if (!initial) throw new Error(`${path}: no tiled map state observed`)
    if (initial.cardinality !== 1_000_000 || initial.featureCap !== 5_000 || initial.maximumTileBytes !== 512 * 1024) {
      throw new Error(`${path}: budgets/cardinality ${JSON.stringify(initial)}; want 1000000 coordinates, 5000 features, 512KiB`)
    }
    if (initial.zoomControlWidth !== 30 || initial.zoomControlHeight !== 30) {
      throw new Error(`${path}: zoom control is ${initial.zoomControlWidth}x${initial.zoomControlHeight} CSS pixels; want 30x30`)
    }

    const zoomIn = page.locator('lv-dashboard-page').locator('lv-visualization-host').locator('button.maplibregl-ctrl-zoom-in')
    const zoomOut = page.locator('lv-dashboard-page').locator('lv-visualization-host').locator('button.maplibregl-ctrl-zoom-out')
    for (let cycle = 0; cycle < 30; cycle++) {
      await (cycle % 2 === 0 ? zoomIn : zoomOut).click()
      await page.waitForTimeout(250)
    }
    const beforeReturn = tiles.length
    const reset = page.locator('lv-dashboard-page').locator('lv-visualization-host').locator('button[aria-label="Reset map view"]')
    await reset.click()
    await page.waitForTimeout(500)
    const returnedTiles = tiles.slice(beforeReturn)
    if (returnedTiles.some((tile) => tile.cache === 'miss')) {
      throw new Error(`${path}: returning to the initial area caused new DuckDB work: ${JSON.stringify(returnedTiles)}`)
    }
    if (tiles.length === 0 || tiles.some((tile) => tile.status !== 200 || tile.features > 5_000 || tile.bytes > 512 * 1024)) {
      throw new Error(`${path}: invalid tile delivery ${JSON.stringify(tiles)}`)
    }
    if (basemapResponses.length === 0) throw new Error(`${path}: no PMTiles byte-range response observed`)
    for (const basemap of basemapResponses) {
      if (new URL(basemap.url).origin !== origin || basemap.status !== 206 || basemap.acceptRanges !== 'bytes' || !basemap.cacheControl.includes('immutable')) {
        throw new Error(`${path}: invalid basemap delivery ${JSON.stringify(basemap)}`)
      }
    }
    assertNoBlockingConsoleMessages(path, messages)
    console.log(`${path}: coldLoads=10 readyP95=${Math.round(readyP95)}ms maxInitialBytes=${Math.max(...initialBytes)} tiles=${tiles.length}`)
  } finally {
    await page.close()
  }
}

async function spatialTileSnapshot(page: Page): Promise<SpatialTileSnapshot> {
  return page.evaluate(() => {
    const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & { shadowRoot: ShadowRoot }
    const host = dashboard?.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any; shadowRoot: ShadowRoot }
    const envelope = host?.envelope
    const state = envelope?.dataState
    const zoom = host?.shadowRoot?.querySelector('button.maplibregl-ctrl-zoom-in') as HTMLButtonElement | null
    const style = zoom ? getComputedStyle(zoom) : undefined
    return {
      status: String(envelope?.status?.kind ?? ''), message: String(envelope?.status?.message ?? ''), dataRevision: Number(envelope?.dataRevision ?? 0),
      cardinality: Number(state?.cardinality?.count ?? 0), featureCap: Number(state?.featureCap ?? 0), maximumTileBytes: Number(state?.maximumTileBytes ?? 0), tileURL: String(state?.tileURL ?? ''),
      zoomControlWidth: Number.parseFloat(style?.width ?? '0'), zoomControlHeight: Number.parseFloat(style?.height ?? '0'),
    }
  })
}

function assertSpatialTiles(path: string, snapshot: SpatialTileSnapshot): void {
  if (!['partial', 'ready'].includes(snapshot.status) || snapshot.message || snapshot.dataRevision < 1 || !snapshot.tileURL.includes('/tiles/')) {
    throw new Error(`${path}: incomplete spatial tile state ${JSON.stringify(snapshot)}`)
  }
}

function percentile(values: number[], quantile: number): number {
  if (values.length === 0) return 0
  const ordered = [...values].sort((left, right) => left - right)
  return ordered[Math.max(0, Math.ceil(ordered.length * quantile) - 1)]!
}

function collectBlockingConsoleMessages(page: Page): string[] {
  const messages: string[] = []
  page.on('console', (message) => {
    if (message.type() !== 'warning' && message.type() !== 'error') return
    const text = message.text()
    if (text.includes('Failed to load resource')) {
      const source = message.location().url
      messages.push(source ? `${text} (${source})` : text)
    }
    if (text.includes('[LeapView]')) messages.push(text)
    if (text.includes('Multiple versions of Lit loaded')) messages.push(text)
    if (text.includes('Lit is in dev mode')) messages.push(text)
  })
  return messages
}

function assertNoBlockingConsoleMessages(label: string, messages: string[]): void {
  if (messages.length === 0) return
  throw new Error(`${label}: blocking console messages:\n${messages.join('\n')}`)
}
