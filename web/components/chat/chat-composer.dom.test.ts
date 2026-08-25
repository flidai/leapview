import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/chat-composer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('composer renders a compact centered prompt surface', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    await page.locator('lv-chat-composer').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-chat-composer').evaluate((element: any) => {
      const root = element.shadowRoot
      const form = root.querySelector('form') as HTMLElement
      const surface = root.querySelector('.composer-surface') as HTMLElement
      const textarea = root.querySelector('textarea') as HTMLTextAreaElement
      const actions = root.querySelector('.actions') as HTMLElement
      const button = root.querySelector('button') as HTMLButtonElement
      const formRect = form.getBoundingClientRect()
      const surfaceRect = surface.getBoundingClientRect()
      const surfaceStyle = getComputedStyle(surface)
      const textareaStyle = getComputedStyle(textarea)
      return {
        formWidth: Math.round(formRect.width),
        formLeft: Math.round(formRect.left),
        surfaceWidth: Math.round(surfaceRect.width),
        surfaceLeft: Math.round(surfaceRect.left),
        surfaceDisplay: surfaceStyle.display,
        surfaceRadius: surfaceStyle.borderRadius,
        surfaceShadow: surfaceStyle.boxShadow,
        textareaLabel: textarea.getAttribute('aria-label'),
        textareaPlaceholder: textarea.getAttribute('placeholder'),
        textareaResize: textareaStyle.resize,
        textareaMinHeight: Math.round(parseFloat(textareaStyle.minHeight)),
        textareaMaxHeight: Math.round(parseFloat(textareaStyle.maxHeight)),
        actionsJustify: getComputedStyle(actions).justifyContent,
        buttonWidth: Math.round(button.getBoundingClientRect().width),
        buttonHeight: Math.round(button.getBoundingClientRect().height),
        buttonDisabled: button.disabled,
      }
    })

    expect(state).toMatchObject({
      formWidth: 784,
      formLeft: 248,
      surfaceWidth: 760,
      surfaceLeft: 260,
      surfaceDisplay: 'grid',
      surfaceRadius: '12px',
      surfaceShadow: 'none',
      textareaLabel: 'Ask about dashboards, metrics, or models',
      textareaPlaceholder: 'Ask about dashboards, metrics, or models...',
      textareaResize: 'none',
      textareaMinHeight: 38,
      textareaMaxHeight: 160,
      actionsJustify: 'flex-end',
      buttonWidth: 32,
      buttonHeight: 32,
      buttonDisabled: true,
    })
  } finally {
    await page.close()
  }
})

test('composer preserves submit, multiline, disabled, and pending behavior', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    await page.locator('lv-chat-composer').evaluate((element: any) => element.updateComplete)

    const events = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const root = element.shadowRoot
      const textarea = root.querySelector('textarea') as HTMLTextAreaElement
      const button = root.querySelector('button') as HTMLButtonElement
      const received: string[] = []
      element.addEventListener('lv-chat-submit', (event: CustomEvent) => received.push(event.detail.input))

      textarea.value = '  Revenue trend  '
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, inputType: 'insertText', data: 'Revenue trend' }))
      await element.updateComplete
      const enabledAfterInput = !button.disabled
      const singleLineHeight = Math.round(textarea.getBoundingClientRect().height)

      textarea.value = ['one', 'two', 'three', 'four', 'five', 'six'].join('\n')
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, inputType: 'insertLineBreak', data: '\n' }))
      await element.updateComplete
      const multilineHeight = Math.round(textarea.getBoundingClientRect().height)
      const multilineOverflowY = getComputedStyle(textarea).overflowY

      textarea.value = '  Revenue trend  '
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, inputType: 'insertText', data: 'Revenue trend' }))
      await element.updateComplete

      textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, composed: true }))
      const afterShiftEnter = received.length

      textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, composed: true }))
      const afterEnter = received.length

      element.pending = true
      await element.updateComplete
      const pendingDisabled = button.disabled
      const hasSpinner = Boolean(root.querySelector('lv-loading-spinner'))

      element.pending = false
      element.disabled = true
      await element.updateComplete
      const textareaDisabled = textarea.disabled
      const disabledButton = button.disabled

      return { received, enabledAfterInput, singleLineHeight, multilineHeight, multilineOverflowY, afterShiftEnter, afterEnter, pendingDisabled, hasSpinner, textareaDisabled, disabledButton }
    })

    expect(events.received).toEqual(['Revenue trend'])
    expect(events.enabledAfterInput).toBe(true)
    expect(events.singleLineHeight).toBe(38)
    expect(events.multilineHeight).toBeGreaterThan(events.singleLineHeight)
    expect(events.multilineHeight).toBeLessThanOrEqual(160)
    expect(events.multilineOverflowY).toBe('hidden')
    expect(events.afterShiftEnter).toBe(0)
    expect(events.afterEnter).toBe(1)
    expect(events.pendingDisabled).toBe(true)
    expect(events.hasSpinner).toBe(true)
    expect(events.textareaDisabled).toBe(true)
    expect(events.disabledButton).toBe(true)
  } finally {
    await page.close()
  }
})

