import { LitElement, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronRight, Database, Search, Server, Table2, Waves } from 'lucide'
import type { AdminStorageSignal, AdminStorageTableSignal, RecordTableSignal } from '../../generated/signals'
import { jsonAttribute } from '../shared/json-attribute'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/record-table'

const emptyStorage: AdminStorageSignal = {
  summary: {
    catalogPath: '',
    dataPath: '',
    catalogSizeLabel: '',
    dataSizeLabel: '',
    totalSizeLabel: '',
    totalDataSizeLabel: '',
    databaseCount: 0,
    tableCount: 0,
    snapshotCount: 0,
    dataFileCount: 0,
  },
  status: '',
  warnings: [],
  tables: [],
  snapshots: [],
  servingStates: [],
  selectedKey: '',
  selectedTable: undefined,
}

type SchemaGroup = {
  schema: string
  tables: AdminStorageTableSignal[]
}

type DatabaseGroup = {
  id: string
  name: string
  model: string
  schemas: SchemaGroup[]
}

type SchemaSelection = {
  databaseId: string
  schema: string
}

type DatabaseSelection = {
  databaseId: string
}

type TableDetailTab = 'schema' | 'files' | 'history'
type CatalogDetailTab = 'schemas' | 'servingStates' | 'snapshots'

class StorageExplorer extends LitElement {
  @property({ converter: jsonAttribute<AdminStorageSignal>(emptyStorage) }) storage: AdminStorageSignal = emptyStorage
  @state() private search = ''
  @state() private selectedDatabase: DatabaseSelection | null = null
  @state() private selectedSchema: SchemaSelection | null = null
  @state() private localSelectedTable: AdminStorageTableSignal | null = null
  @state() private tableDetailTab: TableDetailTab = 'schema'
  @state() private catalogDetailTab: CatalogDetailTab = 'schemas'

  updated(changedProperties: PropertyValues<this>): void {
    if (!changedProperties.has('storage')) return
    const groups = groupTables(this.resolvedStorage.tables ?? [])
    if (this.selectedDatabase && !this.resolveSelectedDatabase(groups)) {
      this.selectedDatabase = null
    }
    if (this.selectedSchema && !this.resolveSelectedSchema(groups)) {
      this.selectedSchema = null
    }
    if (this.localSelectedTable) {
      this.localSelectedTable = this.findTableByKey(this.localSelectedTable.key)
    }
  }

  render() {
    const storage = this.resolvedStorage
    const tables = storage.tables ?? []
    const filtered = filterTables(tables, this.search)
    const selectedKey = this.selectedDatabase || this.selectedSchema ? '' : this.localSelectedTable?.key ?? storage.selectedKey ?? ''
    const selected = this.selectedDatabase || this.selectedSchema ? null : this.localSelectedTable ?? storage.selectedTable ?? null
    const selectedDatabase = this.resolveSelectedDatabase(groupTables(tables))
    const selectedSchema = this.resolveSelectedSchema(groupTables(tables))

    return html`
      <style>
        ${storageExplorerStyles}
      </style>
      <div class="storage-explorer" @lv-record-table-action=${this.handleRecordTableAction}>
        <div class="storage-explorer-header">
          <div class="storage-heading">
            <span class="storage-logo" aria-hidden="true">${lucideIcon(Database, { size: 18 })}</span>
            <div>
              <h2>Storage</h2>
              <p>DuckLake catalog · <span>${label(storage.summary?.catalogPath)}</span></p>
            </div>
          </div>
          <div class="storage-summary" aria-label="DuckLake storage summary">
            ${this.summaryMetric('Tables', storage.summary?.tableCount)}
            ${this.summaryMetric('Snapshots', storage.summary?.snapshotCount)}
            ${this.summaryMetric('Data files', storage.summary?.dataFileCount)}
            ${this.summaryMetric('Data size', storage.summary?.totalDataSizeLabel)}
          </div>
        </div>
        ${storage.warnings?.length ? html`
          <div class="storage-warnings">
            ${storage.warnings.map((warning) => html`<p class="storage-warning">${warning}</p>`)}
          </div>
        ` : html`<div class="storage-warnings storage-warnings-empty" aria-hidden="true"></div>`}
        <aside class="storage-browser" aria-label="DuckLake table browser">
          <div class="storage-browser-menu">
            <label class="storage-search">
              <span class="storage-search-icon" aria-hidden="true">${lucideIcon(Search, { size: 15 })}</span>
              <input
                type="search"
                .value=${this.search}
                @input=${this.onSearch}
                placeholder="Schema, table, model"
                autocomplete="off"
              />
            </label>
          </div>
          <div class="storage-tree">
            ${storage.status && tables.length === 0
              ? html`<p class="storage-empty">${storage.status}</p>`
              : filtered.length === 0
                ? html`<p class="storage-empty">No matching tables.</p>`
                : this.renderDatabaseGroups(groupTables(filtered), selectedKey)}
          </div>
        </aside>
        <section class="storage-detail" aria-label="Selected DuckLake table details">
          ${selectedDatabase
            ? this.renderSelectedDatabase(selectedDatabase.database)
            : selectedSchema
            ? this.renderSelectedSchema(selectedSchema.database, selectedSchema.schema)
            : selected
              ? this.renderSelectedTable(selected)
              : html`<p class="storage-empty">Select a schema or table to inspect storage metadata.</p>`}
        </section>
      </div>
    `
  }

  private get resolvedStorage(): AdminStorageSignal {
    return this.storage ?? emptyStorage
  }

