import { expect, test } from 'bun:test'
import type { ChatTranscriptItemSignal } from '../../generated/signals'
import { dashboardGenerationState, isDashboardBuildPrompt } from './dashboard-generation-state'

const tool = (name: string, status = 'running', runId = 'new'): ChatTranscriptItemSignal => ({ id: name, kind: 'tool', name, status, runId })

test('dashboard build intent opens only for a requested dashboard action', () => {
  for (const text of ['Build a dashboard of sales', 'Please create me a dashboard', 'Can you make a sales dashboard?', 'Design a dashboard for finance', 'Generate a dashboard', 'Create a new report with metrics', 'Can you build me a dashboard?', 'Could you help me make a sales dashboard?', 'I would like you to create a dashboard', 'Help me build a dashboard']) expect(isDashboardBuildPrompt(text)).toBe(true)
  for (const text of ['What is a dashboard?', 'Explain the dashboard filter', 'List dashboards', 'Do not create a dashboard', 'Don’t build a dashboard', 'Create a chart of sales', 'Show the dashboard', 'Explain how to create a dashboard', 'How do I build a dashboard?', 'Could you tell me how to build a dashboard?', 'Please explain how to make a dashboard', 'I want to understand how to build a dashboard']) expect(isDashboardBuildPrompt(text)).toBe(false)
})

test('assembly stage follows the active run, never stale tools or elapsed time', () => {
  const transcript = [tool('preview_dashboard_draft', 'complete', 'old'), tool('catalog_search')]
  expect(dashboardGenerationState(transcript, 'new', true).stage).toBe('discover')
  expect(dashboardGenerationState(transcript, '', true).stage).toBe('discover')
  expect(dashboardGenerationState([...transcript, tool('create_dashboard_draft')], 'new', true).stage).toBe('assemble')
  expect(dashboardGenerationState([...transcript, tool('query_visual')], 'new', true).stage).toBe('query')
  expect(dashboardGenerationState([...transcript, tool('preview_dashboard_draft')], 'new', true).stage).toBe('preview')
})

test('stopped and failed runs do not claim progress or completion', () => {
  expect(dashboardGenerationState([], 'new', false).label).toBe('Dashboard generation paused')
  expect(dashboardGenerationState([], 'new', false, 'Request failed').label).toBe('Dashboard generation interrupted')
  const failed = { ...tool('preview_dashboard_draft', 'error'), error: 'Invalid metric' }
  expect(dashboardGenerationState([failed], 'new', true).stage).toBe('assemble')
  expect(dashboardGenerationState([failed], 'new', false).detail).toBe('Invalid metric')
})
