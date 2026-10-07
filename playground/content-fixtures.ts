import { createChartFixture, defaultChartOptions } from './chart-fixtures'
import type { ChatContextReference } from '../web/components/chat/reference'

export { contentExamples } from './catalog'

export const composerReferences: ChatContextReference[] = [
  { reference: { kind: 'visual', id: 'regional-revenue' }, name: 'Regional revenue', description: 'Revenue by region', visualType: 'bar', hierarchy: ['Finance', 'Quarterly review'], href: '#charts/bar', locations: [], context: ['current_page'] },
  { reference: { kind: 'metric', id: 'total-revenue' }, name: 'Total revenue', description: 'Sum of completed orders', hierarchy: ['Orders'], href: '#charts/kpi', locations: [], context: [] },
  { reference: { kind: 'model', id: 'orders' }, name: 'Orders', description: 'Completed and pending orders', hierarchy: ['Commerce'], href: '#content/config-viewer', locations: [], context: [] },
]

export const configurationYAML = `name: regional_revenue
description: Revenue by region for the quarterly review
enabled: true
refresh_minutes: 30
owner: null
tags:
  - finance
  - quarterly
model:
  source: orders
  definition:
    sql: |
      SELECT region, SUM(revenue) AS revenue
      FROM orders
      GROUP BY region
display:
  title: Regional revenue
  format: currency
`

export const configurationJSON = JSON.stringify({
  name: 'regional_revenue', description: 'Revenue by region for the quarterly review', enabled: true,
  refresh_minutes: 30, owner: null, tags: ['finance', 'quarterly'],
  model: { source: 'orders', definition: { sql: 'SELECT region, SUM(revenue) AS revenue FROM orders GROUP BY region' } },
  display: { title: 'Regional revenue', format: 'currency' },
}, null, 2)

export const markdownFixture = `## Quarterly revenue review

Revenue increased **18%** across the four reporting regions. This note uses the production Markdown renderer.

### Findings

- **North** led the quarter with a strong renewal rate.
- _West_ improved after a slower start.
- Follow up on accounts marked \`pending\`.

| Region | Revenue | Change |
| --- | ---: | ---: |
| North | €124,800 | +22% |
| West | €98,600 | +14% |

> Figures are deterministic playground fixtures.

\`\`\`sql
SELECT region, SUM(revenue) AS revenue
FROM orders
GROUP BY region;
\`\`\`

1. Review the regional breakdown.
2. Share the next reporting date with the team.

---

[Open the code block example](#content/code-block)
`

export const codeFixtures: Record<string, string> = {
  sql: 'SELECT region, SUM(revenue) AS revenue\nFROM orders\nWHERE status = \'complete\'\nGROUP BY region\nORDER BY revenue DESC;',
  yaml: configurationYAML,
  json: configurationJSON,
  markdown: markdownFixture,
  text: 'Quarterly revenue review\n\nNorth: €124,800 (+22%)\nWest: €98,600 (+14%)\n\nAll figures are local fixtures.',
  shell: '# Example command — displayed only\nleapview --help',
  toon: 'regions[2]{name,revenue}:\n  North,124800\n  West,98600',
}

export type ArtifactState = 'ready' | 'limited' | 'loading' | 'empty' | 'error' | 'unavailable' | 'unsupported'

export function createArtifactFixture(state: ArtifactState) {
  const status = state === 'loading' || state === 'empty' || state === 'error' ? state : 'ready'
  const { envelope } = createChartFixture('bar', { ...defaultChartOptions, status, multiSeries: false })
  // This compact artifact is an informational visual; interactive charts have their own gallery.
  envelope.spec.interactions = []
  if (state === 'limited') envelope.spec.dataBudget.maxRows = 8
  return envelope
}