  private onSearch(event: Event): void {
    this.search = (event.target as HTMLInputElement).value
  }

  private renderDatabaseGroups(groups: DatabaseGroup[], selectedKey: string) {
    return groups.map((database) => html`
      <details class="storage-db" open>
        <summary>
          <span class="storage-chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
          <span class="storage-node-icon" aria-hidden="true">${lucideIcon(Database, { size: 15 })}</span>
          <span>Catalog</span>
        </summary>
        ${database.schemas.map((schema) => html`
          <details class="storage-schema" open>
            <summary
              class=${this.isSelectedSchema(database.id, schema.schema) ? 'is-selected-schema' : ''}
              @click=${(event: Event) => this.selectSchema(event, database.id, schema.schema)}
            >
              <span class="storage-chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
              <span class="storage-node-icon" aria-hidden="true">${lucideIcon(Server, { size: 14 })}</span>
              <span>${label(schema.schema)}</span>
            </summary>
            <div class="storage-table-list">
              ${schema.tables.map((table) => this.renderTableButton(table, selectedKey))}
            </div>
          </details>
        `)}
      </details>
    `)
  }

  private renderTableButton(table: AdminStorageTableSignal, selectedKey: string) {
    const isSelected = table.key === selectedKey
    return html`
      <button
        type="button"
        class=${isSelected ? 'storage-table-button is-selected' : 'storage-table-button'}
        aria-pressed=${isSelected ? 'true' : 'false'}
        @click=${() => this.selectTable(table)}
      >
        <span class=${table.type === 'view' ? 'storage-table-icon storage-table-icon-view' : 'storage-table-icon'} aria-hidden="true">
          ${lucideIcon(table.type === 'view' ? Waves : Table2, { size: 14 })}
        </span>
        <span>
          <strong>${label(table.name)}</strong>
          <small>${label(table.schema)}.${label(table.name)} · ${label(table.rowCountLabel)} rows · ${table.fileCount ?? 0} files</small>
        </span>
        <span class="storage-table-size">${label(table.sizeLabel)}</span>
      </button>
    `
  }

  private summaryMetric(labelText: string, value: unknown) {
    return html`
      <div>
        <span>${labelText}</span>
        <strong>${label(value)}</strong>
      </div>
    `
  }

  private renderSelectedDatabase(database: DatabaseGroup) {
    const tables = database.schemas.flatMap((schema) => schema.tables)
    const storage = this.resolvedStorage
    return html`
      <div class="storage-detail-header">
        <nav aria-label="Selected database location">
          <span class="storage-breadcrumb-current">
            ${lucideIcon(Database, { size: 16 })}
            <strong>DuckLake catalog</strong>
          </span>
        </nav>
      </div>
      <dl class="storage-metrics">
        <div>
          <dt>Catalog path</dt>
          <dd>${label(storage.summary?.catalogPath)}</dd>
        </div>
        <div>
          <dt>Data path</dt>
          <dd>${label(storage.summary?.dataPath)}</dd>
        </div>
        <div>
          <dt>Schemas</dt>
          <dd>${database.schemas.length}</dd>
        </div>
        <div>
          <dt>Data size</dt>
          <dd>${label(storage.summary?.totalDataSizeLabel || sumKnownSizes(tables))}</dd>
        </div>
        <div>
          <dt>Snapshots</dt>
          <dd>${storage.summary?.snapshotCount ?? 0}</dd>
        </div>
        <div>
          <dt>Data files</dt>
          <dd>${storage.summary?.dataFileCount ?? 0}</dd>
        </div>
      </dl>
      <div class="storage-detail-body">
        <div class="storage-tabs" role="tablist" aria-label="Catalog metadata">
          ${this.renderCatalogTabButton('schemas', 'Schemas', database.schemas.length)}
          ${this.renderCatalogTabButton('servingStates', 'Serving states', storage.servingStates?.length ?? 0)}
          ${this.renderCatalogTabButton('snapshots', 'Snapshots', storage.snapshots?.length ?? 0)}
        </div>
        <div class="storage-tab-panel" role="tabpanel">
          ${this.catalogDetailTab === 'servingStates'
            ? html`
              <div class="storage-columns">
                <div class="storage-columns-header">
                  <h3>Active serving states</h3>
                </div>
                <div class="storage-column-table-wrap">
                  <lv-record-table .table=${this.servingStatesTable(storage.servingStates ?? [])}></lv-record-table>
                </div>
              </div>
            `
            : this.catalogDetailTab === 'snapshots'
              ? html`
                <div class="storage-columns">
                  <div class="storage-columns-header">
                    <h3>Snapshots</h3>
                  </div>
                  <div class="storage-column-table-wrap">
                    <lv-record-table .table=${this.snapshotsTable(storage.snapshots ?? [])}></lv-record-table>
                  </div>
                </div>
              `
              : html`
                <div class="storage-columns">
                  <div class="storage-columns-header">
                    <h3>Schemas</h3>
                  </div>
                  <div class="storage-column-table-wrap">
                    <lv-record-table .table=${this.databaseSchemasTable(database)}></lv-record-table>
                  </div>
                </div>
              `}
        </div>
      </div>
    `
  }

