import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { ChevronRight, Code2, Database, Eye, Search, Server, Table2 } from 'lucide'
import type {
  DataExplorerCommand,
  DataExplorerObjectSignal,
  DataExplorerPageSignal,
  DataExplorerSignal,
  DataPreviewSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { lucideIcon } from '../shared/lucide-icons'
import './preview-table'

const emptyPreview: DataPreviewSignal = {
  columns: [],
  totalRows: 0,
  availableRows: 0,
  chunkSize: 100,
  rowHeight: 34,
  resetVersion: 0,
  blocks: {},
  totalRowLabel: 'Unknown',
  sort: {},
  sql: '',
  error: '',
}

const emptyExplorer: DataExplorerSignal = {
  objects: [],
  selectedKey: '',
  selectedObject: undefined,
  selectedWorkspaceId: '',
  preview: emptyPreview,
  command: { workspaceId: '', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
  warnings: [],
}

type LayerGroup = {
  id: string
  label: string
  objects: DataExplorerObjectSignal[]
}

type WorkspaceGroup = {
  id: string
  title: string
  objects: DataExplorerObjectSignal[]
  layers: LayerGroup[]
}

class DataExplorerPage extends DatastarLit(LitElement) {
  @state() private search = ''
  @state() private showSQL = false
  private lastSelectedKey = ''

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
    }

    .route {
      display: grid;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
    }

    .header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-12);
      align-items: center;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12) var(--base-size-16);
      background: var(--lv-bg-app);
    }

    h1,
    h2,
    h3,
    p {
      margin: 0;
    }

    h1 {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
      line-height: var(--base-text-lineHeight-tight);
    }

    .detail {
      margin-top: var(--base-size-4);
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
    }

    .explorer {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: minmax(18rem, 22rem) minmax(0, 1fr);
      overflow: hidden;
    }

    .browser,
    .main {
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .browser {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      border-right: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .search {
      position: relative;
      padding: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
    }

    .search input {
      width: 100%;
      min-width: 0;
      height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8) 0 var(--base-size-32);
      font: var(--lv-type-body);
    }

    .search-icon {
      position: absolute;
      left: var(--base-size-20);
      top: 50%;
      display: grid;
      color: var(--lv-fg-muted);
      transform: translateY(-50%);
    }

    .tree {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-8);
    }

    details {
      min-width: 0;
    }

    summary {
      display: grid;
      grid-template-columns: 1rem 1rem minmax(0, 1fr) auto;
      gap: var(--base-size-6);
      align-items: center;
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-6) var(--base-size-8);
      color: var(--lv-fg-muted);
      cursor: pointer;
      list-style: none;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    summary::-webkit-details-marker {
      display: none;
    }

    details[open] > summary .chevron {
      transform: rotate(90deg);
    }

    summary em {
      color: var(--lv-fg-subtle);
      font-style: normal;
    }

    .object-list {
      display: grid;
      gap: var(--base-size-2);
      padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-16);
    }

    .object-button {
      display: grid;
      min-width: 0;
      width: 100%;
      grid-template-columns: 1rem minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      align-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      padding: var(--base-size-8);
      text-align: left;
      cursor: pointer;
      font: inherit;
    }

    .object-button:hover,
    .object-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .object-button.is-selected {
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .object-button strong,
    .object-button small {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .object-button strong {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .object-button small {
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .object-button span:last-child {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .main {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .selected-header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-12);
      align-items: center;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12) var(--base-size-16);
    }

    .selected-title {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .selected-title h2 {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
      line-height: var(--base-text-lineHeight-tight);
    }

    .badges {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-6);
      margin-top: var(--base-size-6);
    }

    .badge {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-8);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
    }

    .icon-button:hover,
    .icon-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .content {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr) auto;
      overflow: hidden;
    }

    lv-data-preview-table {
      min-height: 0;
    }

    .sql-panel {
      max-height: 14rem;
      overflow: auto;
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12) var(--base-size-16);
    }

    pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      color: var(--lv-fg-muted);
      font: var(--lv-type-code-block);
    }

    .empty {
      color: var(--lv-fg-muted);
      padding: var(--base-size-16);
      font: var(--lv-type-body);
    }

    @media (max-width: 760px) {
      .route {
        height: auto;
        min-height: 100svh;
        overflow: visible;
      }

      .explorer {
        grid-template-columns: 1fr;
      }

      .browser,
      .main {
        min-height: 22rem;
      }
    }
  `

  updated(): void {
    const selectedKey = `${this.dataExplorer.selectedWorkspaceId}:${this.dataExplorer.selectedKey}`
    if (selectedKey !== this.lastSelectedKey) {
      this.lastSelectedKey = selectedKey
      this.showSQL = false
    }
  }

  get page(): DataExplorerPageSignal | null {
    return this.signal<DataExplorerPageSignal | null>('page', null)
  }

  get dataExplorer(): DataExplorerSignal {
    return this.signal<DataExplorerSignal>('dataExplorer', emptyExplorer)
  }

  render() {
    const page = this.page
    const explorer = this.dataExplorer ?? emptyExplorer
    const selected = explorer.selectedObject
    const filtered = filterObjects(explorer.objects ?? [], this.search)
    const grouped = groupObjectsByWorkspace(filtered, page)
    return html`
      <section class="route" aria-label="Data Explorer">
        <header class="header">
          <div>
            <h1>${page?.title ?? 'Data Explorer'}</h1>
            ${page?.description ? html`<p class="detail">${page.description}</p>` : nothing}
          </div>
        </header>
        <div class="explorer">
          <aside class="browser" aria-label="Data objects">
            <label class="search">
              <span class="search-icon" aria-hidden="true">${lucideIcon(Search, { size: 15 })}</span>
              <input
                type="search"
                .value=${this.search}
                @input=${(event: Event) => this.search = (event.target as HTMLInputElement).value}
                placeholder="Search data"
                autocomplete="off"
              />
            </label>
            <div class="tree">
              ${filtered.length
                ? this.renderWorkspaceGroups(grouped, explorer.selectedWorkspaceId ?? '', explorer.selectedKey ?? '')
                : html`<p class="empty">No data objects match this search.</p>`}
            </div>
          </aside>
          <main class="main" aria-label="Data preview">
            ${selected ? this.renderSelected(selected, explorer.preview ?? emptyPreview, explorer.command ?? emptyExplorer.command) : html`<p class="empty">No data objects are available.</p>`}
          </main>
        </div>
      </section>
    `
  }

  private renderWorkspaceGroups(groups: WorkspaceGroup[], selectedWorkspaceId: string, selectedKey: string) {
    return groups.map((workspace) => html`
      <details open class="workspace-group">
        <summary>
          <span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
          <span aria-hidden="true">${lucideIcon(Database, { size: 14 })}</span>
          <span>${label(workspace.title)}</span>
          <em>${workspace.objects.length}</em>
        </summary>
        <div class="workspace-layers">
          ${this.renderLayerGroups(workspace.layers, selectedWorkspaceId, selectedKey)}
        </div>
      </details>
    `)
  }

  private renderLayerGroups(groups: LayerGroup[], selectedWorkspaceId: string, selectedKey: string) {
    return groups.map((group) => html`
      <details open>
        <summary>
          <span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
          <span aria-hidden="true">${lucideIcon(iconForLayer(group.id), { size: 14 })}</span>
          <span>${group.label}</span>
          <em>${group.objects.length}</em>
        </summary>
        <div class="object-list">
          ${group.objects.map((object) => {
            const selected = object.workspaceId === selectedWorkspaceId && object.key === selectedKey
            return html`
              <button
                type="button"
                class=${selected ? 'object-button is-selected' : 'object-button'}
                @click=${() => this.emitCommand({ workspaceId: object.workspaceId, objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: (this.dataExplorer?.command?.resetVersion ?? 0) + 1 })}
              >
                <span aria-hidden="true">${lucideIcon(iconForLayer(object.layer), { size: 14 })}</span>
                <span>
                  <strong>${label(object.title)}</strong>
                  <small>${label(object.modelId)}${object.table || object.source ? ` · ${object.table || object.source}` : ''}</small>
                </span>
                <span>${object.columnCount || 0}</span>
              </button>
            `
          })}
        </div>
      </details>
    `)
  }

  private renderSelected(object: DataExplorerObjectSignal, preview: DataPreviewSignal, command: DataExplorerCommand) {
    return html`
      <header class="selected-header">
        <div>
          <div class="selected-title">
            <span aria-hidden="true">${lucideIcon(iconForLayer(object.layer), { size: 18 })}</span>
            <h2>${label(object.title)}</h2>
          </div>
          <div class="badges">
            <span class="badge">${layerLabel(object.layer)}</span>
            <span class="badge">${label(object.workspaceTitle || object.workspaceId)}</span>
            <span class="badge">${label(object.modelId)}</span>
            <span class="badge">${object.columnCount || preview.columns?.length || 0} columns</span>
            <span class="badge">${label(preview.totalRowLabel || object.rowCountLabel)} rows</span>
          </div>
        </div>
        <button type="button" class="icon-button" title="Toggle SQL" aria-label="Toggle SQL" @click=${() => this.showSQL = !this.showSQL}>
          ${lucideIcon(Code2, { size: 16 })}
        </button>
      </header>
      <div class="content">
        <lv-data-preview-table
          .preview=${preview}
          .command=${command}
          @lv-data-preview-table-command=${(event: CustomEvent<Partial<DataExplorerCommand>>) => this.emitCommand(event.detail)}
        ></lv-data-preview-table>
        ${this.showSQL ? html`<section class="sql-panel" aria-label="Generated SQL"><pre>${preview.sql || 'No SQL is available for this preview.'}</pre></section>` : nothing}
      </div>
    `
  }

  private emitCommand(partial: Partial<DataExplorerCommand>) {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    const next: DataExplorerCommand = {
      workspaceId: partial.workspaceId ?? current.workspaceId ?? this.dataExplorer?.selectedWorkspaceId ?? this.dataExplorer?.selectedObject?.workspaceId ?? '',
      objectKey: partial.objectKey ?? current.objectKey ?? this.dataExplorer?.selectedKey ?? '',
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
    if (partial.workspaceId !== undefined || partial.objectKey !== undefined) {
      replaceDataExplorerURL(next)
    }
    this.dispatchEvent(new CustomEvent('lv-data-explorer-command', { bubbles: true, composed: true, detail: next }))
  }
}

function filterObjects(objects: DataExplorerObjectSignal[], query: string): DataExplorerObjectSignal[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return objects
  return objects.filter((object) => [
    object.title,
    object.description,
    object.layer,
    object.workspaceId,
    object.workspaceTitle,
    object.modelId,
    object.table,
    object.source,
  ].some((value) => String(value ?? '').toLowerCase().includes(normalized)))
}

function groupObjectsByWorkspace(objects: DataExplorerObjectSignal[], page: DataExplorerPageSignal | null): WorkspaceGroup[] {
  const workspaces = new Map<string, WorkspaceGroup>()
  for (const workspace of page?.workspaces ?? []) {
    workspaces.set(workspace.id, { id: workspace.id, title: workspace.title, objects: [], layers: [] })
  }
  for (const object of objects) {
    const id = object.workspaceId || ''
    if (!workspaces.has(id)) {
      workspaces.set(id, { id, title: object.workspaceTitle || id || 'Workspace', objects: [], layers: [] })
    }
    workspaces.get(id)!.objects.push(object)
  }
  const groups = Array.from(workspaces.values()).filter((workspace) => workspace.objects.length > 0)
  for (const workspace of groups) {
    workspace.layers = groupObjectsByLayer(workspace.objects)
  }
  return groups
}

function groupObjectsByLayer(objects: DataExplorerObjectSignal[]): LayerGroup[] {
  const groups: LayerGroup[] = [
    { id: 'source', label: 'Sources', objects: [] },
    { id: 'model_table', label: 'Model tables', objects: [] },
    { id: 'semantic_view', label: 'Semantic views', objects: [] },
  ]
  for (const object of objects) {
    const group = groups.find((candidate) => candidate.id === object.layer)
    if (group) group.objects.push(object)
  }
  return groups.filter((group) => group.objects.length > 0)
}

function replaceDataExplorerURL(command: DataExplorerCommand) {
  if (typeof window === 'undefined') return
  const workspaceId = command.workspaceId || ''
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (workspaceId) params.set('workspace', workspaceId)
  if (objectKey) params.set('object', objectKey)
  const next = params.toString() ? `/data?${params.toString()}` : '/data'
  if (window.location.pathname + window.location.search !== next) {
    window.history.replaceState({}, '', next)
  }
}

function iconForLayer(layer: string): any {
  switch (layer) {
    case 'source':
      return Server
    case 'semantic_view':
      return Eye
    case 'model_table':
      return Table2
    default:
      return Database
  }
}

function layerLabel(layer: string): string {
  switch (layer) {
    case 'source':
      return 'Source'
    case 'model_table':
      return 'Model table'
    case 'semantic_view':
      return 'Semantic view'
    default:
      return label(layer)
  }
}

function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}

if (!customElements.get('lv-data-explorer')) customElements.define('lv-data-explorer', DataExplorerPage)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer': DataExplorerPage
  }
}
