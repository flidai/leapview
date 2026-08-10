import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''

const root = join(process.cwd(), '.tmp/markdown-view-test')

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

test('markdown view renders sanitized markdown with default and compact typography', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-markdown-view'))

    const state = await page.evaluate(async () => {
      const markdown = [
        '# Hello darkness',
        '',
        'A paragraph with **strong**, _emphasis_, ~~strike~~, `inline code`, and https://example.com.',
        '',
        '## Section',
        '',
        '- One',
        '- Two',
        '  - Nested',
        '',
        '> Quoted guidance',
        '',
        '---',
        '',
        '| Name | Value |',
        '| --- | --- |',
        '| Tool | Enabled |',
        '',
        '```json',
        '{"enabled": true}',
        '```',
        '',
        '![Alt text](https://example.com/image.png)',
        '',
        '<script>window.__unsafe = true</script><img src=x onerror="window.__unsafe = true">',
      ].join('\n')
      const standard = document.createElement('lv-markdown-view') as any
      standard.value = markdown
      const compact = document.createElement('lv-markdown-view') as any
      compact.value = markdown
      compact.compact = true
      const empty = document.createElement('lv-markdown-view') as any
      empty.emptyText = 'Nothing here.'
      document.body.append(standard, compact, empty)
      await standard.updateComplete
      await compact.updateComplete
      await empty.updateComplete

      const standardRoot = standard.shadowRoot
      const compactRoot = compact.shadowRoot
      const h1 = compactRoot.querySelector('h1')!
      const h2 = compactRoot.querySelector('h2')!
      const paragraph = compactRoot.querySelector('p')!
      const blockquote = compactRoot.querySelector('blockquote')!
      const inlineCode = compactRoot.querySelector('p code')!
      const pre = compactRoot.querySelector('pre')!
      const th = compactRoot.querySelector('th')!
      const image = compactRoot.querySelector('img')!
      return {
        h1Text: h1.textContent,
        h1FontSize: getComputedStyle(h1).fontSize,
        h2FontSize: getComputedStyle(h2).fontSize,
        paragraphFontSize: getComputedStyle(paragraph).fontSize,
        compactFontSize: getComputedStyle(compact).fontSize,
        standardFontSize: getComputedStyle(standard).fontSize,
        hasStrong: Boolean(compactRoot.querySelector('strong')),
        hasEmphasis: Boolean(compactRoot.querySelector('em')),
        hasStrike: Boolean(compactRoot.querySelector('s')),
        hasAutolink: compactRoot.querySelector('a')?.getAttribute('href'),
        hasNestedList: Boolean(compactRoot.querySelector('li ul')),
        blockquoteBorder: getComputedStyle(blockquote).borderLeftWidth,
        inlineCodeBackground: getComputedStyle(inlineCode).backgroundColor,
        preOverflow: getComputedStyle(pre).overflowX,
        tableHeaderDisplay: getComputedStyle(th).display,
        tableHeaderWeight: getComputedStyle(th).fontWeight,
        imageMaxWidth: getComputedStyle(image).maxWidth,
        imageAlt: image.getAttribute('alt'),
        unsafeScript: Boolean(compactRoot.querySelector('script')),
        unsafeHandler: compactRoot.querySelector('img[src="x"]')?.getAttribute('onerror') ?? null,
        emptyText: empty.shadowRoot.textContent?.trim(),
        standardHasHeading: Boolean(standardRoot.querySelector('h1')),
      }
    })

    expect(state.h1Text).toBe('Hello darkness')
    expect(Number.parseFloat(state.h1FontSize)).toBeGreaterThan(Number.parseFloat(state.h2FontSize))
    expect(Number.parseFloat(state.h2FontSize)).toBeGreaterThan(Number.parseFloat(state.paragraphFontSize))
    expect(Number.parseFloat(state.standardFontSize)).toBeGreaterThan(Number.parseFloat(state.compactFontSize))
    expect(state.hasStrong).toBe(true)
    expect(state.hasEmphasis).toBe(true)
    expect(state.hasStrike).toBe(true)
    expect(state.hasAutolink).toBe('https://example.com')
    expect(state.hasNestedList).toBe(true)
    expect(state.blockquoteBorder).toBe('3px')
    expect(state.inlineCodeBackground).toBe('rgb(246, 248, 250)')
    expect(state.preOverflow).toBe('auto')
    expect(state.tableHeaderDisplay).toBe('table-cell')
    expect(Number.parseInt(state.tableHeaderWeight, 10)).toBeGreaterThanOrEqual(600)
    expect(state.imageMaxWidth).toBe('100%')
    expect(state.imageAlt).toBe('Alt text')
    expect(state.unsafeScript).toBe(false)
    expect(state.unsafeHandler).toBeNull()
    expect(state.emptyText).toBe('Nothing here.')
    expect(state.standardHasHeading).toBe(true)
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
          body { --fontStack-system: system-ui; --fontStack-monospace: ui-monospace; --text-codeBlock-size: 13px; --base-text-weight-normal: 400; --base-text-weight-medium: 500; --base-text-weight-semibold: 600; --base-text-lineHeight-tight: 1.25; --base-text-lineHeight-snug: 1.375; --base-text-lineHeight-normal: 1.5; --base-text-lineHeight-relaxed: 1.625; --lv-type-caption: 400 12px/1.25 system-ui; --lv-type-secondary: 400 12px/1.625 system-ui; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-body-large: 400 16px/1.5 system-ui; --lv-type-section-title: 600 16px/1.5 system-ui; --lv-type-page-title: 600 20px/1.625 system-ui; --lv-type-title-large: 600 32px/1.5 system-ui; --lv-type-code-block: 400 13px/1.5 ui-monospace; --lv-type-code-inline: 400 0.9285em ui-monospace; --fontStack-monospace: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control: #f6f8fa; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-line-muted: #d8dee4; --lv-border-width: 1px; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --base-size-4: 4px; --base-size-8: 8px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --lv-space-2xs: 2px; --lv-space-xs: 4px; --lv-chat-markdown-block-gap: 10px; --lv-chat-markdown-list-indent: 20px; --lv-chat-markdown-list-item-gap: 2px; --lv-chat-code-radius: 4px; --lv-chat-code-padding-block: 1px; --lv-chat-code-padding-inline: 4px; --lv-chat-code-font-scale: 0.92em; --lv-chat-pre-padding-block: 9px; --lv-chat-pre-padding-inline: 10px; --lv-chat-quote-border-width: 2px; --lv-chat-bubble-padding-block: 12px; --lv-chat-link-underline-thickness: 1px; --lv-chat-link-underline-offset: 2px; }
          lv-markdown-view { display: block; width: 760px; margin: 24px; }
        </style>
      </head>
      <body>
        <script type="module" src="/markdown-view-under-test.js"></script>
      </body>
    </html>
  `
}