  private renderSelectedSchema(database: DatabaseGroup, schema: SchemaGroup) {
    return html`
      <div class="storage-detail-header">
        <nav aria-label="Selected schema location">
          <button type="button" class="storage-breadcrumb-button" data-breadcrumb-kind="database" @click=${() => this.selectDatabase(database.id)}>
            ${label(database.name)}
          </button>
          <span class="storage-breadcrumb-separator" aria-hidden="true">${lucideIcon(ChevronRight, { size: 15 })}</span>
          <span class="storage-breadcrumb-current">
            ${lucideIcon(Server, { size: 16 })}
            <strong>${label(schema.schema)}</strong>
          </span>
        </nav>
      </div>
      <dl class="storage-metrics">
        <div>
          <dt>Catalog</dt>
          <dd>DuckLake</dd>
        </div>
        <div>
          <dt>Tables</dt>
          <dd>${schema.tables.length}</dd>
        </div>
        <div>
          <dt>Rows</dt>
          <dd>${sumKnownRows(schema.tables)}</dd>
        </div>
        <div>
          <dt>Data size</dt>
          <dd>${sumKnownSizes(schema.tables)}</dd>
        </div>
        <div>
          <dt>Data files</dt>
          <dd>${sumKnownFiles(schema.tables)}</dd>
        </div>
      </dl>
      <div class="storage-columns">
        <div class="storage-columns-header">
          <h3>Tables</h3>
        </div>
        <div class="storage-column-table-wrap">
          <lv-record-table .table=${this.schemaTablesTable(schema)}></lv-record-table>
        </div>
      </div>
    `
  }

  private renderSelectedTable(table: AdminStorageTableSignal) {
    const columns = table.columns ?? []
    const files = table.files ?? []
    const history = table.history ?? []
    return html`
      <div class="storage-detail-header">
        <nav aria-label="Selected table location">
          <button type="button" class="storage-breadcrumb-button" data-breadcrumb-kind="database" @click=${() => this.selectDatabase(table.databaseId)}>
            DuckLake catalog
          </button>
          <span class="storage-breadcrumb-separator" aria-hidden="true">${lucideIcon(ChevronRight, { size: 15 })}</span>
          <button type="button" class="storage-breadcrumb-button" data-breadcrumb-kind="schema" @click=${() => this.selectSchemaByID(table.databaseId, table.schema)}>
            ${label(table.schema)}
          </button>
          <span class="storage-breadcrumb-separator" aria-hidden="true">${lucideIcon(ChevronRight, { size: 15 })}</span>
          <span class="storage-breadcrumb-current">
            ${lucideIcon(table.type === 'view' ? Waves : Table2, { size: 16 })}
            <strong>${label(table.name)}</strong>
          </span>
        </nav>
      </div>
      <dl class="storage-metrics">
        <div>
          <dt>Table ID</dt>
          <dd>${table.tableId ?? '-'}</dd>
        </div>
        <div class="storage-metric-uuid">
          <dt>Table UUID</dt>
          <dd>${label(table.tableUuid)}</dd>
        </div>
        <div class="storage-metric-path">
          <dt>DuckLake path</dt>
          <dd>${label(table.duckLakePath)}</dd>
        </div>
        <div>
          <dt>Begin snapshot</dt>
          <dd>${table.beginSnapshot || '-'}</dd>
        </div>
        <div>
          <dt>Rows</dt>
          <dd>${label(table.rowCountLabel)}</dd>
        </div>
        <div>
          <dt>Columns</dt>
          <dd>${table.columnCount ?? columns.length}</dd>
        </div>
        <div>
          <dt>Data files</dt>
          <dd>${table.fileCount ?? files.length}</dd>
        </div>
        <div>
          <dt>Data size</dt>
          <dd>${label(table.sizeLabel)}</dd>
        </div>
      </dl>
      <div class="storage-detail-body">
        <div class="storage-tabs" role="tablist" aria-label="Table metadata">
          ${this.renderTableTabButton('schema', 'Schema', columns.length)}
          ${this.renderTableTabButton('files', 'Data files', files.length)}
          ${this.renderTableTabButton('history', 'History', history.length)}
        </div>
        <div class="storage-tab-panel" role="tabpanel">
          ${this.tableDetailTab === 'files'
            ? html`
              <div class="storage-columns">
                <div class="storage-columns-header">
                  <h3>Data files</h3>
                </div>
                <div class="storage-column-table-wrap">
                  <lv-record-table .table=${this.tableFilesTable(table)}></lv-record-table>
                </div>
              </div>
            `
            : this.tableDetailTab === 'history'
              ? html`
                <div class="storage-columns">
                  <div class="storage-columns-header">
                    <h3>History</h3>
                  </div>
                  <div class="storage-column-table-wrap">
                    <lv-record-table .table=${this.tableHistoryTable(table)}></lv-record-table>
                  </div>
                </div>
              `
              : html`
                <div class="storage-columns">
                  <div class="storage-columns-header">
                    <h3>Schema</h3>
                  </div>
                  ${columns.length === 0
                    ? html`<p class="storage-empty">No column metadata available.</p>`
                    : html`
                      <div class="storage-column-table-wrap">
                        <lv-record-table .table=${this.tableColumnsTable(table)}></lv-record-table>
                      </div>
                    `}
                </div>
              `}
        </div>
      </div>
    `
  }

  private renderTableTabButton(tab: TableDetailTab, labelText: string, count: number) {
    const active = this.tableDetailTab === tab
    return html`
      <button
        type="button"
        role="tab"
        class=${active ? 'storage-tab is-active' : 'storage-tab'}
        aria-selected=${active ? 'true' : 'false'}
        @click=${() => { this.tableDetailTab = tab }}
      >
        <span>${labelText}</span>
        <em>${count.toLocaleString('en-US')}</em>
      </button>
    `
  }

