import { afterAll, beforeAll } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

export function chatThreadBrowserFixture() {
  const fixture = { browser: undefined as unknown as Browser, baseURL: '' }
  let server: Server

  beforeAll(async () => {
    const root = join(process.cwd(), '.tmp/chat-thread-test')
    server = createServer(async (request, response) => {
      const url = new URL(request.url ?? '/', 'http://127.0.0.1')
      if (url.pathname !== '/') {
        const file = normalize(join(root, url.pathname))
        if (!file.startsWith(root)) {
          response.writeHead(404)
          response.end('not found')
          return
        }
        try {
          response.setHeader('content-type', 'text/javascript')
          response.end(await readFile(file))
          return
        } catch {
          response.writeHead(404)
          response.end('not found')
          return
        }
      }
      if (url.pathname === '/') {
        response.setHeader('content-type', 'text/html')
        response.end(`
        <!doctype html>
        <html>
          <head>
            <style>
              :root {
                --lv-chart-surface: rgb(1, 2, 3);
                --lv-border-default: 2px solid rgb(4, 5, 6);
                ${typographyTestTokens}
                --fontStack-monospace: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  	              --lv-bg-app: rgb(11, 12, 13);
                --lv-bg-page: #fff;
                --lv-bg-panel: #fff;
                --lv-bg-panel-muted: #f6f8fa;
                --lv-bg-control: #f6f8fa;
                --lv-fg-default: #24292f;
                --lv-fg-muted: #57606a;
                --lv-fg-accent: #0969da;
                --lv-line-muted: #d8dee4;
                --lv-border-width: 1px;
                --lv-border-muted: 1px solid #d8dee4;
                --lv-radius-default: 6px;
                --base-size-4: 4px;
                --base-size-8: 8px;
                --base-size-12: 12px;
                --base-size-16: 16px;
                --base-size-20: 20px;
                --lv-space-sm: 8px;









                --lv-chat-thread-padding: 16px;
                --lv-chat-stack-width: 760px;
                --lv-chat-stack-gap: 16px;
                --lv-chat-message-width: 760px;
                --lv-chat-message-gap: 8px;
                --lv-chat-agent-item-gap: 8px;
                --lv-chat-empty-min-height: 180px;
                --lv-chat-bubble-padding-block: 12px;
                --lv-chat-bubble-padding-inline: 16px;
                --lv-chat-markdown-block-gap: 10px;
                --lv-chat-markdown-list-indent: 20px;
                --lv-chat-markdown-list-item-gap: 2px;
                --lv-chat-code-radius: 4px;
                --lv-chat-code-padding-block: 1px;
                --lv-chat-code-padding-inline: 4px;
                --lv-chat-code-font-scale: 0.92em;
                --lv-chat-pre-padding-block: 9px;
                --lv-chat-pre-padding-inline: 10px;
                --lv-chat-quote-border-width: 2px;
                --lv-chat-link-underline-thickness: 1px;
                --lv-chat-link-underline-offset: 2px;
              }
            </style>
            <script type="module" src="/chat-under-test.js"></script>
          </head>
          <body><lv-chat-thread></lv-chat-thread><lv-visual-modal></lv-visual-modal></body>
        </html>
      `)
      }
    })
    await new Promise<void>((resolve) => server.listen(0, resolve))
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
    fixture.baseURL = `http://127.0.0.1:${address.port}`
    fixture.browser = await chromium.launch()
  })

  afterAll(async () => {
    await fixture.browser?.close()
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }, 15_000)

  return fixture
}
