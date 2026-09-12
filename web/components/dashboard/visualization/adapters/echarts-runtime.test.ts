import { expect, test } from 'bun:test'
import { use } from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'

import visualDocumentation from '../../../../../docs/visuals/examples.gen.json'
import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'
import { init } from './echarts-runtime'

// Production registers CanvasRenderer. SVG makes the same registered chart and
// component set observable without a browser DOM in this contract test.
use([SVGRenderer])

test('the modular runtime renders every documented ECharts mark', () => {
  const documents = Object.values(visualDocumentation.documents as Record<string, VisualizationEnvelope[]>).flat()
  const envelopes = documents.filter((envelope) => envelope.rendererID === 'echarts')
  const rendered = new Set<string>()
  const warnings: unknown[][] = []
  const originalWarn = console.warn
  console.warn = (...args: unknown[]) => { warnings.push(args) }

  try {
    for (const envelope of envelopes) {
      const chart = init(null, null, { renderer: 'svg', ssr: true, width: 800, height: 500 })
      try {
        chart.setOption(echartsOption(envelope, defaultRendererContext))
        expect(chart.getModel().getSeriesCount()).toBeGreaterThan(0)
        rendered.add(envelope.spec.kind === 'point' ? 'point:scatter' : `${envelope.spec.kind}:${envelope.spec.mark}`)
      } finally {
        chart.dispose()
      }
    }
  } finally {
    console.warn = originalWarn
  }

  expect(warnings).toEqual([])
  expect([...rendered].sort()).toEqual([
    'cartesian:area',
    'cartesian:bar',
    'cartesian:boxplot',
    'cartesian:candlestick',
    'cartesian:column',
    'cartesian:combo',
    'cartesian:heatmap',
    'cartesian:histogram',
    'cartesian:line',
    'cartesian:waterfall',
    'hierarchy:graph',
    'hierarchy:sankey',
    'hierarchy:sunburst',
    'hierarchy:tree',
    'hierarchy:treemap',
    'point:scatter',
    'polar:gauge',
    'polar:radar',
    'proportional:donut',
    'proportional:funnel',
    'proportional:pie',
  ])
})
