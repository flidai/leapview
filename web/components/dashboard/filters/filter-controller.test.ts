import { expect, test } from 'bun:test'
import type { DashboardFilterCommand, DashboardFilterExpression, DashboardFilterState } from '../../../generated/signals'
import { DashboardFilterController } from './filter-controller'

const unfiltered: DashboardFilterExpression = { kind: 'unfiltered' }

function setExpression(value: string): DashboardFilterExpression {
  return { kind: 'set', operator: 'in', values: [{ kind: 'string', value }] }
}

function state(revision: number): DashboardFilterState {
  return {
    revision,
    defaultsRevision: 'defaults',
    appliedControls: {
      state: { expression: unfiltered, resolvedExpression: unfiltered },
    },
    draftControls: {},
    dirtyBindings: [],
  }
}

test('filter controller serializes commands and rebases queued mutations after reconciliation', () => {
  const sent: DashboardFilterCommand[] = []
  const controller = new DashboardFilterController((command) => sent.push(command), () => 'mutation-id')
  controller.reconcile(state(4))

  controller.mutate('state', {
    kind: 'set',
    operator: 'in',
    values: [{ kind: 'string', value: 'CA' }],
  })
  controller.clear('state')

  expect(sent).toHaveLength(1)
  expect(sent[0]?.baseRevision).toBe(4)
  controller.reconcile(state(5))
  expect(sent).toHaveLength(2)
  expect(sent[1]?.baseRevision).toBe(5)
})

test('filter controller reports pending state only for affected bindings', () => {
  let mutation = 0
  const controller = new DashboardFilterController(() => {}, () => `mutation-${++mutation}`)
  controller.reconcile(state(4))

  controller.mutate('state', setExpression('CA'))

  expect(controller.pending).toBe(true)
  expect(controller.pendingFor('state')).toBe(true)
  expect(controller.pendingFor('category')).toBe(false)
})

test('filter controller normalizes sparse empty collections at the signal boundary', () => {
  const sent: DashboardFilterCommand[] = []
  const controller = new DashboardFilterController((command) => sent.push(command), () => 'mutation-id')
  controller.reconcile({
    revision: 3,
    defaultsRevision: 'defaults',
    appliedControls: {},
    draftControls: {},
  } as DashboardFilterState)

  controller.mutate('state', setExpression('CA'))

  expect(sent[0]?.baseRevision).toBe(3)
  expect(controller.projected.dirtyBindings).toEqual([])
})

test('filter controller removes stale union fields from streamed unfiltered expressions', () => {
  const controller = new DashboardFilterController(() => {})
  const staleUnfiltered = {
    kind: 'unfiltered',
    operator: 'in',
    values: [{ kind: 'boolean', value: true }],
  } as unknown as DashboardFilterExpression
  controller.reconcile({
    revision: 6,
    defaultsRevision: 'defaults',
    appliedControls: {
      delivered: { expression: staleUnfiltered, resolvedExpression: staleUnfiltered },
    },
    draftControls: {},
    dirtyBindings: [],
  })

  expect(controller.projected.appliedControls.delivered).toEqual({
    expression: { kind: 'unfiltered' },
    resolvedExpression: { kind: 'unfiltered' },
  })
})

test('filter controller projects optimistic state without replacing unrelated controls', () => {
  const controller = new DashboardFilterController(() => {}, () => 'mutation-id')
  const current = state(2)
  current.appliedControls.category = { expression: unfiltered, resolvedExpression: unfiltered }
  controller.reconcile(current)

  controller.mutate('state', {
    kind: 'set',
    operator: 'in',
    values: [{ kind: 'string', value: 'WA' }],
  })

  expect(controller.projected.appliedControls.state.expression).toEqual({
    kind: 'set',
    operator: 'in',
    values: [{ kind: 'string', value: 'WA' }],
  })
  expect(controller.projected.appliedControls.category.expression).toEqual(unfiltered)
})

test('filter controller clears one binding atomically while preserving unrelated filters', () => {
  const sent: DashboardFilterCommand[] = []
  const controller = new DashboardFilterController(command => sent.push(command), () => 'clear-one')
  const current = state(5)
  current.appliedControls.state = { expression: setExpression('CA'), resolvedExpression: setExpression('CA') }
  current.appliedControls.category = { expression: setExpression('books'), resolvedExpression: setExpression('books') }
  controller.reconcile(current)

  controller.clear('state')

  expect(sent[0]).toMatchObject({ kind: 'mutate', operation: 'clear', bindingKey: 'state', baseRevision: 5 })
  expect(controller.projected.appliedControls.state.expression).toEqual(unfiltered)
  expect(controller.projected.appliedControls.category.expression).toEqual(setExpression('books'))
})

