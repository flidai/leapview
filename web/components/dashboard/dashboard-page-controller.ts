import type {
  DashboardFilterState,
  DashboardPageSignal,
} from '../../generated/signals'
import type { VisualizationSpatialSelectionState } from '../../generated/visualization'
import type { CanonicalInteractionSelection } from './interaction-selection'

export type DashboardAgentStoredState = {
  open: boolean
  conversationId: string
}

const emptyAgentState: DashboardAgentStoredState = { open: false, conversationId: '' }

/**
 * Small storage adapter used by the dashboard shell. Keeping storage access
 * here makes the Lit element usable in SSR/tests where localStorage is absent.
 */
export function readDashboardAgentState(storage: Storage | undefined = typeof localStorage === 'undefined' ? undefined : localStorage): DashboardAgentStoredState {
  if (!storage) return { ...emptyAgentState }
  try {
    const value = JSON.parse(storage.getItem('leapview-dashboard-agent-state') ?? '') as Partial<DashboardAgentStoredState>
    return {
      open: value.open === true,
      conversationId: typeof value.conversationId === 'string' ? value.conversationId.trim() : '',
    }
  } catch {
    return { ...emptyAgentState }
  }
}

export function writeDashboardAgentState(
  state: DashboardAgentStoredState,
  storage: Storage | undefined = typeof localStorage === 'undefined' ? undefined : localStorage,
): void {
  if (!storage) return
  try {
    storage.setItem('leapview-dashboard-agent-state', JSON.stringify(state))
  } catch {
    // Storage can be unavailable in privacy-constrained browser contexts.
  }
}

export class DashboardAgentStateController {
  private initialized = false
  private restoredConversationId = ''
  private persisted: DashboardAgentStoredState = { ...emptyAgentState }

  constructor(private readonly storage?: Storage) {}

  initialize(enabled: boolean): DashboardAgentStoredState {
    if (!enabled || this.initialized) return this.state
    const stored = readDashboardAgentState(this.storage ?? (typeof localStorage === 'undefined' ? undefined : localStorage))
    this.restoredConversationId = stored.conversationId
    this.persisted = stored
    this.initialized = true
    return this.state
  }

  get state(): DashboardAgentStoredState {
    return { open: this.persisted.open, conversationId: this.restoredConversationId }
  }

  setOpen(open: boolean): DashboardAgentStoredState {
    this.persisted.open = open
    this.persist()
    return this.state
  }

  newConversation(): DashboardAgentStoredState {
    this.restoredConversationId = ''
    this.persist()
    return this.state
  }

  syncConversation(activeConversationId: string | undefined): DashboardAgentStoredState {
    const conversationId = activeConversationId?.trim() ?? ''
    if (conversationId) {
      this.restoredConversationId = conversationId
      this.persist()
    }
    return this.state
  }

  get restoreConversationId(): string {
    return this.restoredConversationId
  }

  private persist(): void {
    const next = this.state
    writeDashboardAgentState(next, this.storage ?? (typeof localStorage === 'undefined' ? undefined : localStorage))
    this.persisted = { open: next.open, conversationId: next.conversationId }
  }
}

export type DashboardNavigationRequest = {
  href: string
  pageId: string
}

export type DashboardNavigationDecision = 'navigate' | 'apply' | 'discard' | 'cancel'

/** State machine for page navigation while deferred filters are dirty. */
export class DashboardNavigationController {
  private pending: DashboardNavigationRequest | null = null
  private requested = false

  get request(): DashboardNavigationRequest | null {
    return this.pending ? { ...this.pending } : null
  }

  get navigationRequested(): boolean {
    return this.requested
  }

  begin(request: DashboardNavigationRequest, options: {
    active: boolean
    deferred: boolean
    dirty: boolean
    confirm: (message: string) => boolean
  }): DashboardNavigationDecision {
    if (options.active) return 'cancel'
    this.pending = { ...request }
    this.requested = false
    if (!options.deferred || !options.dirty) return 'navigate'
    if (options.confirm('Apply pending filter changes before leaving this page?')) return 'apply'
    if (options.confirm('Discard pending filter changes and leave this page?')) return 'discard'
    this.clear()
    return 'cancel'
  }