  private renderCatalogTabButton(tab: CatalogDetailTab, labelText: string, count: number) {
    const active = this.catalogDetailTab === tab
    return html`
      <button
        type="button"
        role="tab"
        class=${active ? 'storage-tab is-active' : 'storage-tab'}
        aria-selected=${active ? 'true' : 'false'}
        @click=${() => { this.catalogDetailTab = tab }}
      >
        <span>${labelText}</span>
        <em>${count.toLocaleString('en-US')}</em>
      </button>
    `
  }

  private databaseSchemasTable(database: DatabaseGroup): RecordTableSignal {
    return {
      columns: [
        { id: 'index', header: '#', kind: 'number', align: 'right', width: '64px' },
        { id: 'schema', header: 'Schema', kind: 'button', width: '220px' },
        { id: 'tables', header: 'Tables', kind: 'number', align: 'right', width: '120px' },
        { id: 'rows', header: 'Known rows', align: 'right', width: '150px' },
        { id: 'size', header: 'Known size', align: 'right', width: '150px' },
      ],
      rows: database.schemas.map((schema, index) => ({
        index: index + 1,
        schema: { label: schema.schema, icon: 'schema', action: 'select-schema' },
        tables: schema.tables.length,
        rows: sumKnownRows(schema.tables),
        size: sumKnownSizes(schema.tables),
        databaseId: database.id,
        schemaName: schema.schema,
      })),
      empty: 'No schemas found.',
      minWidth: '700px',
    }
  }

  private schemaTablesTable(schema: SchemaGroup): RecordTableSignal {
    return {
      columns: [
        { id: 'index', header: '#', kind: 'number', align: 'right', width: '64px' },
        { id: 'table', header: 'Table', kind: 'button', width: '240px' },
        { id: 'tableId', header: 'Table ID', kind: 'number', align: 'right', width: '100px' },
        { id: 'rows', header: 'Rows', align: 'right', width: '130px' },
        { id: 'columns', header: 'Columns', kind: 'number', align: 'right', width: '120px' },
        { id: 'files', header: 'Files', kind: 'number', align: 'right', width: '100px' },
        { id: 'size', header: 'Data size', align: 'right', width: '150px' },
        { id: 'snapshot', header: 'Begin snapshot', align: 'right', width: '150px' },
      ],
      rows: schema.tables.map((table, index) => ({
        index: index + 1,
        table: { label: table.name, icon: table.type === 'view' ? 'view' : 'table', action: 'select-table' },
        tableId: table.tableId ?? '-',
        rows: label(table.rowCountLabel),
        columns: table.columnCount ?? '-',
        files: table.fileCount ?? 0,
        size: label(table.sizeLabel),
        snapshot: table.beginSnapshot || '-',
        tableKey: table.key,
      })),
      empty: 'No tables found.',
      minWidth: '980px',
    }
  }

  private tableColumnsTable(table: AdminStorageTableSignal): RecordTableSignal {
    return {
      columns: [
        { id: 'ordinal', header: '#', kind: 'number', align: 'right', width: '64px' },
        { id: 'id', header: 'Column ID', kind: 'number', align: 'right', width: '110px' },
        { id: 'name', header: 'Name', kind: 'code', width: '220px' },
        { id: 'type', header: 'Type', kind: 'code', width: '180px' },
        { id: 'nullable', header: 'Nullable', width: '120px' },
        { id: 'default', header: 'Default', kind: 'code', width: '180px' },
        { id: 'initialDefault', header: 'Initial default', kind: 'code', width: '180px' },
        { id: 'defaultType', header: 'Default type', width: '140px' },
        { id: 'dialect', header: 'Dialect', width: '120px' },
        { id: 'snapshot', header: 'Begin snapshot', align: 'right', width: '150px' },
        { id: 'containsNull', header: 'Nulls', width: '100px' },
        { id: 'containsNan', header: 'NaN', width: '100px' },
        { id: 'min', header: 'Min', kind: 'code', width: '180px' },
        { id: 'max', header: 'Max', kind: 'code', width: '180px' },
        { id: 'extraStats', header: 'Extra stats', kind: 'code', width: '180px' },
      ],
      rows: (table.columns ?? []).map((column) => ({
        ordinal: column.ordinal ?? '',
        id: column.id ?? '-',
        name: label(column.name),
        type: label(column.type),
        nullable: label(column.nullable),
        default: column.default || '-',
        initialDefault: column.initialDefault || '-',
        defaultType: label(column.defaultValueType),
        dialect: label(column.defaultValueDialect),
        snapshot: column.beginSnapshot || '-',
        containsNull: label(column.containsNull),
        containsNan: label(column.containsNan),
        min: label(column.minValue),
        max: label(column.maxValue),
        extraStats: label(column.extraStats),
      })),
      empty: 'No column metadata available.',
      minWidth: '2060px',
    }
  }

  private tableFilesTable(table: AdminStorageTableSignal): RecordTableSignal {
    const files = table.files ?? []
    return {
      columns: [
        { id: 'id', header: 'File ID', kind: 'number', align: 'right', width: '90px' },
        { id: 'path', header: 'Path', kind: 'code', width: '320px' },
        { id: 'format', header: 'Format', width: '100px' },
        { id: 'records', header: 'Rows', align: 'right', width: '130px' },
        { id: 'size', header: 'Size', align: 'right', width: '130px' },
        { id: 'snapshot', header: 'Begin snapshot', align: 'right', width: '150px' },
      ],
      rows: files.map((file) => ({
        id: file.id,
        path: label(file.path),
        format: label(file.format),
        records: label(file.recordCountLabel),
        size: label(file.sizeLabel),
        snapshot: file.beginSnapshot || '-',
      })),
      empty: 'No DuckLake data files recorded for this table.',
      minWidth: '920px',
    }
  }