test('composer searches for and attaches typed @ references with spaces', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const reference = {
        reference: { kind: 'visual', id: 'executive-sales.orders_chart' },
        name: 'Orders',
		visualType: 'table',
        description: 'Overview · Executive Sales',
        href: '/dashboards/executive-sales/pages/overview',
        locations: [],
        context: [],
		hierarchy: ['Sales', 'Executive Sales', 'Overview'],
      }
      element.suggestions = [reference]
      await element.updateComplete
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      const searches: string[] = []
      element.addEventListener('lv-chat-reference-search', (event: CustomEvent) => searches.push(event.detail.query))
      textarea.value = 'Compare @orders by'
      textarea.setSelectionRange(textarea.value.length, textarea.value.length)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const option = element.shadowRoot.querySelector('.mention-option') as HTMLButtonElement
      const optionText = option?.textContent?.replace(/\s+/g, ' ').trim()
      option.click()
      await element.updateComplete
      const draftAfterReference = textarea.value
      textarea.value = 'Compare this with last month'
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true }))
      const submitted = await new Promise<any>((resolve) => {
        element.addEventListener('lv-chat-submit', (event: CustomEvent) => resolve(event.detail), { once: true })
        textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
      })
		const iconClass = option.querySelector('.mention-icon svg')?.getAttribute('class')
		return { searches, optionText, iconClass, draftAfterReference, submitted }
    })

    expect(result).toEqual({
      searches: ['orders by'],
      optionText: 'Orders Sales › Executive Sales › Overview Visual',
		iconClass: 'reference-icon-table',
      draftAfterReference: 'Compare',
      submitted: {
        input: 'Compare this with last month',
        references: [{
          reference: { kind: 'visual', id: 'executive-sales.orders_chart' },
          name: 'Orders',
		  visualType: 'table',
          description: 'Overview · Executive Sales',
          href: '/dashboards/executive-sales/pages/overview',
          locations: [],
          context: [],
		  hierarchy: ['Sales', 'Executive Sales', 'Overview'],
        }],
      },
    })
  } finally {
    await page.close()
  }
})