  markRequested(): DashboardNavigationRequest | null {
    if (!this.pending || this.requested) return null
    this.requested = true
    return { ...this.pending }
  }

  complete(pageId: string): DashboardNavigationRequest | null {
    if (!this.pending || this.pending.pageId !== pageId) return null
    const request = { ...this.pending }
    this.clear()
    return request
  }

  clear(): void {
    this.pending = null
    this.requested = false
  }

  canDispatch(deferred: boolean, dirty: boolean, filterPending: boolean): boolean {
    return Boolean(this.pending && (!deferred || !dirty) && !filterPending && !this.requested)
  }
}

export type DashboardOptimisticSnapshot = {
  selections: CanonicalInteractionSelection[] | null
  spatialSelections: VisualizationSpatialSelectionState[] | null
  expectedGeneration: number
}

/**
 * Timer-backed optimistic interaction state. Validation and command shaping
 * remain in the route because they require decoded visual configuration; this
 * controller owns only lifecycle and rollback semantics.
 */
export class DashboardOptimisticInteractionController {
  private snapshot: DashboardOptimisticSnapshot = {
    selections: null,
    spatialSelections: null,
    expectedGeneration: 0,
  }
  private rollbackTimer?: ReturnType<typeof setTimeout>

  constructor(
    private readonly onChange?: (snapshot: DashboardOptimisticSnapshot) => void,
    private readonly currentGeneration: () => number = () => 0,
  ) {}

  get state(): DashboardOptimisticSnapshot {
    return {
      selections: this.snapshot.selections ? [...this.snapshot.selections] : null,
      spatialSelections: this.snapshot.spatialSelections ? [...this.snapshot.spatialSelections] : null,
      expectedGeneration: this.snapshot.expectedGeneration,
    }
  }

  setSelections(selections: CanonicalInteractionSelection[], generation: number): void {
    this.snapshot.selections = [...selections]
    this.snapshot.expectedGeneration = Math.max(generation + 1, this.snapshot.expectedGeneration + 1)
    this.scheduleRollback()
    this.notify()
  }

  setSpatialSelections(selections: VisualizationSpatialSelectionState[], generation: number): void {
    this.snapshot.spatialSelections = [...selections]
    this.snapshot.expectedGeneration = Math.max(generation + 1, this.snapshot.expectedGeneration + 1)
    this.scheduleRollback()
    this.notify()
  }

  reconcile(generation: number): boolean {
    if (!this.snapshot.selections && !this.snapshot.spatialSelections) return false
    if (generation < this.snapshot.expectedGeneration) return false
    this.clear(generation)
    return true
  }

  clear(generation = this.snapshot.expectedGeneration): void {
    this.clearRollbackTimer()
    this.snapshot = { selections: null, spatialSelections: null, expectedGeneration: generation }
    this.notify()
  }

  dispose(): void {
    this.clearRollbackTimer()
  }

  rollback(): void {
    this.clear(this.currentGeneration())
  }

  private scheduleRollback(): void {
    this.clearRollbackTimer()
    this.rollbackTimer = setTimeout(() => this.rollback(), 10_000)
  }

  private clearRollbackTimer(): void {
    if (this.rollbackTimer !== undefined) clearTimeout(this.rollbackTimer)
    this.rollbackTimer = undefined
  }

  private notify(): void {
    this.onChange?.(this.state)
  }
}

export function pendingDashboardPageNavigation(page: DashboardPageSignal | null, navigation: DashboardNavigationController): string {
  const request = navigation.request
  if (!request || !page || page.pageId !== request.pageId) return ''
  return request.href
}

export function dashboardNavigationFilterState(state: DashboardFilterState): { dirty: boolean; revision: number } {
  return { dirty: state.dirtyBindings.length > 0, revision: state.revision }
}