  private tableHistoryTable(table: AdminStorageTableSignal): RecordTableSignal {
    const history = table.history ?? []
    return {
      columns: [
        { id: 'snapshot', header: 'Snapshot', kind: 'number', align: 'right', width: '110px' },
        { id: 'time', header: 'Time', width: '220px' },
        { id: 'source', header: 'Source', width: '140px' },
        { id: 'changes', header: 'Changes', width: '220px' },
        { id: 'message', header: 'Message', width: '260px' },
        { id: 'author', header: 'Author', width: '160px' },
      ],
      rows: history.map((event) => ({
        snapshot: event.snapshotId,
        time: label(event.time),
        source: label(event.source),
        changes: label(event.changes),
        message: label(event.message || event.extraInfo),
        author: label(event.author),
      })),
      empty: 'No DuckLake snapshot history recorded for this table.',
      minWidth: '1100px',
    }
  }

  private servingStatesTable(servingStates: NonNullable<AdminStorageSignal['servingStates']>): RecordTableSignal {
    return {
      columns: [
        { id: 'workspace', header: 'Workspace', kind: 'code', width: '160px' },
        { id: 'environment', header: 'Environment', width: '130px' },
        { id: 'servingState', header: 'Serving state', kind: 'code', width: '220px' },
        { id: 'status', header: 'Status', width: '120px' },
        { id: 'snapshot', header: 'Snapshot', kind: 'number', align: 'right', width: '120px' },
        { id: 'active', header: 'Active', width: '100px' },
      ],
      rows: servingStates.map((servingState) => ({
        workspace: label(servingState.workspaceId),
        environment: label(servingState.environment),
        servingState: label(servingState.servingStateId),
        status: label(servingState.status),
        snapshot: servingState.snapshotId || '-',
        active: servingState.active ? 'Yes' : 'No',
      })),
      empty: 'No serving states reference this snapshot.',
      minWidth: '860px',
    }
  }

  private snapshotsTable(snapshots: NonNullable<AdminStorageSignal['snapshots']>): RecordTableSignal {
    return {
      columns: [
        { id: 'snapshot', header: 'Snapshot', kind: 'number', align: 'right', width: '110px' },
        { id: 'time', header: 'Time', width: '220px' },
        { id: 'version', header: 'Schema version', kind: 'number', align: 'right', width: '150px' },
        { id: 'protected', header: 'Protected', width: '110px' },
        { id: 'servingStates', header: 'Serving states', kind: 'number', align: 'right', width: '150px' },
        { id: 'message', header: 'Message', width: '260px' },
      ],
      rows: snapshots.map((snapshot) => ({
        snapshot: snapshot.id,
        time: label(snapshot.time),
        version: snapshot.schemaVersion,
        protected: snapshot.protected ? 'Yes' : 'No',
        servingStates: snapshot.servingStateCount,
        message: label(snapshot.message || snapshot.author || snapshot.changes),
      })),
      empty: 'No DuckLake snapshots recorded.',
      minWidth: '980px',
    }
  }

  private handleRecordTableAction(event: CustomEvent): void {
    const action = String(event.detail?.action ?? '')
    const row = event.detail?.row ?? {}
    if (action === 'select-schema') {
      this.selectSchemaByID(String(row.databaseId ?? ''), String(row.schemaName ?? ''))
      return
    }
    if (action === 'select-table') {
      const table = this.findTableByKey(String(row.tableKey ?? ''))
      if (table) this.selectTable(table)
    }
  }

  private selectTable(table: AdminStorageTableSignal): void {
    this.selectedDatabase = null
    this.selectedSchema = null
    this.localSelectedTable = table
    this.tableDetailTab = 'schema'
    this.dispatchEvent(new CustomEvent('lv-storage-table-select', {
      bubbles: true,
      composed: true,
      detail: {
        databaseId: table.databaseId ?? '',
        schema: table.schema ?? '',
        table: table.name ?? '',
      },
    }))
  }

  private selectDatabase(databaseId: string): void {
    this.selectedDatabase = { databaseId }
    this.selectedSchema = null
    this.localSelectedTable = null
    this.catalogDetailTab = 'schemas'
  }

  private selectSchema(event: Event, databaseId: string, schema: string): void {
    if ((event.target as HTMLElement).closest('.storage-chevron')) {
      return
    }
    event.preventDefault()
    this.selectSchemaByID(databaseId, schema)
  }

  private selectSchemaByID(databaseId: string, schema: string): void {
    this.selectedDatabase = null
    this.selectedSchema = { databaseId, schema }
    this.localSelectedTable = null
  }

  private isSelectedSchema(databaseId: string, schema: string): boolean {
    return this.selectedSchema?.databaseId === databaseId && this.selectedSchema.schema === schema
  }

  private resolveSelectedDatabase(groups: DatabaseGroup[]): { database: DatabaseGroup } | null {
    const selection = this.selectedDatabase
    if (!selection) return null
    const database = groups.find((group) => group.id === selection.databaseId)
    if (!database) return null
    return { database }
  }

  private resolveSelectedSchema(groups: DatabaseGroup[]): { database: DatabaseGroup; schema: SchemaGroup } | null {
    const selection = this.selectedSchema
    if (!selection) return null
    const database = groups.find((group) => group.id === selection.databaseId)
    const schema = database?.schemas.find((item) => item.schema === selection.schema)
    if (!database || !schema) return null
    return { database, schema }
  }

