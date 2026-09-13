import { expect, test } from 'bun:test'
import { defaultRendererContext } from '../../host-controller'
import { echartsOption } from '../echarts'
import { cartesianFixture } from '../echarts-test-fixtures'

test('bar legends reflect conditional fills and retain their series identity and authored label', () => {
  const envelope = cartesianFixture('bar')
  if (envelope.spec.kind !== 'cartesian' || envelope.dataState.kind !== 'inline') throw new Error('invalid fixture')
  envelope.spec.conditionalFormatting = [{
    id: 'variance', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'less_than', value: 0, style: { color: 'danger' } }], defaultStyle: { color: 'success' }, nullStyle: { color: 'neutral' } },
  }]
  envelope.spec.presentation.legendItems = [{ value: 'value', label: 'Revenue variance' }]
  envelope.dataState.datasets[0]!.rows = [['Below', -20], ['Above', 10]]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.formatter('value')).toBe('Revenue variance')
  expect(option.legend.data).toEqual([{ name: 'value', itemStyle: { color: {
    type: 'linear', x: 0, y: 0, x2: 1, y2: 0, colorStops: [
      { offset: 0, color: defaultRendererContext.colors.danger }, { offset: 0.5, color: defaultRendererContext.colors.danger },
      { offset: 0.5, color: defaultRendererContext.colors.success }, { offset: 1, color: defaultRendererContext.colors.success },
    ],
  } } }])
  envelope.dataState.datasets[0]!.rows = [['Above', 10]]
  expect((echartsOption(envelope, defaultRendererContext) as any).legend.data[0].itemStyle.color).toBe(defaultRendererContext.colors.success)
  envelope.spec.presentation.legend = 'hidden'
  expect((echartsOption(envelope, defaultRendererContext) as any).legend).toBeUndefined()
})

test('ordinary bars retain their automatic series legend', () => {
  expect((echartsOption(cartesianFixture('bar'), defaultRendererContext) as any).legend.data).toBeUndefined()
})
