import { expect, test } from 'bun:test'

import visualDocumentation from '../../../../docs/visuals/examples.gen.json'
import { formatTooltipEntries } from './tooltip-format'

test('generated revenue_line_status tooltip formats its partial currency override', () => {
  const envelope = (visualDocumentation as any).documents['visuals/line'].find((candidate: any) => candidate.visualID === 'revenue_line_status')
  if (!envelope) throw new Error('generated revenue_line_status fixture is missing')
  const dataset = envelope.dataState.datasets.find((candidate: any) => candidate.id === 'primary')
  const entries = formatTooltipEntries(envelope, dataset.rows[0], 'primary', { locale: 'en-US' }, envelope.spec.tooltip, envelope.spec.tooltipItems)

  expect(entries.find((entry) => entry.label === 'Revenue')).toEqual({ label: 'Revenue', value: '$357' })
})
