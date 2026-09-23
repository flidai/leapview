import { expect, test } from 'bun:test'

import { echartsOption } from './adapters/echarts'
import { proportionalFixture } from './adapters/echarts-test-fixtures'
import {
  CHART_CUE_FONT_FAMILY,
  CHART_CUE_GLYPHS,
  chartCueFontStack,
  proportionalCueFontNeeded,
  requireChartCueAndBaseFonts,
  waitForChartBaseFont,
  waitForChartCueFont,
} from './cue-font'
import { defaultRendererContext } from './host-controller'

test('chart cue asset covers every authored conditional glyph', async () => {
  expect([...CHART_CUE_GLYPHS]).toEqual(['●', '■', '◆', '▲', '▼', '↑', '↓', '⚠'])
  expect(chartCueFontStack('"Inter Variable", system-ui')).toBe(`'${CHART_CUE_FONT_FAMILY}', "Inter Variable", system-ui`)

  const path = 'static/files/noto-sans-symbols-2-cues-400-normal.woff2'
  const file = Bun.file(path)
  expect(await file.exists()).toBe(true)
  const hash = new Bun.CryptoHasher('sha256')
  hash.update(new Uint8Array(await file.arrayBuffer()))
  expect(hash.digest('hex')).toBe('814efd1ab221bc83136fd9c3e485dc21863737e422e733abe43086235b44c03c')
  const stylesheet = await Bun.file('static/app.input.css').text()
  for (const codepoint of ['U+2191', 'U+2193', 'U+25A0', 'U+25B2', 'U+25BC', 'U+25C6', 'U+25CF', 'U+26A0']) {
    expect(stylesheet).toContain(codepoint)
  }
})

test('cue font readiness requires the intended face and retries failures', async () => {
  const calls: string[] = []
  expect(await waitForChartCueFont({
    check: () => true,
    load: async (font, glyphs) => { calls.push(`${font}:${glyphs}`); return [{}] },
  })).toBe('ready')
  expect(calls).toHaveLength(1)

  expect(await waitForChartCueFont({ check: () => true, load: async () => [] })).toBe('unavailable')

  let loads = 0
  const fonts = {
    check: () => loads > 1,
    load: async () => { loads++; return loads > 1 ? [{}] : [] },
  }
  expect(await waitForChartCueFont(fonts)).toBe('unavailable')
  expect(await waitForChartCueFont(fonts)).toBe('ready')
})

test('cue and base font readiness rejects unavailable faces', async () => {
  await expect(requireChartCueAndBaseFonts('system-ui', {
    check: () => false,
    load: async () => [],
  })).rejects.toThrow('chart cue font failed to load')
})

test('font readiness is bounded when the browser font pipeline stalls', async () => {
  const started = performance.now()
  expect(await waitForChartCueFont({ check: () => false, load: () => new Promise(() => {}) }, 10)).toBe('unavailable')
  expect(performance.now() - started).toBeLessThan(500)
})

test('base chart font readiness loads every rendered label weight and caches by family', async () => {
  const loaded: string[] = []
  const fonts = {
    check: (font: string) => font.includes('Inter Variable') || font.includes('Other'),
    load: async (font: string, text?: string) => { loaded.push(`${font}:${text}`); return [] },
  }
  expect(await waitForChartBaseFont(fonts, '"Inter Variable", system-ui')).toBe('ready')
  expect(await waitForChartBaseFont(fonts, '"Inter Variable", system-ui')).toBe('ready')
  expect(await waitForChartBaseFont(fonts, 'Other')).toBe('ready')
  expect(loaded.map((font) => font.split(' ')[0])).toEqual(['400', '500', '600', '400', '500', '600'])
  expect(loaded.every((font) => font.endsWith(':Chart Value 0123'))).toBe(true)
})

test('cue font applicability follows proportional icon-bearing formats', () => {
  const envelope = proportionalFixture('pie') as any
  envelope.spec.conditionalFormatting = [{
    id: 'cue', target: 'series_color', field: envelope.spec.value,
    rule: {
      kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }],
      nullStyle: {}, defaultStyle: {},
    },
  }]

  for (const mark of ['pie', 'donut', 'funnel']) {
    envelope.spec.mark = mark
    expect(proportionalCueFontNeeded(envelope)).toBe(true)
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.series[0].label.fontFamily).toBe("'LeapView Chart Cues', system-ui")
  }

  envelope.spec.conditionalFormatting[0].rule.rules[0].style = { color: 'success' }
  expect(proportionalCueFontNeeded(envelope)).toBe(false)
})