test('composer consumes attachments only after a user turn is accepted', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const reference = {
        reference: { kind: 'visual', id: 'executive-sales.revenue' },
        name: 'Revenue by month',
        hierarchy: ['Sales', 'Executive Sales', 'Overview'],
        href: '/dashboards/executive-sales/pages/overview',
        locations: [],
        context: ['current_page'],
      }
      element.acceptedRunId = 'run_previous'
      await element.updateComplete
	  element.references = [reference]
	  await element.updateComplete
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = 'Why did revenue fall?'
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true }))
      const changes: any[] = []
      element.addEventListener('lv-chat-references-change', (event: CustomEvent) => changes.push(event.detail.references))
      textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
      await element.updateComplete
      const afterSubmit = { draft: textarea.value, references: element.references.length, changes: changes.length }

      // A rejected request returns no newly persisted user run.
      element.value = ''
      await element.updateComplete
      const afterRejected = { draft: textarea.value, references: element.references.length, changes: changes.length }

      // The persisted user message identifies the accepted turn before model completion.
      element.acceptedRunId = 'run_new'
      await element.updateComplete
      return {
        afterSubmit,
        afterRejected,
        afterAccepted: { draft: textarea.value, references: element.references.length, changes },
      }
    })

    expect(result.afterSubmit).toEqual({ draft: 'Why did revenue fall?', references: 1, changes: 0 })
    expect(result.afterRejected).toEqual({ draft: 'Why did revenue fall?', references: 1, changes: 0 })
    expect(result.afterAccepted).toEqual({ draft: '', references: 0, changes: [[]] })
  } finally {
    await page.close()
  }
})

test('composer distinguishes matching reference IDs from different kinds', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const attached = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
		const references = ['field', 'metric'].map((kind) => ({
		reference: { kind, id: 'orders.revenue' },
		name: `Revenue ${kind}`,
		href: kind === 'metric' ? `/explore?metric=${kind}` : `/explore?field=${kind}`,
		locations: [],
		context: [],
      }))
      element.suggestions = references
      await element.updateComplete
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement

      for (const reference of references) {
        textarea.value = '@revenue'
        textarea.setSelectionRange(textarea.value.length, textarea.value.length)
        textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
        await element.updateComplete
        const option = Array.from(element.shadowRoot.querySelectorAll('.mention-option'))
		  .find((candidate: any) => candidate.textContent.includes(reference.name)) as HTMLButtonElement
        option.click()
        await element.updateComplete
      }

	  return element.references.map((reference: any) => reference.reference.kind)
    })

	expect(attached).toEqual(['field', 'metric'])
  } finally {
    await page.close()
  }
})

test('mention picker opens immediately, renders compact rows, and scrolls with keyboard navigation', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const root = element.shadowRoot
      const textarea = root.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = '@'
      textarea.setSelectionRange(1, 1)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await element.updateComplete

      const immediatePicker = root.querySelector('.mention-picker') as HTMLElement
      const immediate = {
        visible: Boolean(immediatePicker),
        busy: immediatePicker?.getAttribute('aria-busy'),
        status: root.querySelector('.mention-status')?.textContent?.trim(),
      }

      element.suggestions = Array.from({ length: 8 }, (_, index) => ({
		reference: { kind: index % 2 === 0 ? 'visual' : 'metric', id: `result-${index}` },
		name: `Result ${index + 1}`,
        description: `Compact description ${index + 1}`,
		href: `/explore?result=${index}`, locations: [], context: [],
      }))
      await element.updateComplete
      await element.updateComplete

      const picker = root.querySelector('.mention-picker') as HTMLElement
      const firstOption = root.querySelector('.mention-option') as HTMLElement
      const copy = root.querySelector('.mention-copy') as HTMLElement
	  const hierarchy = root.querySelector('.mention-hierarchy') as HTMLElement
	  const type = root.querySelector('.mention-type') as HTMLElement
      const initialScrollTop = picker.scrollTop
      for (let index = 0; index < 7; index += 1) {
        textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)))
      }
      const active = root.querySelector('.mention-option[data-active="true"]') as HTMLElement
      const pickerBox = picker.getBoundingClientRect()
      const activeBox = active.getBoundingClientRect()

      return {
        immediate,
        optionHeight: Math.round(firstOption.getBoundingClientRect().height),
        copyDisplay: getComputedStyle(copy).display,
		hierarchyText: hierarchy.textContent?.trim(),
		typeText: type.textContent?.trim(),
		descriptionVisible: Boolean(root.querySelector('.mention-description')),
        scrolled: picker.scrollTop > initialScrollTop,
        activeText: active.textContent?.replace(/\s+/g, ' ').trim(),
        activeVisible: activeBox.top >= pickerBox.top && activeBox.bottom <= pickerBox.bottom,
      }
    })

    expect(result.immediate).toEqual({ visible: true, busy: 'true', status: 'Searching…' })
    expect(result.optionHeight).toBeLessThanOrEqual(32)
    expect(result.copyDisplay).toBe('grid')
	expect(result.hierarchyText).toBe('')
	expect(result.typeText).toBe('Visual')
	expect(result.descriptionVisible).toBe(false)
    expect(result.scrolled).toBe(true)
    expect(result.activeText).toContain('Result 8')
    expect(result.activeVisible).toBe(true)
  } finally {
    await page.close()
  }
})