  private findTableByKey(key: string | undefined): AdminStorageTableSignal | null {
    if (!key) return null
    return this.resolvedStorage.tables?.find((table) => table.key === key) ?? null
  }
}

function filterTables(tables: AdminStorageTableSignal[], query: string): AdminStorageTableSignal[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return tables
  return tables.filter((table) => [
    table.databaseName,
    table.modelName,
    table.schema,
    table.name,
    table.type,
    table.tableId,
    table.tableUuid,
    table.beginSnapshot,
    table.rowCountLabel,
    table.sizeLabel,
    ...(table.files ?? []).flatMap((file) => [file.id, file.path, file.format, file.sizeLabel, file.recordCountLabel]),
    ...(table.history ?? []).flatMap((event) => [event.snapshotId, event.time, event.source, event.changes, event.author, event.message, event.extraInfo]),
  ].some((value) => String(value ?? '').toLowerCase().includes(normalized)))
}

function groupTables(tables: AdminStorageTableSignal[]): DatabaseGroup[] {
  const databases = new Map<string, DatabaseGroup>()
  for (const table of tables) {
    const id = table.databaseId || table.databaseName || 'database'
    let database = databases.get(id)
    if (!database) {
      database = {
        id,
        name: table.databaseName || id,
        model: table.modelName || table.modelId || '-',
        schemas: [],
      }
      databases.set(id, database)
    }
    let schema = database.schemas.find((item) => item.schema === (table.schema || '-'))
    if (!schema) {
      schema = { schema: table.schema || '-', tables: [] }
      database.schemas.push(schema)
    }
    schema.tables.push(table)
  }
  return [...databases.values()]
}

function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}

function sumKnownRows(tables: AdminStorageTableSignal[]): string {
  let total = 0
  let known = 0
  for (const table of tables) {
    const value = Number(String(table.rowCountLabel ?? '').replaceAll(',', ''))
    if (Number.isFinite(value)) {
      total += value
      known += 1
    }
  }
  if (known === 0) return '-'
  const label = total.toLocaleString('en-US')
  return known < tables.length ? `${label} + unknown` : label
}

function sumKnownSizes(tables: AdminStorageTableSignal[]): string {
  let total = 0
  let known = 0
  for (const table of tables) {
    const bytes = parseSizeLabel(table.sizeLabel)
    if (bytes != null) {
      total += bytes
      known += 1
    }
  }
  if (known === 0) return 'Unknown'
  const label = formatBytes(total)
  return known < tables.length ? `${label} + unknown` : label
}

function sumKnownFiles(tables: AdminStorageTableSignal[]): string {
  let total = 0
  for (const table of tables) total += Number(table.fileCount ?? 0)
  return total.toLocaleString('en-US')
}

