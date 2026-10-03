import type {
  DataExploreCommand,
  DataExploreFieldSignal,
  DataExplorerCommand,
  DataExplorerObjectSignal,
} from '../../generated/signals'

const dataExplorerAgentStorageKey = 'leapview-data-explorer-agent-state'

export type DataExplorerAgentStoredState = { open: boolean; conversationId: string }

export function readDataExplorerAgentState(storage: Storage | undefined = typeof localStorage === 'undefined' ? undefined : localStorage): DataExplorerAgentStoredState {
  if (!storage) return { open: false, conversationId: '' }
  try {
    const value = JSON.parse(storage.getItem(dataExplorerAgentStorageKey) ?? '') as Partial<DataExplorerAgentStoredState>
    return {
      open: value.open === true,
      conversationId: typeof value.conversationId === 'string' ? value.conversationId.trim() : '',
    }
  } catch {
    return { open: false, conversationId: '' }
  }
}

export class DataExplorerAgentStateController {
  private stateValue: DataExplorerAgentStoredState = { open: false, conversationId: '' }
  private initialized = false

  constructor(private readonly storage?: Storage) {}

  initialize(): DataExplorerAgentStoredState {
    if (!this.initialized) {
      this.stateValue = readDataExplorerAgentState(this.storage ?? (typeof localStorage === 'undefined' ? undefined : localStorage))
      this.initialized = true
    }
    return this.state
  }

  get state(): DataExplorerAgentStoredState {
    return { ...this.stateValue }
  }

  setOpen(open: boolean): DataExplorerAgentStoredState {
    this.stateValue.open = open
    return this.persist()
  }

  newConversation(): DataExplorerAgentStoredState {
    this.stateValue.conversationId = ''
    return this.persist()
  }

  syncConversation(activeConversationId: string | undefined): DataExplorerAgentStoredState {
    const conversationId = activeConversationId?.trim() ?? ''
    if (conversationId) {
      this.stateValue.conversationId = conversationId
      this.persist()
    }
    return this.state
  }

  private persist(): DataExplorerAgentStoredState {
    try {
      const storage = this.storage ?? (typeof localStorage === 'undefined' ? undefined : localStorage)
      storage?.setItem(dataExplorerAgentStorageKey, JSON.stringify(this.stateValue))
    } catch {
      // The drawer remains usable when local storage is unavailable.
    }
    return this.state
  }
}

export type ExplorerPanelSnapshot = {
  browserCollapsed: boolean
  browserWidth: number
  filterField: string
  filterOperator: string
  filterValue: string
}

/** Browser/filter panel state independent of Lit's rendering lifecycle. */
export class DataExplorerPanelController {
  private snapshotValue: ExplorerPanelSnapshot = {
    browserCollapsed: false,
    browserWidth: 320,
    filterField: '',
    filterOperator: 'equals',
    filterValue: '',
  }

  get state(): ExplorerPanelSnapshot {
    return { ...this.snapshotValue }
  }

  toggleBrowser(): ExplorerPanelSnapshot {
    this.snapshotValue.browserCollapsed = !this.snapshotValue.browserCollapsed
    return this.state
  }

  setBrowserWidth(width: number): ExplorerPanelSnapshot {
    this.snapshotValue.browserWidth = clampBrowserWidth(width)
    return this.state
  }

  openFilter(field: string): ExplorerPanelSnapshot {
    this.snapshotValue.filterField = field
    this.snapshotValue.filterOperator = 'equals'
    this.snapshotValue.filterValue = ''
    return this.state
  }

  setFilterOperator(operator: string): ExplorerPanelSnapshot {
    this.snapshotValue.filterOperator = operator
    return this.state
  }

  setFilterValue(value: string): ExplorerPanelSnapshot {
    this.snapshotValue.filterValue = value
    return this.state
  }

  closeFilter(): ExplorerPanelSnapshot {
    this.snapshotValue.filterField = ''
    this.snapshotValue.filterOperator = 'equals'
    this.snapshotValue.filterValue = ''
    return this.state
  }
}

export class DataExplorerSelectionController {
  private lastSelectedKey = ''

  observe(selectedKey: string): boolean {
    if (selectedKey === this.lastSelectedKey) return false
    this.lastSelectedKey = selectedKey
    return true
  }

  reset(): void {
    this.lastSelectedKey = ''
  }
}

/**
 * Normalises query commands and gives every user edit a monotonic request and
 * reset sequence. The server can therefore safely ignore stale responses.
 */
export class DataExplorerQueryController {
  explore(current: DataExploreCommand, next: Partial<DataExploreCommand>, requestNow = false): DataExploreCommand {
    const command: DataExploreCommand = {
      ...current,
      ...next,
      semanticModelId: next.semanticModelId ?? current.semanticModelId ?? '',
      datasetId: next.datasetId ?? current.datasetId ?? '',
      dimensions: [...(next.dimensions ?? current.dimensions ?? [])],
      metrics: [...(next.metrics ?? current.metrics ?? [])],
      filters: [...(next.filters ?? current.filters ?? [])],
      sort: [...(next.sort ?? current.sort ?? [])],
      limit: next.limit || current.limit || 100,
      requestSeq: Math.max(current.requestSeq ?? 0, next.requestSeq ?? 0) + 1,
      resetVersion: Math.max(current.resetVersion ?? 0, next.resetVersion ?? 0) + 1,
      columnWidths: next.columnWidths ?? current.columnWidths ?? {},
    }
    // The flag is intentionally accepted for call-site readability. Debounce
    // scheduling belongs to the route because it owns its lifecycle timer.
    void requestNow
    return command
  }

  command(current: DataExplorerCommand, partial: Partial<DataExplorerCommand>): DataExplorerCommand {
    return {
      mode: partial.mode ?? current.mode ?? 'browse',
      explore: partial.explore ?? current.explore,
      objectKey: partial.objectKey ?? current.objectKey ?? '',
      offset: partial.offset ?? current.offset ?? 0,
      limit: partial.limit ?? current.limit ?? 100,
      block: partial.block ?? current.block ?? 'all',
      start: partial.start ?? partial.offset ?? current.start ?? current.offset ?? 0,
      count: partial.count ?? partial.limit ?? current.count ?? current.limit ?? 100,
      requestSeq: partial.requestSeq ?? current.requestSeq ?? 0,
      resetVersion: partial.resetVersion ?? current.resetVersion ?? 0,
      sort: partial.sort ?? current.sort ?? {},
      visibleColumns: partial.visibleColumns ?? current.visibleColumns ?? [],
      columnWidths: partial.columnWidths ?? current.columnWidths ?? {},
    }
  }
}

export function clampBrowserWidth(value: number): number {
  return Math.min(440, Math.max(280, Math.round(Number.isFinite(value) ? value : 320)))
}

export function toggleVisibleColumns(columns: string[], key: string, checked: boolean, allKeys: string[]): string[] {
  const visible = columns.length ? columns.filter((candidate) => allKeys.includes(candidate)) : [...allKeys]
  const next = checked
    ? allKeys.filter((candidate) => candidate === key || visible.includes(candidate))
    : visible.filter((candidate) => candidate !== key)
  return next.length === allKeys.length ? [] : next
}

export function objectDatasetID(object: DataExplorerObjectSignal): string {
  return object.datasetId?.trim() || object.title.trim()
}

export function fieldColumnID(field: DataExploreFieldSignal): string {
  const parts = field.id.split('.')
  return parts[parts.length - 1] || field.id
}

export function exploreContextMatchesObject(command: DataExploreCommand, object: DataExplorerObjectSignal): boolean {
  return command.semanticModelId === (object.semanticModelId ?? '')
}