test('mention picker ignores search responses from an older request', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      const requests: Array<{ query: string; requestId: number }> = []
      element.addEventListener('lv-chat-reference-search', (event: CustomEvent) => requests.push(event.detail))

      const search = async (value: string) => {
        textarea.value = value
        textarea.setSelectionRange(value.length, value.length)
        textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
        await element.updateComplete
      }

      await search('@orders')
      const first = requests[0]
      element.suggestionQuery = first.query
      element.suggestionRequestId = first.requestId
	  element.suggestions = [{ reference: { kind: 'visual', id: 'orders' }, name: 'Orders', href: '/dashboards/orders', locations: [], context: [] }]
      await element.updateComplete
      const optionText = () => element.shadowRoot.querySelector('.mention-option')?.textContent?.replace(/\s+/g, ' ').trim()
      const firstVisible = optionText()

      await search('@revenue')
      const second = requests[1]
      element.suggestionQuery = first.query
      element.suggestionRequestId = first.requestId
	  element.suggestions = [{ reference: { kind: 'visual', id: 'orders-old' }, name: 'Old orders response', href: '/dashboards/orders-old', locations: [], context: [] }]
      await element.updateComplete
      const staleVisible = element.shadowRoot.querySelector('.mention-option')?.textContent?.trim() ?? ''
      const staleStatus = element.shadowRoot.querySelector('.mention-status')?.textContent?.replace(/\s+/g, ' ').trim()

      element.suggestionQuery = second.query
      element.suggestionRequestId = second.requestId
	 element.suggestions = [{ reference: { kind: 'metric', id: 'revenue' }, name: 'Revenue', href: '/explore?metric=revenue', locations: [], context: [] }]
      await element.updateComplete
      const currentVisible = optionText()

      element.suggestionQuery = first.query
      element.suggestionRequestId = first.requestId
	  element.suggestions = [{ reference: { kind: 'visual', id: 'orders-late' }, name: 'Late orders response', href: '/dashboards/orders-late', locations: [], context: [] }]
      await element.updateComplete
      const afterLateStale = optionText()

      return { requests, firstVisible, staleVisible, staleStatus, currentVisible, afterLateStale }
    })

    expect(result.requests).toEqual([
      { query: 'orders', requestId: 1 },
      { query: 'revenue', requestId: 2 },
    ])
	expect(result.firstVisible).toBe('Orders Visual')
    expect(result.staleVisible).toBe('')
    expect(result.staleStatus).toBe('Searching…')
	expect(result.currentVisible).toBe('Revenue Metric')
	expect(result.afterLateStale).toBe('Revenue Metric')
  } finally {
    await page.close()
  }
})

