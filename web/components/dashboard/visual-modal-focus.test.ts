import { expect, test } from 'bun:test'
import { visualSourceFromEvent } from './visual-modal-focus'

test('visualSourceFromEvent returns the first focusable visual in the composed path', () => {
  const originalHTMLElement = globalThis.HTMLElement
  class TestHTMLElement {
    constructor(readonly localName: string) {}
  }
  globalThis.HTMLElement = TestHTMLElement as unknown as typeof HTMLElement
  try {
    const button = new TestHTMLElement('button')
    const chart = new TestHTMLElement('lv-visualization-host')
    const table = new TestHTMLElement('lv-visualization-host')
    const event = { composedPath: () => [button, chart, table] } as unknown as Event

    expect(visualSourceFromEvent(event)).toBe(chart as unknown as HTMLElement)
  } finally {
    globalThis.HTMLElement = originalHTMLElement
  }
})
