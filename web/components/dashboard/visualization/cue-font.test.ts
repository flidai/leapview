import { expect, test } from 'bun:test'

import { CHART_CUE_FONT_FAMILY, CHART_CUE_GLYPHS, chartCueFontStack, proportionalCueFontNeeded, requireChartCueFont, waitForChartBaseFont, waitForChartCueFont } from './cue-font'
import { defaultRendererContext } from './host-controller'
import { echartsOption } from './adapters/echarts'
import { proportionalFixture } from './adapters/echarts-test-fixtures'

test('chart cue asset covers every authored conditional glyph', () => {
  expect([...CHART_CUE_GLYPHS]).toEqual(['●', '■', '◆', '▲', '▼', '↑', '↓', '⚠'])
  expect(chartCueFontStack('"Inter Variable", system-ui')).toBe(`'${CHART_CUE_FONT_FAMILY}', "Inter Variable", system-ui`)
})

test('chart cue asset is the checked-in restricted subset', async () => {
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

test('cue font readiness requires the intended face and handles load errors', async () => {
  const loadedCalls: string[] = []
  const loaded = await waitForChartCueFont({
    check: () => true,
    load: async (font, glyphs) => {
      loadedCalls.push(`${font}:${glyphs}`)
      return [{}]
    },
  })
  expect(loaded).toBe('ready')
  expect(loadedCalls).toHaveLength(1)

  const missingFace = await waitForChartCueFont({ check: () => true, load: async () => [] })
  expect(missingFace).toBe('unavailable')

  let retryLoads = 0
  const retryFonts = {
    check: () => retryLoads > 1,
    load: async () => {
      retryLoads++
      return retryLoads > 1 ? [{}] : []
    },
  }
  expect(await waitForChartCueFont(retryFonts)).toBe('unavailable')
  expect(await waitForChartCueFont(retryFonts)).toBe('ready')

  const rejected = await waitForChartCueFont({ check: () => false, load: async () => Promise.reject(new Error('font unavailable')) })
  expect(rejected).toBe('unavailable')
  await expect(requireChartCueFont({ check: () => false, load: async () => [] })).rejects.toThrow('chart cue font failed to load')
})

test('cue font readiness is bounded when the browser font pipeline stalls', async () => {
  const started = performance.now()
  const result = await waitForChartCueFont({ check: () => false, load: () => new Promise(() => {}) }, 10)
  expect(result).toBe('unavailable')
  expect(performance.now() - started).toBeLessThan(500)
})

test('base chart font readiness loads ordinary labels at rendered weights', async () => {
  const loaded: string[] = []
  const result = await waitForChartBaseFont({
    check: (font) => font.includes('Inter Variable'),
    load: async (font, text) => {
      loaded.push(`${font}:${text}`)
      return []
    },
  }, '"Inter Variable", system-ui')
  expect(result).toBe('ready')
  expect(loaded.map((font) => font.split(' ')[0])).toEqual(['400', '500', '600'])
  expect(loaded.every((font) => font.endsWith(':Chart Value 0123'))).toBe(true)
})

test('base chart font readiness allows an empty system fallback result', async () => {
  const result = await waitForChartBaseFont({ check: () => true, load: async () => [] }, 'system-ui')
  expect(result).toBe('ready')
})

test('base chart font readiness keeps independent family cache keys', async () => {
  let loads = 0
  const fonts = { check: () => true, load: async () => { loads++; return [] } }
  expect(await waitForChartBaseFont(fonts, 'family-a')).toBe('ready')
  expect(await waitForChartBaseFont(fonts, 'family-a')).toBe('ready')
  expect(await waitForChartBaseFont(fonts, 'family-b')).toBe('ready')
  expect(loads).toBe(6)
})

test('base chart font readiness evicts rejected loads for retry', async () => {
  let loads = 0
  const fonts = {
    check: () => true,
    load: async () => {
      loads++
      if (loads <= 3) throw new Error('font unavailable')
      return []
    },
  }
  expect(await waitForChartBaseFont(fonts, 'retry-family')).toBe('unavailable')
  expect(await waitForChartBaseFont(fonts, 'retry-family')).toBe('ready')
  expect(loads).toBe(6)
})

test('base chart font readiness rejects a false post-load check', async () => {
  expect(await waitForChartBaseFont({ check: () => false, load: async () => [] }, 'missing-family')).toBe('unavailable')
})

test('base chart font readiness evicts timed out loads for retry', async () => {
  let blocked = true
  const fonts = {
    check: () => true,
    load: () => blocked ? new Promise<readonly unknown[]>(() => {}) : Promise.resolve([]),
  }
  expect(await waitForChartBaseFont(fonts, 'slow-family', 10)).toBe('unavailable')
  blocked = false
  expect(await waitForChartBaseFont(fonts, 'slow-family', 10)).toBe('ready')
})

test('base chart font readiness is unavailable without a browser font set', async () => {
  expect(await waitForChartBaseFont(undefined, 'system-ui')).toBe('unavailable')
})

test('cue font applicability is limited to conditional pie and donut labels', () => {
  const pie = proportionalFixture('pie')
  if (pie.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  pie.spec.conditionalFormatting = [{
    id: 'cue', target: 'series_color', field: pie.spec.value,
    rule: {
      kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }],
      nullStyle: {}, defaultStyle: {},
    },
  }]
  expect(proportionalCueFontNeeded(pie)).toBe(true)
  const option = echartsOption(pie, defaultRendererContext) as { series: Array<{ label: { fontFamily?: string } }> }
  expect(option.series[0]?.label.fontFamily).toBe("'LeapView Chart Cues', system-ui")
  pie.spec.mark = 'funnel'
  expect(proportionalCueFontNeeded(pie)).toBe(false)
})
