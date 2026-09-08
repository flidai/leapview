import { expect, test } from 'bun:test'
import type { AgentContextSignal } from '../../generated/signals'
import { exploreContextHref } from './explore-context'

test('chat context link uses the canonical authorized exploration state', () => {
  const context = { surface: 'dashboard', dashboardId: 'dashboard:sales', pageId: 'overview', exploration: { schemaVersion: 1, modelId: 'semantic:sales', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 } } as AgentContextSignal
  const href = exploreContextHref(context)
  expect(href).toContain('/explore?')
  expect(href).toContain('mode=explore')
  expect(new URLSearchParams(href?.split('?')[1]).get('returnDashboard')).toBe('dashboard:sales')
  expect(JSON.parse(new URLSearchParams(href?.split('?')[1]).get('state')!).modelId).toBe('semantic:sales')
})

test('chat context link is absent without a canonical exploration', () => {
  expect(exploreContextHref({} as AgentContextSignal)).toBeUndefined()
})
