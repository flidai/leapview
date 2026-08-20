import { afterEach, describe, expect, test } from 'bun:test'
import {
  DashboardAgentStateController,
  DashboardNavigationController,
  DashboardOptimisticInteractionController,
  readDashboardAgentState,
} from './dashboard-page-controller'

function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key),
    setItem: (key, value) => values.set(key, value),
  }
}

afterEach(() => {
  // Keep timer-backed tests deterministic when this file is run in a shared
  // Bun worker.
})

describe('DashboardAgentStateController', () => {
  test('round-trips drawer and conversation state through storage', () => {
    const storage = memoryStorage()
    const controller = new DashboardAgentStateController(storage)
    expect(controller.initialize(true)).toEqual({ open: false, conversationId: '' })
    controller.setOpen(true)
    controller.syncConversation(' conversation-1 ')
    expect(readDashboardAgentState(storage)).toEqual({ open: true, conversationId: 'conversation-1' })
    controller.newConversation()
    expect(controller.state).toEqual({ open: true, conversationId: '' })
  })
})

describe('DashboardNavigationController', () => {
  test('gates deferred navigation behind apply/discard decisions', () => {
    const controller = new DashboardNavigationController()
    const prompts: string[] = []
    expect(controller.begin({ href: '/next', pageId: 'next' }, {
      active: false,
      deferred: true,
      dirty: true,
      confirm: (message) => { prompts.push(message); return true },
    })).toBe('apply')
    expect(controller.markRequested()).toEqual({ href: '/next', pageId: 'next' })
    expect(controller.navigationRequested).toBe(true)
    expect(controller.complete('next')).toEqual({ href: '/next', pageId: 'next' })
    expect(prompts).toHaveLength(1)
  })
})

describe('DashboardOptimisticInteractionController', () => {
  test('reconciles only after the expected generation', () => {
    const changes: number[] = []
    const controller = new DashboardOptimisticInteractionController((snapshot) => changes.push(snapshot.expectedGeneration))
    controller.setSelections([{ sourceKind: 'visual', sourceId: 'orders', entries: [] }], 4)
    expect(controller.reconcile(4)).toBe(false)
    expect(controller.state.selections).not.toBeNull()
    expect(controller.reconcile(5)).toBe(true)
    expect(controller.state.selections).toBeNull()
    expect(changes.length).toBeGreaterThanOrEqual(2)
  })

  test('rolls back to the current server generation before the next interaction', () => {
    let generation = 4
    const controller = new DashboardOptimisticInteractionController(undefined, () => generation)
    controller.setSelections([{ sourceKind: 'visual', sourceId: 'orders', entries: [] }], generation)
    generation = 8
    controller.rollback()
    expect(controller.state).toEqual({ selections: null, spatialSelections: null, expectedGeneration: 8 })

    controller.setSelections([{ sourceKind: 'visual', sourceId: 'orders', entries: [] }], generation)
    expect(controller.reconcile(9)).toBe(true)
  })
})