test('mention picker pins on-page results above deduplicated accessible results', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      const onPage = {
		reference: { kind: 'visual', id: 'orders-on-page' },
		name: 'Orders on this page', description: 'Overview',
		hierarchy: ['Sales', 'Executive Sales', 'Overview'],
		href: '/overview', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/overview' }], context: ['current_page'],
      }
      element.pinnedSuggestions = [onPage]
      element.suggestions = [
        onPage,
		{ reference: { kind: 'metric', id: 'orders-metric' }, name: 'Orders metric', description: 'Sales model', hierarchy: ['Sales model'], href: '/explore?metric=orders', locations: [], context: [] },
      ]
      await element.updateComplete
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = '@orders'
      textarea.setSelectionRange(textarea.value.length, textarea.value.length)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await element.updateComplete

      return {
        labels: Array.from(element.shadowRoot.querySelectorAll('.mention-section-label')).map((node: any) => node.textContent.trim()),
        options: Array.from(element.shadowRoot.querySelectorAll('.mention-option')).map((node: any) => node.textContent.replace(/\s+/g, ' ').trim()),
      }
    })

    expect(result.labels).toEqual(['On this page', 'All accessible'])
	expect(result.options).toEqual(['Orders on this page Sales › Executive Sales › Overview Visual', 'Orders metric Sales model Metric'])
  } finally {
    await page.close()
  }
})

test('composer enforces the server-provided reference limit', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-composer'))
    const result = await page.locator('lv-chat-composer').evaluate(async (element: any) => {
      element.referenceLimit = 2
      const searches: string[] = []
      element.addEventListener('lv-chat-reference-search', (event: CustomEvent) => searches.push(event.detail.query))
      const textarea = element.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      for (const id of ['one', 'two', 'three']) {
	 element.suggestions = [{ reference: { kind: 'metric', id }, name: id, href: `/explore?metric=${id}`, locations: [], context: [] }]
        textarea.value = `@${id}`
        textarea.setSelectionRange(textarea.value.length, textarea.value.length)
        textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
        await element.updateComplete
        element.shadowRoot.querySelector('.mention-option')?.click()
        await element.updateComplete
      }
      textarea.value = '@three'
      textarea.setSelectionRange(textarea.value.length, textarea.value.length)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const limited = {
		references: element.references.map((reference: any) => reference.reference.id),
        status: element.shadowRoot.querySelector('.mention-status')?.textContent?.replace(/\s+/g, ' ').trim(),
        optionCount: element.shadowRoot.querySelectorAll('.mention-option').length,
      }
      element.shadowRoot.querySelector('.reference-chip')?.click()
      await element.updateComplete
      return {
        limited,
        searches,
      }
    })
    expect(result).toEqual({
      limited: {
        references: ['one', 'two'],
        status: 'Up to 2 items can be attached',
        optionCount: 0,
      },
      searches: ['one', 'two', 'three'],
    })
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body {
            ${typographyTestTokens}
            --lv-bg-app: #f6f8fa;
            --lv-bg-panel: #fff;
            --lv-bg-control: #f6f8fa;
            --lv-bg-hover: #eff2f5;
            --lv-bg-accent-muted: #ddf4ff;
            --lv-fg-default: #24292f;
            --lv-fg-muted: #57606a;
            --lv-accent: #0969da;
            --lv-accent-fg: #fff;
            --lv-line-default: #d0d7de;
            --lv-line-muted: #d8dee4;
            --lv-line-accent: #0969da;
            --lv-line-accent-muted: #54aeff;
            --lv-border-default: 1px solid #d0d7de;
            --lv-border-muted: 1px solid #d8dee4;
            --lv-border-width-focus: 2px;
            --lv-radius-default: 6px;
            --lv-radius-large: 12px;
            --lv-space-2xs: 2px;
            --lv-space-xs: 4px;
            --lv-space-sm: 6px;
            --lv-space-md: 8px;
            --lv-space-lg: 12px;
            --lv-space-xl: 16px;
            --lv-control-medium: 32px;
            --lv-control-small: 28px;
            --lv-chat-stack-width: 760px;



            --lv-transition-fast: 160ms ease;
            --lv-shadow-floating-sm: 0 8px 24px rgb(0 0 0 / .12);
            --lv-spinner-size-md: 16px;
            --lv-spinner-duration: 1800ms;
            --duration-fast: 160ms;
            --ease-lv: ease;
          }
        </style>
      </head>
      <body>
        <lv-chat-composer placeholder="Ask about dashboards, metrics, or models..."></lv-chat-composer>
        <script type="module" src="/chat-composer-under-test.js"></script>
      </body>
    </html>
  `
}