function parseSizeLabel(value: string | undefined): number | null {
  if (!value || value === 'Unknown' || value === '-') return null
  const match = value.match(/^([\d.]+)\s*(B|KiB|MiB|GiB|TiB)$/)
  if (!match) return null
  const amount = Number(match[1])
  if (!Number.isFinite(amount)) return null
  const units: Record<string, number> = {
    B: 1,
    KiB: 1024,
    MiB: 1024 ** 2,
    GiB: 1024 ** 3,
    TiB: 1024 ** 4,
  }
  return amount * units[match[2]]
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${Math.round(bytes)} B`
  let value = bytes
  for (const suffix of ['KiB', 'MiB', 'GiB', 'TiB']) {
    value /= 1024
    if (value < 1024) return `${value.toFixed(1)} ${suffix}`
  }
  return `${(value / 1024).toFixed(1)} PiB`
}

const storageExplorerStyles = `
  :host {
    display: block;
    min-width: 0;
    height: 100%;
    max-width: 100%;
  }

  .storage-explorer {
    display: grid;
    height: 100svh;
    min-height: 0;
    min-width: 0;
    grid-template-columns: minmax(18rem, 22rem) minmax(0, 1fr);
    grid-template-rows: auto auto minmax(0, 1fr);
    grid-template-areas:
      "header header"
      "warnings warnings"
      "browser detail";
    overflow: hidden;
    border: 0;
    border-radius: 0;
    background: var(--lv-bg-panel);
  }

  .storage-explorer-header {
    grid-area: header;
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--base-size-16, 1rem);
    align-items: center;
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-12) var(--base-size-16);
    background: var(--lv-bg-panel);
  }

  .storage-browser,
  .storage-detail {
    min-width: 0;
    min-height: 0;
  }

  .storage-browser {
    grid-area: browser;
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    gap: 0;
    border-right: var(--lv-border-muted);
    background: var(--lv-bg-panel);
  }

  .storage-heading {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr);
    align-items: center;
    gap: var(--base-size-4);
  }

  .storage-detail-header,
  .storage-columns-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  h2,
  h3,
  p,
  dl {
    margin: 0;
  }

  h2 {
    color: var(--lv-fg-default);
    font: var(--lv-type-section-title);
  }

  .storage-heading p {
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
  }

  .storage-heading p span {
    color: var(--lv-fg-default);
    font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  }

  .storage-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(5.75rem, auto));
    gap: var(--base-size-8, 0.5rem);
    align-items: center;
  }

  .storage-summary div {
    display: grid;
    gap: 0.125rem;
    min-width: 0;
    border-left: var(--lv-border-muted);
    padding-left: var(--base-size-12, 0.75rem);
  }

  .storage-summary span {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  .storage-summary strong {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body-compact);
  }

  h3 {
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  .storage-logo {
    display: none;
    width: 1.875rem;
    height: 1.875rem;
    place-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-small);
    color: var(--lv-fg-success);
    background: var(--lv-bg-panel);
  }

  .storage-search {
    position: relative;
    display: block;
    min-width: 0;
  }

  .storage-browser-menu {
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    padding: var(--base-size-4) var(--base-size-8);
  }

  .storage-search-icon {
    position: absolute;
    left: 0.5rem;
    top: 50%;
    display: grid;
    width: 1rem;
    height: 1rem;
    place-items: center;
    color: var(--lv-fg-muted);
    transform: translateY(-50%);
  }

  .storage-search input {
    min-height: 2.125rem;
    width: 100%;
    border: 0;
    border-radius: 0;
    background: transparent;
    padding: 0 0.625rem 0 2rem;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    outline: 0;
  }

  .storage-search input:focus {
    background: var(--lv-bg-control-hover);
  }

  .storage-warnings {
    grid-area: warnings;
    display: grid;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    padding: var(--base-size-8) var(--base-size-12);
  }

  .storage-warnings-empty {
    display: none;
  }

  .storage-warning {
    border: var(--lv-border-attention, var(--lv-border-muted));
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-attention-muted, var(--lv-bg-panel-muted));
    padding: var(--base-size-8) var(--base-size-12);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
  }

  .storage-tree {
    min-height: 0;
    overflow: auto;
    padding: 0.25rem;
  }

  .storage-db {
    display: grid;
  }

  .storage-db + .storage-db {
    margin-top: 0.125rem;
  }

  summary {
    display: grid;
    min-height: 1.9rem;
    grid-template-columns: 0.875rem 1rem minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.45rem;
    border-radius: var(--lv-radius-small);
    padding: 0 0.5rem;
    cursor: pointer;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    list-style: none;
  }

  summary::-webkit-details-marker {
    display: none;
  }

  summary:hover,
  summary:focus-visible {
    background: var(--lv-bg-hover);
    outline: 0;
  }

  .storage-db > summary {
    grid-template-columns: 0.875rem 1rem minmax(0, 1fr);
    margin-bottom: 0.125rem;
  }

  .storage-schema > summary {
    grid-template-columns: 0.875rem 1rem minmax(0, 1fr) auto;
    min-height: 1.75rem;
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
  }

  .storage-schema > summary.is-selected-schema {
    background: var(--lv-bg-accent-muted);
    color: var(--lv-fg-default);
  }

  summary span:not(.storage-chevron):not(.storage-node-icon) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  summary em {
    border-radius: var(--lv-radius-small);
    background: var(--lv-bg-panel-muted);
    padding: 0.125rem 0.375rem;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-style: normal;
    font-weight: var(--base-text-weight-medium);
    line-height: 1;
  }

  .storage-chevron,
  .storage-node-icon {
    display: grid;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  details[open] > summary .storage-chevron {
    transform: rotate(90deg);
  }

  .storage-schema {
    display: grid;
    margin-left: 1rem;
  }

  .storage-table-list {
    display: grid;
    gap: 0.0625rem;
    margin-left: 1.55rem;
    padding: 0.125rem 0 0.25rem;
  }

  .storage-table-button {
    display: grid;
    min-height: var(--lv-button-height-sm, var(--control-small-size));
    width: 100%;
    grid-template-columns: 1rem minmax(0, 1fr) max-content;
    align-items: center;
    gap: 0.45rem;
    border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-left: var(--borderWidth-thick, var(--lv-border-width-focus)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-radius: var(--lv-radius-small);
    background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    padding: 0 var(--lv-button-padding-inline-xs, var(--control-xsmall-paddingInline-normal));
    color: var(--lv-button-invisible-fg-rest, var(--lv-fg-default));
    text-align: left;
    font: inherit;
    cursor: pointer;
  }

  .storage-table-button:hover,
  .storage-table-button:focus-visible {
    border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-muted)));
    background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-control-hover)));
    outline: var(--focus-outline, var(--lv-border-default));
    outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }

  .storage-table-button.is-selected {
    border-left-color: var(--lv-line-accent);
    background: var(--lv-bg-accent-muted);
  }

  .storage-table-icon {
    display: grid;
    width: 1rem;
    height: 1rem;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  .storage-table-icon-view {
    color: var(--lv-fg-link);
  }

  .storage-table-button span {
    min-width: 0;
  }

  .storage-table-button strong,
  .storage-table-button small {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .storage-table-button strong {
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-medium);
  }

  .storage-table-button small {
    display: none;
  }

  .storage-table-size {
    overflow: hidden;
    max-width: 4.75rem;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-variant-numeric: tabular-nums;
    text-align: right;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .storage-detail {
    grid-area: detail;
    display: grid;
    grid-template-rows: auto auto minmax(0, 1fr);
    gap: 0;
    overflow: hidden;
    background: var(--lv-bg-panel);
  }

  .storage-detail-header {
    min-height: 3rem;
    border-bottom: var(--lv-border-muted);
    padding: 0.5rem 0.75rem;
  }

  .storage-detail-header nav {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: 0.375rem;
    color: var(--lv-fg-default);
    font: var(--lv-type-section-title);
  }

  .storage-detail-header nav > span {
    min-width: 0;
  }

  .storage-detail-header nav > span:not(.storage-breadcrumb-separator):not(.storage-breadcrumb-current),
  .storage-breadcrumb-button {
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .storage-breadcrumb-button {
    min-width: 0;
    border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-radius: var(--lv-radius-tight);
    background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    padding: 0;
    font: inherit;
    font-weight: inherit;
    text-align: left;
    cursor: pointer;
  }

  .storage-breadcrumb-button:hover,
  .storage-breadcrumb-button:focus-visible {
    border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-muted)));
    background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
    color: var(--lv-fg-link);
    outline: var(--focus-outline, var(--lv-border-default));
    outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
    text-decoration: underline;
    text-underline-offset: 0.125rem;
  }

  .storage-breadcrumb-separator {
    display: grid;
    flex: none;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  .storage-breadcrumb-current {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
  }

  .storage-metrics {
    display: flex;
    flex-wrap: wrap;
    align-items: stretch;
    gap: 0.75rem 1.5rem;
    overflow: hidden;
    border-bottom: var(--lv-border-muted);
    padding: 0.625rem 0.75rem;
  }

  .storage-metrics div {
    display: grid;
    min-width: 5.5rem;
    max-width: min(100%, 12rem);
    gap: 0.1875rem;
    align-content: start;
  }

  .storage-metrics .storage-metric-uuid {
    min-width: min(100%, 16rem);
    max-width: min(100%, 28rem);
  }

  .storage-metrics .storage-metric-path {
    min-width: 8rem;
    max-width: min(100%, 24rem);
  }

  .storage-detail-body {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
  }

  .storage-tabs {
    display: flex;
    min-width: 0;
    gap: 0.25rem;
    overflow-x: auto;
    border-bottom: var(--lv-border-muted);
    padding: 0.25rem 0.75rem 0;
  }

  .storage-tab {
    display: inline-flex;
    min-height: 2rem;
    flex: none;
    align-items: center;
    gap: 0.375rem;
    border: 0;
    border-bottom: 2px solid transparent;
    background: transparent;
    padding: 0 0.5rem;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    cursor: pointer;
  }

  .storage-tab:hover,
  .storage-tab:focus-visible {
    color: var(--lv-fg-default);
    outline: 0;
  }

  .storage-tab.is-active {
    border-bottom-color: var(--lv-line-accent);
    color: var(--lv-fg-default);
  }

  .storage-tab em {
    border-radius: var(--lv-radius-small);
    background: var(--lv-bg-panel-muted);
    padding: 0.0625rem 0.3125rem;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-style: normal;
    font-weight: var(--base-text-weight-medium);
  }

  .storage-tab-panel {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  dt {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  dd {
    margin: 0;
    overflow: hidden;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .storage-columns {
    display: grid;
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
    gap: 0;
  }

  .storage-columns-header {
    min-height: 2.125rem;
    border-bottom: var(--lv-border-muted);
    padding: 0 0.75rem;
  }

  .storage-column-table-wrap {
    min-height: 0;
    overflow: auto;
  }

  .storage-column-table {
    width: 100%;
    min-width: 42rem;
    border-collapse: collapse;
    table-layout: fixed;
  }

  .storage-schema-table {
    min-width: 50rem;
  }

  th,
  td {
    border-bottom: var(--lv-border-muted);
    padding: 0.4375rem 0.75rem;
    text-align: left;
    vertical-align: top;
  }

  th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--lv-bg-panel);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  td {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  th:first-child,
  td:first-child {
    width: 4rem;
    color: var(--lv-fg-muted);
    text-align: right;
  }

  .storage-schema-table th:nth-child(4),
  .storage-schema-table td:nth-child(4),
  .storage-schema-table th:nth-child(5),
  .storage-schema-table td:nth-child(5),
  .storage-schema-table th:nth-child(6),
  .storage-schema-table td:nth-child(6) {
    text-align: right;
  }

  .storage-schema-table-link {
    display: inline-flex;
    max-width: 100%;
    align-items: center;
    gap: 0.375rem;
    border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-radius: var(--lv-radius-tight);
    background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    padding: 0;
    color: var(--lv-fg-default);
    font: inherit;
    font-weight: var(--base-text-weight-medium);
    text-align: left;
    cursor: pointer;
  }

  .storage-schema-table-link:hover,
  .storage-schema-table-link:focus-visible {
    border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-muted)));
    background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
    color: var(--lv-fg-link);
    outline: var(--focus-outline, var(--lv-border-default));
    outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }

  .storage-schema-table-link span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  code {
    overflow-wrap: anywhere;
    color: var(--lv-fg-default);
    font: var(--lv-type-code-block);
  }

  .storage-muted,
  .storage-empty {
    color: var(--lv-fg-muted);
  }

  .storage-empty {
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-small);
    background: var(--lv-bg-panel-muted);
    padding: 0.75rem;
    font: var(--lv-type-secondary);
  }

  @media (max-width: 820px) {
    .storage-explorer {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto auto auto minmax(0, 1fr);
      grid-template-areas:
        "header"
        "warnings"
        "browser"
        "detail";
      height: auto;
      min-height: 0;
    }

    .storage-explorer-header {
      grid-template-columns: minmax(0, 1fr);
    }

    .storage-summary {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .storage-browser {
      max-height: 22rem;
      border-right: 0;
      border-bottom: var(--lv-border-muted);
    }

    .storage-detail {
      min-height: 28rem;
    }
  }
`

if (!customElements.get('lv-storage-explorer')) customElements.define('lv-storage-explorer', StorageExplorer)

declare global {
  interface HTMLElementTagNameMap {
    'lv-storage-explorer': StorageExplorer
  }
}