test('filter controller does not expose draft state as applied in deferred mode', () => {
  const controller = new DashboardFilterController(() => {}, () => 'mutation-id')
  const current = state(7)
  current.draftControls.state = {
    kind: 'set',
    operator: 'in',
    values: [{ kind: 'string', value: 'OR' }],
  }
  current.dirtyBindings = ['state']
  controller.reconcile(current)

  expect(controller.expression('state')).toEqual(current.draftControls.state)
  expect(controller.projected.appliedControls.state.expression).toEqual(unfiltered)
})

test('filter controller optimistically applies all deferred drafts together', () => {
  const sent: DashboardFilterCommand[] = []
  const controller = new DashboardFilterController(command => sent.push(command), () => 'apply-1')
  controller.setApplicationMode('deferred')
  const current = state(4)
  current.draftControls.state = setExpression('CA')
  current.draftControls.category = setExpression('books')
  current.dirtyBindings = ['category', 'state']
  controller.reconcile(current)

  controller.apply()

  expect(controller.projected.appliedControls.state?.expression).toEqual(setExpression('CA'))
  expect(controller.projected.appliedControls.category?.expression).toEqual(setExpression('books'))
  expect(controller.projected.draftControls).toEqual({})
  expect(controller.projected.dirtyBindings).toEqual([])
  expect(sent[0]?.baseRevision).toBe(4)
})

test('filter controller restores canonical state when a mutation is rejected at the same revision', () => {
  const controller = new DashboardFilterController(() => {}, () => 'invalid-range')
  const canonical = state(4)
  canonical.appliedControls.state = {
    expression: {
      kind: 'range',
      lower: { value: { kind: 'integer', value: '5' }, inclusive: true },
      upper: { value: { kind: 'integer', value: '10' }, inclusive: true },
    },
    resolvedExpression: {
      kind: 'range',
      lower: { value: { kind: 'integer', value: '5' }, inclusive: true },
      upper: { value: { kind: 'integer', value: '10' }, inclusive: true },
    },
  }
  controller.reconcile(canonical)

  controller.mutate('state', {
    kind: 'range',
    lower: { value: { kind: 'integer', value: '20' }, inclusive: true },
    upper: { value: { kind: 'integer', value: '10' }, inclusive: true },
  })
  expect(controller.projected.appliedControls.state.expression).toMatchObject({
    kind: 'range',
    lower: { value: { value: '20' } },
  })

  expect(controller.reject('invalid-range', canonical)).toBe(true)
  expect(controller.projected.appliedControls.state.expression).toEqual(
    canonical.appliedControls.state.expression,
  )
  expect(controller.pending).toBe(false)
})

test('filter controller ignores rejection acknowledgements for another mutation', () => {
  const controller = new DashboardFilterController(() => {}, () => 'pending-mutation')
  controller.reconcile(state(4))
  controller.mutate('state', setExpression('CA'))

  expect(controller.reject('older-mutation', state(4))).toBe(false)
  expect(controller.projected.appliedControls.state.expression).toEqual(setExpression('CA'))
  expect(controller.pending).toBe(true)
})

test('filter controller projects binding and scope resets to compiled defaults', () => {
  const sent: DashboardFilterCommand[] = []
  const controller = new DashboardFilterController(command => sent.push(command), () => 'reset-mutation')
  controller.setDefaults({
    state: setExpression('SP'),
    category: unfiltered,
  })
  const current = state(9)
  current.appliedControls.state = {
    expression: setExpression('CA'),
    resolvedExpression: setExpression('CA'),
  }
  current.appliedControls.category = {
    expression: setExpression('books'),
    resolvedExpression: setExpression('books'),
  }
  controller.reconcile(current)

  controller.resetBinding('state')
  expect(controller.projected.appliedControls.state.expression).toEqual(setExpression('SP'))
  expect(sent[0]).toMatchObject({
    kind: 'mutate',
    operation: 'reset_binding',
    bindingKey: 'state',
    baseRevision: 9,
  })

  controller.reconcile({
    ...current,
    revision: 10,
  })
  controller.reset('page', ['category', 'state'])
  expect(controller.projected.appliedControls.state.expression).toEqual(setExpression('SP'))
  expect(controller.projected.appliedControls.category.expression).toEqual(unfiltered)
  expect(sent[1]).toMatchObject({
    kind: 'reset',
    resetScope: 'page',
    bindingKeys: ['category', 'state'],
    baseRevision: 10,
  })
})

test('filter controller keeps deferred resets in the draft until apply', () => {
  const controller = new DashboardFilterController(() => {}, () => 'reset-draft')
  controller.setApplicationMode('deferred')
  controller.setDefaults({ state: setExpression('SP') })
  const current = state(3)
  current.appliedControls.state = {
    expression: setExpression('CA'),
    resolvedExpression: setExpression('CA'),
  }
  controller.reconcile(current)

  controller.resetBinding('state')

  expect(controller.projected.appliedControls.state.expression).toEqual(setExpression('CA'))
  expect(controller.projected.draftControls.state).toEqual(setExpression('SP'))
  expect(controller.projected.dirtyBindings).toEqual(['state'])
})
