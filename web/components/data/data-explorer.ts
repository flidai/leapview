import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronRight, Code2, Columns3, Database, Eye, Filter, Play, Plus, RotateCcw, Search, Server, Sigma, Square, SquareCheckBig, Table2, X } from 'lucide'
import type {
  AgentReferenceSignal,
  DataExploreCommand,
  DataExploreFieldSignal,
  DataExploreFilterSignal,
  DataExploreSignal,
  DataExplorerCommand,
  DataExplorerObjectSignal,
  DataExplorerPageSignal,
  DataExplorerSignal,
  DataPreviewSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { agentIcon } from '../chat/agent-icon'
import { fieldTypeIcon } from '../shared/field-type-icon'
import { lucideIcon } from '../shared/lucide-icons'
import '../chat/chat-drawer'
import './preview-table'
import './explore-table'

const emptyPreview: DataPreviewSignal = {
  columns: [],
  totalRows: 0,
  availableRows: 0,
  chunkSize: 100,
  rowHeight: 32,
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
  preview: emptyPreview,
  explore: {
    command: { modelId: '', datasetId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} },
    models: [], datasets: [], fields: [],
    result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
  },
  command: { mode: 'browse', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
  warnings: [],
}

type ResourceGroup = {
  id: string
  title: string
  objects: DataExplorerObjectSignal[]
}

type ExplorerColumn = { key: string, label?: string }

const dataExplorerAgentStorageKey = 'leapview-data-explorer-agent-state'

function readDataExplorerAgentState(): { open: boolean, conversationId: string } {
  try {
    const value = JSON.parse(localStorage.getItem(dataExplorerAgentStorageKey) ?? '') as { open?: boolean, conversationId?: string }
    return { open: value.open === true, conversationId: typeof value.conversationId === 'string' ? value.conversationId.trim() : '' }
  } catch {
    return { open: false, conversationId: '' }
  }
}

class DataExplorerPage extends DatastarLit(LitElement) {
  @property({ type: Boolean, reflect: true }) embedded = false
  @state() private search = ''
  @state() private fieldSearch = ''
  @state() private showSQL = false
  @state() private filterField = ''
  @state() private filterOperator = 'equals'
  @state() private filterValue = ''
  @state() private optimisticExplore: DataExploreCommand | null = null
  @state() private agentDrawerOpen = false
  @state() private browserCollapsed = false
  @state() private browserWidth = 320
  @state() private exploreVisibleColumns: string[] = []
  private lastSelectedKey = ''
  private lastSearch = ''
  private expandedGroupIDs = new Set<string>()
  private exploreTimer = 0
  private agentStateInitialized = false
  private agentRestoreDispatched = false
  private restoredAgentConversationId = ''
  private browserResizeCleanup?: () => void

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
    }

    :host([embedded]) {
      height: 100%;
      min-height: 0;
    }

    :host([embedded]) .route {
      height: 100%;
      min-height: 32rem;
      grid-template-rows: minmax(0, 1fr);
    }

    :host([embedded]) .header {
      display: none;
    }

    :host([embedded]) .route:not(.semantic) .explorer {
      grid-template-columns: minmax(0, 1fr);
    }

    :host([embedded]) .route:not(.semantic) .browser,
    :host([embedded]) .route:not(.semantic) .browser-resizer {
      display: none;
    }

    .route {
      display: grid;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
    }

    .route.agent-open {
      grid-template-columns: minmax(0, 1fr) minmax(20rem, 28rem);
    }

    .route.agent-open > .header,
    .route.agent-open > .explorer {
      grid-column: 1;
    }

    lv-chat-drawer {
      grid-row: 1 / -1;
      grid-column: 2;
      min-height: 0;
    }

    .header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-12);
      align-items: center;
      box-sizing: border-box;
      border-bottom: var(--lv-border-muted);
      min-height: 3.25rem;
      padding: var(--base-size-8) var(--base-size-12);
      background: var(--lv-bg-app);
    }

    .header-actions,
    .query-actions,
    .selection-shelf,
    .filter-pills {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
    }

    .header-columns {
      position: relative;
    }

    .header-columns summary {
      display: flex;
      height: var(--control-small-size);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      list-style: none;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      text-transform: none;
    }

    .header-columns summary::-webkit-details-marker {
      display: none;
    }

    .header-columns summary:hover,
    .header-columns summary:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .header-column-menu {
      position: absolute;
      top: calc(100% + var(--base-size-4));
      right: 0;
      z-index: var(--zIndex-overlay);
      display: grid;
      min-width: 14rem;
      max-height: 22rem;
      gap: var(--base-size-4);
      overflow: auto;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
      padding: var(--base-size-8);
    }

    .header-column-menu label {
      display: flex;
      min-height: var(--control-xsmall-size);
      align-items: center;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-caption);
    }

    .text-button,
    .chip,
    .field-button,
    .field-action {
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      font: inherit;
      cursor: pointer;
    }

    .text-button {
      min-height: var(--control-medium-size);
      padding: 0 var(--base-size-12);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
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
    }

    .explorer {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: auto 4px minmax(0, 1fr);
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
      background: var(--lv-bg-app);
    }

    .browser-tools {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-6) var(--base-size-8);
    }

    .sidebar-toggle {
      display: grid;
      width: var(--control-small-size);
      height: var(--control-small-size);
      flex: none;
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
    }

    .sidebar-toggle:hover,
    .sidebar-toggle:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .browser-resizer {
      position: relative;
      min-width: 4px;
      border-right: var(--lv-border-muted);
      cursor: col-resize;
      touch-action: none;
    }

    .browser-resizer::after {
      position: absolute;
      inset-block: 0;
      left: 1px;
      width: 2px;
      background: transparent;
      content: '';
    }

    .browser-resizer:hover::after,
    .browser-resizer:focus-visible::after {
      background: var(--lv-fg-link);
    }

    .browser-resizer:focus-visible {
      outline: 0;
    }

    .browser-collapsed .browser {
      grid-template-rows: max-content;
      align-content: start;
    }

    .explorer.browser-collapsed {
      grid-template-columns: auto minmax(0, 1fr);
    }

    .browser-collapsed .browser-tools {
      justify-content: start;
      padding-inline: var(--base-size-8);
    }

    .browser-collapsed .browser-resizer {
      display: none;
    }

    .explore-browser {
      grid-template-rows: auto auto minmax(0, 1fr);
    }

    .selectors {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12);
    }

    .selectors label,
    .filter-editor label {
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    select,
    .filter-editor input {
      min-width: 0;
      height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body);
    }

    .field-groups {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-8);
    }

    .field-group {
      margin-bottom: var(--base-size-8);
    }

    .field-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      border-radius: var(--lv-radius-default);
    }

    .field-row:hover,
    .field-row:focus-within {
      background: var(--lv-bg-control-hover);
    }

    .field-button {
      display: grid;
      min-width: 0;
      grid-template-columns: 1rem minmax(0, 1fr);
      gap: var(--base-size-8);
      align-items: center;
      padding: var(--base-size-8);
      text-align: left;
    }

    .field-button strong,
    .field-button small {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-button strong {
      font: var(--lv-type-body);
    }

    .field-button small {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-button.is-selected strong {
      color: var(--lv-fg-accent);
    }

    .field-action {
      display: grid;
      width: var(--control-small-size);
      height: var(--control-small-size);
      place-items: center;
      color: var(--lv-fg-muted);
    }

    .search {
      position: relative;
      min-width: 0;
      flex: 1;
    }

    .search input {
      width: 100%;
      min-width: 0;
      height: var(--control-small-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8) 0 var(--base-size-32);
      font: var(--lv-type-body);
    }

    .search-icon {
      position: absolute;
      left: var(--base-size-8);
      top: 50%;
      display: grid;
      color: var(--lv-fg-muted);
      transform: translateY(-50%);
    }

    .tree {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-6);
    }

    details {
      min-width: 0;
    }

    summary {
      display: grid;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      align-items: center;
      border-radius: var(--lv-radius-default);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
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

    .object-list {
      display: grid;
      gap: var(--base-size-2);
      padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-16);
    }

    .object-node > summary {
      display: grid;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      text-transform: none;
    }

    .object-expand {
      display: grid;
      width: 1.5rem;
      height: 1.5rem;
      place-items: center;
      margin: calc((1.5rem - 1rem) / -2);
      border-radius: var(--lv-radius-default);
      cursor: pointer;
    }

    .object-expand:hover {
      background: var(--lv-bg-control-hover);
    }

    .object-node[open] > summary .chevron {
      transform: rotate(90deg);
    }

    .object-button {
      display: grid;
      min-width: 0;
      width: 100%;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      align-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
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

    .object-button strong {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .object-button strong {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .column-list {
      display: grid;
      gap: var(--base-size-2);
      padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-32);
    }

    .column-item {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      min-height: var(--control-small-size);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .column-item:hover,
    .column-item:focus-within {
      background: var(--lv-bg-control-hover);
    }

    .column-item.is-unavailable {
      opacity: 0.58;
    }

    .column-item.is-unavailable:hover,
    .column-item.is-unavailable:focus-within {
      background: transparent;
    }

    .column-item .field-button {
      display: grid;
      min-width: 0;
      grid-template-columns: 1rem 1rem minmax(0, 1fr) auto;
      gap: var(--base-size-6);
      align-items: center;
      padding: var(--base-size-4) var(--base-size-8);
      text-align: left;
    }

    .column-item .field-button > span:nth-child(3) {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .column-item .field-button.is-selected > span:nth-child(3) {
      color: var(--lv-fg-accent);
    }

    .column-item .field-button:disabled {
      cursor: not-allowed;
    }

    .field-check {
      display: grid;
      place-items: center;
      color: var(--lv-fg-muted);
    }

    .field-button.is-selected .field-check {
      color: var(--lv-fg-accent);
    }

    .metric-field code {
      color: var(--lv-fg-accent);
    }

    .column-item code {
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
    }

    .main {
      display: grid;
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .semantic-result {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: auto auto auto minmax(0, 1fr);
      overflow: hidden;
    }

    .explore-main {
      grid-template-rows: auto auto minmax(0, 1fr) auto;
    }

    .query-bar {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12) var(--base-size-16);
      background: var(--lv-bg-app);
    }

    .query-row {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      align-items: start;
    }

    .query-label {
      min-width: 5rem;
      padding-top: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .selection-shelf,
    .filter-pills {
      min-width: 0;
      flex-wrap: wrap;
    }

    .chip {
      display: inline-flex;
      max-width: 18rem;
      align-items: center;
      gap: var(--base-size-4);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-control);
      padding: var(--base-size-4) var(--base-size-8);
      font: var(--lv-type-caption);
    }

    .chip.metric {
      border-color: var(--lv-line-accent, var(--lv-line-muted));
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .filter-editor {
      display: grid;
      grid-template-columns: minmax(8rem, 1fr) minmax(8rem, 1fr) minmax(12rem, 2fr) auto;
      gap: var(--base-size-8);
      align-items: end;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12) var(--base-size-16);
      background: var(--lv-bg-panel-muted);
    }

    .result-meta {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-8);
      align-items: center;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .result-error {
      color: var(--lv-fg-danger);
    }

    lv-data-explore-table {
      min-height: 0;
    }

    .diagnostics {
      display: grid;
      max-height: 16rem;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      overflow: auto;
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-app);
    }

    .diagnostic-block {
      min-width: 0;
      padding: var(--base-size-12) var(--base-size-16);
    }

    .diagnostic-block + .diagnostic-block {
      border-left: var(--lv-border-muted);
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

    .ask-button {
      display: inline-flex;
      width: auto;
      gap: var(--base-size-6);
      padding: 0 var(--base-size-12);
    }

    .header .icon-button {
      width: var(--control-small-size);
      height: var(--control-small-size);
    }

    .header .ask-button {
      width: auto;
    }

    .content {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-data-preview-table {
      min-height: 0;
    }

    .schema-view,
    .query-view {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-16);
    }

    .metadata-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
      gap: var(--base-size-12);
      margin-bottom: var(--base-size-16);
    }

    .metadata-card {
      min-width: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12);
    }

    .metadata-card dt {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .metadata-card dd {
      margin: var(--base-size-4) 0 0;
      overflow-wrap: anywhere;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .schema-table {
      width: 100%;
      border-collapse: collapse;
      border: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      font: var(--lv-type-body);
    }

    .schema-table th,
    .schema-table td {
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      text-align: left;
      vertical-align: top;
    }

    .schema-table th {
      position: sticky;
      top: 0;
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .schema-table code {
      color: var(--lv-fg-default);
      font: var(--lv-type-code-inline);
    }

    .schema-muted {
      color: var(--lv-fg-muted);
    }

    .query-heading {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      margin-bottom: var(--base-size-8);
      font: var(--lv-type-section-title);
    }

    .query-copy {
      margin-bottom: var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .query-code {
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-16);
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

      .browser-resizer {
        display: none;
      }

      .explorer.browser-collapsed {
        grid-template-columns: 1fr;
      }

      .browser,
      .main {
        min-height: 22rem;
      }

      .browser {
        width: auto !important;
      }

      .filter-editor,
      .diagnostics {
        grid-template-columns: 1fr;
      }
    }
  `

  connectedCallback(): void {
    if (!this.agentStateInitialized) {
      const stored = readDataExplorerAgentState()
      this.agentDrawerOpen = stored.open
      this.restoredAgentConversationId = stored.conversationId
      this.agentStateInitialized = true
    }
    super.connectedCallback()
  }

  disconnectedCallback(): void {
    window.clearTimeout(this.exploreTimer)
    this.browserResizeCleanup?.()
    super.disconnectedCallback()
  }

  updated(): void {
    const selectedKey = this.dataExplorer.selectedKey ?? ''
    if (selectedKey !== this.lastSelectedKey) {
      this.lastSelectedKey = selectedKey
      this.showSQL = false
      requestAnimationFrame(() => {
        this.renderRoot.querySelector<HTMLElement>('.object-button.is-selected')?.scrollIntoView({ block: 'nearest' })
      })
    }
    const search = this.search.trim().toLowerCase()
    if (search !== this.lastSearch) {
      this.lastSearch = search
      if (search) {
        requestAnimationFrame(() => {
          for (const node of this.renderRoot.querySelectorAll<HTMLDetailsElement>('.object-node[data-column-match="true"]')) {
            node.open = true
          }
        })
      }
    }
    if (this.optimisticExplore && (this.dataExplorer.explore?.command?.requestSeq ?? 0) >= this.optimisticExplore.requestSeq) {
      this.optimisticExplore = null
      if (!this.embedded) replaceDataExplorerURL(this.dataExplorer.command)
    }
    const agent = this.signal<{ activeConversationId?: string } | null>('agent', null)
    const activeConversationId = agent?.activeConversationId?.trim() ?? ''
    if (activeConversationId) {
      this.restoredAgentConversationId = activeConversationId
      this.persistAgentState()
    }
    if (agent && this.restoredAgentConversationId && !this.agentRestoreDispatched) {
      this.agentRestoreDispatched = true
      emitDomainEvent(this, domainEvents.chatRestore, { conversationId: this.restoredAgentConversationId })
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
    const semanticActive = explorer.command?.mode === 'explore' || this.optimisticExplore !== null
    const filtered = filterObjects(explorer.objects ?? [], this.search)
    const grouped = groupObjectsByModel(filtered, explorer.explore?.models ?? [])
    const agentEnabled = this.signal<unknown | null>('agent', null) !== null
    const columns = this.headerColumns(explorer, semanticActive)
    const visibleColumnKeys = this.headerVisibleColumnKeys(explorer, columns, semanticActive)
    return html`
      <section class=${`route${semanticActive ? ' semantic' : ''}${agentEnabled && this.agentDrawerOpen ? ' agent-open' : ''}`} aria-label="Data Explorer">
        <header class="header">
          <h1>${page?.title ?? 'Data Explorer'}</h1>
          <div class="header-actions">
            ${columns.length ? html`
              <details class="header-columns">
                <summary title="Choose visible columns" aria-label="Choose visible columns">
                  ${lucideIcon(Columns3, { size: 15 })}<span>Columns</span><span aria-hidden="true">${visibleColumnKeys.length}/${columns.length}</span>
                </summary>
                <div class="header-column-menu">
                  ${columns.map((column) => {
                    const checked = visibleColumnKeys.includes(column.key)
                    return html`
                      <label>
                        <input
                          type="checkbox"
                          .checked=${checked}
                          ?disabled=${checked && visibleColumnKeys.length <= 1}
                          @change=${(event: Event) => this.toggleHeaderColumn(column.key, (event.target as HTMLInputElement).checked, columns, semanticActive)}
                        />
                        ${column.label || column.key}
                      </label>
                    `
                  })}
                </div>
              </details>
            ` : nothing}
            ${agentEnabled ? html`<button type="button" class="icon-button ask-button" aria-label="Ask about this data" aria-expanded=${String(this.agentDrawerOpen)} title="Ask about this data" @click=${() => this.setAgentDrawerOpen(!this.agentDrawerOpen)}>${agentIcon()}<span>Ask</span></button>` : nothing}
          </div>
        </header>
        <div
          class=${`explorer${this.browserCollapsed ? ' browser-collapsed' : ''}`}
        >
          <aside class="browser" aria-label="Data objects" style=${`width:${this.browserCollapsed ? 44 : this.browserWidth}px`}>
            <div class="browser-tools">
              <button
                type="button"
                class="sidebar-toggle"
                aria-label=${this.browserCollapsed ? 'Open data browser' : 'Close data browser'}
                aria-expanded=${String(!this.browserCollapsed)}
                title=${this.browserCollapsed ? 'Open data browser' : 'Close data browser'}
                @click=${() => this.browserCollapsed = !this.browserCollapsed}
              >${lucideIcon(Database, { size: 16 })}</button>
              ${this.browserCollapsed ? nothing : html`
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
              `}
            </div>
            ${this.browserCollapsed ? nothing : html`
              <div class="tree">
                ${filtered.length
                  ? html`
                    ${this.renderResourceGroups(grouped, explorer.selectedKey ?? '', explorer.explore ?? emptyExplorer.explore, semanticActive)}
                  `
                  : html`<p class="empty">No data objects match this search.</p>`}
              </div>
            `}
          </aside>
          <div
            class="browser-resizer"
            role="separator"
            aria-label="Resize data browser"
            aria-orientation="vertical"
            aria-valuemin="280"
            aria-valuemax="440"
            aria-valuenow=${String(this.browserWidth)}
            tabindex="0"
            @pointerdown=${this.beginBrowserResize}
            @keydown=${this.resizeBrowserFromKeyboard}
          ></div>
          <main class="main" aria-label="Data results">
            ${selected
              ? semanticActive
                ? this.renderExploreSelected(selected, explorer.explore ?? emptyExplorer.explore)
                : this.renderSelected(selected, explorer.preview ?? emptyPreview, explorer.command ?? emptyExplorer.command)
              : html`<p class="empty">${(explorer.objects ?? []).length
                ? 'Select a data object to begin.'
                : 'No data objects are available.'}</p>`}
          </main>
        </div>
        ${agentEnabled && this.agentDrawerOpen ? html`<lv-chat-drawer
          open
          .suggestions=${this.agentSuggestions(explorer)}
          @lv-chat-drawer-close=${() => this.setAgentDrawerOpen(false)}
          @lv-chat-new=${this.handleAgentNew}
        ></lv-chat-drawer>` : nothing}
      </section>
    `
  }

  private renderExplore(exploreSignal: DataExploreSignal) {
    const explore = exploreSignal ?? emptyExplorer.explore
    const command = this.optimisticExplore ?? explore.command
    const selectedModel = explore.models.find((model) => model.id === command.modelId) ?? explore.selectedModel
    const datasets = selectedModel?.datasets ?? explore.datasets ?? []
    const selectedDataset = datasets.find((dataset) => dataset.id === command.datasetId) ?? explore.selectedDataset
    const queryFields = new Set([...command.dimensions, ...command.metrics])
    const visibleFields = (explore.fields ?? []).filter((field) => {
      const query = this.fieldSearch.trim().toLowerCase()
      return !query || [field.label, field.id, field.modelTable, field.description, field.type]
        .some((value) => String(value ?? '').toLowerCase().includes(query))
    })
    const fieldGroups = groupExploreFields(visibleFields)
    const result = explore.result
    const hasQuery = command.dimensions.length > 0 || command.metrics.length > 0 || Boolean(command.time)
    return html`
      <div class="explorer">
        <aside class="browser explore-browser" aria-label="Semantic fields">
          <div class="selectors">
            <label>Semantic model
              <select .value=${command.modelId ?? ''} @change=${(event: Event) => this.changeExploreModel((event.target as HTMLSelectElement).value, explore)}>
                ${(explore.models ?? []).map((model) => html`<option value=${model.id}>${model.title}</option>`)}
              </select>
            </label>
            <label>Starting dataset
              <select .value=${command.datasetId ?? ''} @change=${(event: Event) => this.emitExplore({ ...command, datasetId: (event.target as HTMLSelectElement).value }, true)}>
                ${datasets.map((dataset) => html`<option value=${dataset.id}>${dataset.title}</option>`)}
              </select>
            </label>
          </div>
          <label class="search">
            <span class="search-icon" aria-hidden="true">${lucideIcon(Search, { size: 15 })}</span>
            <input
              type="search"
              .value=${this.fieldSearch}
              @input=${(event: Event) => this.fieldSearch = (event.target as HTMLInputElement).value}
              placeholder="Search fields"
              autocomplete="off"
            />
          </label>
          <div class="field-groups">
            ${fieldGroups.length ? fieldGroups.map((group) => html`
              <details class="field-group" open>
                <summary>
                  <span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
                  <span aria-hidden="true">${lucideIcon(group.kind === 'metric' ? Code2 : Table2, { size: 14 })}</span>
                  <span>${group.label}</span>
                  <em>${group.fields.length}</em>
                </summary>
                <div class="object-list">
                  ${group.fields.map((field) => html`
                    <div class="field-row">
                      <button
                        type="button"
                        class=${queryFields.has(field.id) ? 'field-button is-selected' : 'field-button'}
                        title=${field.description || field.id}
                        @click=${() => this.toggleExploreField(field, command)}
                      >
                        <span aria-hidden="true">${queryFields.has(field.id) ? lucideIcon(X, { size: 14 }) : lucideIcon(Plus, { size: 14 })}</span>
                        <span><strong>${field.label}</strong><small>${field.modelTable} · ${field.type || field.kind}</small></span>
                      </button>
                      ${field.kind === 'dimension' ? html`<button type="button" class="field-action" title="Filter ${field.label}" aria-label="Filter ${field.label}" @click=${() => this.openFilter(field)}>${lucideIcon(Filter, { size: 14 })}</button>` : nothing}
                    </div>
                  `)}
                </div>
              </details>
            `) : html`<p class="empty">No semantic fields match this search.</p>`}
          </div>
        </aside>
        <main class="main explore-main" aria-label="Semantic exploration">
          <section class="query-bar" aria-label="Exploration query">
            <div class="query-row">
              <span class="query-label">Fields</span>
              <div class="selection-shelf">
                ${command.dimensions.map((id) => this.renderQueryChip(id, 'dimension', explore.fields, command))}
                ${command.metrics.map((id) => this.renderQueryChip(id, 'metric', explore.fields, command))}
                ${!queryFields.size ? html`<span class="empty">Choose dimensions and metrics from the field picker.</span>` : nothing}
              </div>
              <div class="query-actions">
                <button type="button" class="text-button" title="Run now" @click=${() => this.emitExplore(command, true)}>${lucideIcon(Play, { size: 14 })} Run</button>
                <button type="button" class="icon-button" title="Reset exploration" aria-label="Reset exploration" @click=${() => this.resetExplore(command)}>${lucideIcon(RotateCcw, { size: 16 })}</button>
                <button type="button" class="icon-button" title="Toggle query details" aria-label="Toggle query details" @click=${() => this.showSQL = !this.showSQL}>${lucideIcon(Code2, { size: 16 })}</button>
              </div>
            </div>
            <div class="query-row">
              <span class="query-label">Filters</span>
              <div class="filter-pills">
                ${command.filters.map((filter, index) => html`
                  <button type="button" class="chip" title="Remove filter" @click=${() => this.removeExploreFilter(index, command)}>
                    ${fieldLabel(filter.field, explore.fields)} ${filter.operator.replaceAll('_', ' ')} ${filter.values.join(', ')} ${lucideIcon(X, { size: 12 })}
                  </button>
                `)}
                ${!command.filters.length ? html`<span class="empty">No filters</span>` : nothing}
              </div>
              <label>Rows
                <select .value=${String(command.limit)} @change=${(event: Event) => this.emitExplore({ ...command, limit: Number((event.target as HTMLSelectElement).value) })}>
                  ${[50, 100, 250, 500, 1000].map((limit) => html`<option value=${limit}>${limit}</option>`)}
                </select>
              </label>
            </div>
          </section>
          ${this.filterField ? this.renderFilterEditor(command, explore.fields) : nothing}
          <div class="result-meta" aria-live="polite">
            <span><strong>${selectedModel?.title ?? 'Semantic model'}</strong>${selectedDataset ? ` · ${selectedDataset.title}` : ''}</span>
            ${selectedDataset?.grain ? html`<span>Grain: ${selectedDataset.grain}</span>` : nothing}
            ${hasQuery && !result.error ? html`<span>${result.rowsReturned} rows · ${result.durationMs} ms${result.truncated ? ' · truncated' : ''}</span>` : nothing}
            ${result.error ? html`<span class="result-error">${result.error}</span>` : nothing}
            ${(result.warnings ?? []).map((warning) => html`<span>${warning}</span>`)}
          </div>
          ${hasQuery
            ? html`<lv-data-explore-table
                .command=${command}
                .result=${result}
                .visibleColumns=${this.exploreVisibleColumns}
                @lv-data-explore-table-command=${(event: CustomEvent<Partial<DataExploreCommand>>) => this.emitExplore({ ...command, ...event.detail })}
              ></lv-data-explore-table>`
            : html`<p class="empty">Select at least one dimension or metric to run a governed exploration.</p>`}
          ${this.showSQL ? html`<section class="diagnostics" aria-label="Query details">
            <div class="diagnostic-block"><h3>Generated SQL</h3><pre>${result.sql || 'Run an exploration to inspect generated SQL.'}</pre></div>
            <div class="diagnostic-block"><h3>Query plan</h3><pre>${result.plan || 'No query plan is available.'}</pre></div>
          </section>` : nothing}
        </main>
      </div>
    `
  }

  private renderQueryChip(id: string, kind: 'dimension' | 'metric', fields: DataExploreFieldSignal[], command: DataExploreCommand) {
    return html`<button type="button" class=${`chip ${kind}`} title="Remove field" @click=${() => this.removeExploreField(id, kind, command)}>
      ${fieldLabel(id, fields)} ${lucideIcon(X, { size: 12 })}
    </button>`
  }

  private renderFilterEditor(command: DataExploreCommand, fields: DataExploreFieldSignal[]) {
    return html`<section class="filter-editor" aria-label="Add filter">
      <label>Field<input .value=${fieldLabel(this.filterField, fields)} disabled /></label>
      <label>Condition
        <select .value=${this.filterOperator} @change=${(event: Event) => this.filterOperator = (event.target as HTMLSelectElement).value}>
          <option value="equals">Equals</option>
          <option value="in">Is one of</option>
          <option value="contains">Contains</option>
          <option value="not_contains">Does not contain</option>
          <option value="starts_with">Starts with</option>
          <option value="greater_than_or_equal">At least</option>
          <option value="less_than">Less than</option>
          <option value="is_null">Is null</option>
          <option value="is_not_null">Is not null</option>
        </select>
      </label>
      <label>Value<input .value=${this.filterValue} ?disabled=${this.filterOperator === 'is_null' || this.filterOperator === 'is_not_null'} @input=${(event: Event) => this.filterValue = (event.target as HTMLInputElement).value} @keydown=${(event: KeyboardEvent) => { if (event.key === 'Enter') this.applyExploreFilter(command) }} /></label>
      <div class="query-actions">
        <button type="button" class="text-button" @click=${() => this.closeFilter()}>Cancel</button>
        <button type="button" class="text-button" @click=${() => this.applyExploreFilter(command)}>Apply</button>
      </div>
    </section>`
  }

  private setMode(mode: 'browse' | 'explore') {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    if (current.mode === mode) return
    this.showSQL = false
    this.emitCommand({ mode, explore: this.optimisticExplore ?? current.explore ?? this.dataExplorer.explore.command })
  }

  private changeExploreModel(modelId: string, explore: DataExploreSignal) {
    const model = explore.models.find((candidate) => candidate.id === modelId)
    const current = this.optimisticExplore ?? explore.command
    this.emitExplore({
      ...current, modelId, datasetId: model?.datasets?.[0]?.id ?? '', dimensions: [], metrics: [], filters: [], sort: [],
    }, true)
  }

  private toggleExploreField(field: DataExploreFieldSignal, command: DataExploreCommand) {
    if (field.compatible === false && !field.rebaseDatasetId) return
    const key = field.kind === 'metric' ? 'metrics' : 'dimensions'
    const values = command[key]
    const next = values.includes(field.id) ? values.filter((id) => id !== field.id) : [...values, field.id]
    this.emitExplore({ ...command, [key]: next, sort: command.sort.filter((sort) => sort.field !== field.id) })
  }

  private toggleUnifiedField(
    field: DataExploreFieldSignal,
    object: DataExplorerObjectSignal,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    if (field.compatible === false && !field.rebaseDatasetId) return
    const selected = this.dataExplorer.selectedObject
    const baseObject = selected && selected.modelId === object.modelId
      ? selected
      : object
    const current = this.optimisticExplore ?? explore.command ?? emptyExplorer.explore.command
    const contextMatches = exploreContextMatchesObject(current, baseObject)
    const activeCommand = semanticActive && contextMatches
    const baseDimensions = (explore.fields ?? [])
      .filter((candidate) => candidate.compatible !== false && candidate.kind !== 'metric' && candidate.modelTable === objectTableID(baseObject))
      .map((candidate) => candidate.id)
    const fallbackDimensions = (baseObject.columns ?? []).map((column) => `${objectTableID(baseObject)}.${column.key}`)
    const command: DataExploreCommand = activeCommand ? current : {
      ...current,
      modelId: baseObject.modelId ?? '',
      datasetId: objectTableID(baseObject),
      dimensions: baseDimensions.length ? baseDimensions : fallbackDimensions,
      metrics: [],
      filters: [],
      sort: [],
      columnWidths: {},
    }
    const key = field.kind === 'metric' ? 'metrics' : 'dimensions'
    const values = command[key] ?? []
    const selectedByDefault = !activeCommand && field.kind !== 'metric' && field.modelTable === objectTableID(baseObject)
    const selectedNow = values.includes(field.id) || selectedByDefault
    const next = selectedNow ? values.filter((id) => id !== field.id) : [...values, field.id]
    this.emitExplore({ ...command, [key]: next, sort: (command.sort ?? []).filter((sort) => sort.field !== field.id) })
  }

  private removeExploreField(id: string, kind: 'dimension' | 'metric', command: DataExploreCommand) {
    const key = kind === 'metric' ? 'metrics' : 'dimensions'
    this.emitExplore({ ...command, [key]: command[key].filter((field) => field !== id), sort: command.sort.filter((sort) => sort.field !== id) })
  }

  private resetExplore(command: DataExploreCommand) {
    this.closeFilter()
    this.emitExplore({ ...command, dimensions: [], metrics: [], filters: [], sort: [], time: undefined, columnWidths: {} }, true)
  }

  private openFilter(field: DataExploreFieldSignal) {
    this.filterField = field.id
    this.filterOperator = 'equals'
    this.filterValue = ''
  }

  private closeFilter() {
    this.filterField = ''
    this.filterOperator = 'equals'
    this.filterValue = ''
  }

  private applyExploreFilter(command: DataExploreCommand) {
    if (!this.filterField) return
    const needsValue = this.filterOperator !== 'is_null' && this.filterOperator !== 'is_not_null'
    const values = needsValue
      ? this.filterValue.split(',').map((value) => value.trim()).filter(Boolean)
      : []
    if (needsValue && !values.length) return
    const filter: DataExploreFilterSignal = { field: this.filterField, operator: this.filterOperator, values }
    this.closeFilter()
    this.emitExplore({ ...command, filters: [...command.filters.filter((current) => current.field !== filter.field), filter] })
  }

  private removeExploreFilter(index: number, command: DataExploreCommand) {
    this.emitExplore({ ...command, filters: command.filters.filter((_, current) => current !== index) })
  }

  private emitExplore(next: DataExploreCommand, immediate = false) {
    window.clearTimeout(this.exploreTimer)
    const current = this.optimisticExplore ?? this.dataExplorer.explore.command ?? emptyExplorer.explore.command
    const command: DataExploreCommand = {
      ...current,
      ...next,
      modelId: next.modelId ?? current.modelId ?? '',
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
    this.optimisticExplore = command
    if (!this.embedded) replaceDataExplorerURL({ ...this.dataExplorer.command, mode: 'explore', explore: command })
    const dispatch = () => this.emitCommand({ mode: 'explore', explore: command })
    if (immediate) dispatch()
    else this.exploreTimer = window.setTimeout(dispatch, 320)
  }

  private agentSuggestions(explorer: DataExplorerSignal): AgentReferenceSignal[] {
    const command = this.optimisticExplore ?? explorer.explore.command
    const context = this.page?.context
    const projectId = context?.projectId ?? ''
    const generationId = context?.generationId ?? ''
    const modelId = command.modelId ?? ''
    const datasetId = command.datasetId ?? ''
    if (!projectId || !generationId || !modelId || !datasetId) return []
    const dataset = explorer.explore.datasets.find((candidate) => candidate.id === datasetId)
    const href = `/explore?mode=explore&model=${encodeURIComponent(modelId)}&dataset=${encodeURIComponent(datasetId)}`
    return [{
      reference: { kind: 'dataset', id: `${modelId}/${datasetId}` },
      name: dataset?.title ?? datasetId,
      description: dataset?.description,
      hierarchy: [projectId, modelId], href, locations: [], context: ['active_project_generation'],
    }]
  }

  private handleAgentNew = () => {
    this.restoredAgentConversationId = ''
    this.agentRestoreDispatched = true
    this.persistAgentState()
  }

  private setAgentDrawerOpen(open: boolean): void {
    this.agentDrawerOpen = open
    this.persistAgentState()
  }

  private beginBrowserResize = (event: PointerEvent): void => {
    if (this.browserCollapsed) return
    event.preventDefault()
    this.browserResizeCleanup?.()
    const startX = event.clientX
    const startWidth = this.browserWidth
    const move = (next: PointerEvent) => {
      this.browserWidth = clampBrowserWidth(startWidth + next.clientX - startX)
    }
    const finish = () => this.browserResizeCleanup?.()
    this.browserResizeCleanup = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
      window.removeEventListener('pointercancel', finish)
      this.browserResizeCleanup = undefined
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', finish)
  }

  private resizeBrowserFromKeyboard = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    this.browserWidth = clampBrowserWidth(this.browserWidth + (event.key === 'ArrowRight' ? 16 : -16))
  }

  private headerColumns(explorer: DataExplorerSignal, semanticActive: boolean): ExplorerColumn[] {
    if (semanticActive) return explorer.explore?.result?.columns ?? []
    const previewColumns = explorer.preview?.columns ?? []
    return previewColumns.length ? previewColumns : explorer.selectedObject?.columns ?? []
  }

  private headerVisibleColumnKeys(explorer: DataExplorerSignal, columns: ExplorerColumn[], semanticActive: boolean): string[] {
    const configured = semanticActive ? this.exploreVisibleColumns : explorer.command?.visibleColumns ?? []
    if (!configured.length) return columns.map((column) => column.key)
    const allowed = new Set(configured)
    const visible = columns.filter((column) => allowed.has(column.key)).map((column) => column.key)
    return visible.length ? visible : columns.map((column) => column.key)
  }

  private toggleHeaderColumn(key: string, checked: boolean, columns: ExplorerColumn[], semanticActive: boolean): void {
    const visible = this.headerVisibleColumnKeys(this.dataExplorer, columns, semanticActive)
    const next = checked
      ? columns.map((column) => column.key).filter((columnKey) => columnKey === key || visible.includes(columnKey))
      : visible.filter((columnKey) => columnKey !== key)
    const configured = next.length === columns.length ? [] : next
    if (semanticActive) {
      this.exploreVisibleColumns = configured
      return
    }
    this.emitCommand({ visibleColumns: configured })
  }

  private persistAgentState(): void {
    try {
      localStorage.setItem(dataExplorerAgentStorageKey, JSON.stringify({
        open: this.agentDrawerOpen,
        conversationId: this.restoredAgentConversationId,
      }))
    } catch {
      // The drawer remains usable when local storage is unavailable.
    }
  }

  private renderResourceGroups(
    groups: ResourceGroup[],
    selectedKey: string,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    const revealMatches = Boolean(this.search.trim())
    return groups.map((group) => html`
      <details
        ?open=${revealMatches || this.expandedGroupIDs.has(group.id)}
        class="resource-group"
        data-group-id=${group.id}
        @toggle=${(event: Event) => this.handleGroupToggle(event, group.id)}
      >
        <summary>
          <span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
          <span class="resource-icon" aria-hidden="true" title="Project resource">${lucideIcon(Database, { size: 14 })}</span>
          <span title=${`${group.objects.length} model tables`}>${label(group.title)} (${group.objects.length})</span>
        </summary>
        <div class="object-list">
          ${this.renderObjectNodes(group.objects, selectedKey, explore, semanticActive)}
        </div>
      </details>
    `)
  }

  private handleGroupToggle(event: Event, groupID: string): void {
    const details = event.currentTarget as HTMLDetailsElement
    if (details.open) this.expandedGroupIDs.add(groupID)
    else this.expandedGroupIDs.delete(groupID)
  }

  private renderObjectNodes(
    objects: DataExplorerObjectSignal[],
    selectedKey: string,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    const titleCounts = new Map<string, number>()
    for (const object of objects) {
      const title = object.title.trim().toLowerCase()
      titleCounts.set(title, (titleCounts.get(title) ?? 0) + 1)
    }
    return objects.map((object) => {
      const selected = object.key === selectedKey
      const duplicateTitle = (titleCounts.get(object.title.trim().toLowerCase()) ?? 0) > 1
      const displayTitle = duplicateTitle && object.modelId ? `${object.modelId}.${object.title}` : object.title
      const columnMatch = objectColumnMatchesSearch(object, this.search)
      const command = this.optimisticExplore ?? explore.command
      const contextMatches = exploreContextMatchesObject(command, object)
      const semanticFields = contextMatches
        ? (explore.fields ?? []).filter((field) => field.modelTable === objectTableID(object))
        : []
      const dimensionByColumn = new Map(
        semanticFields.filter((field) => field.kind !== 'metric').map((field) => [fieldColumnID(field), field]),
      )
      const dimensions = (object.columns ?? []).map((column): DataExploreFieldSignal => dimensionByColumn.get(column.key) ?? {
        id: `${objectTableID(object)}.${column.key}`,
        label: column.label || column.key,
        kind: 'dimension',
        modelTable: objectTableID(object),
        type: column.type,
        description: column.description,
        compatible: true,
        selected: false,
      })
      const metrics = semanticFields.filter((field) => field.kind === 'metric')
      const fields = [...dimensions, ...metrics]
      const queryFields = new Set([...(command.dimensions ?? []), ...(command.metrics ?? [])])
      return html`
        <details class="object-node" data-column-match=${String(columnMatch)}>
          <summary
            class=${selected ? 'object-button is-selected' : 'object-button'}
            @click=${(event: MouseEvent) => this.handleObjectNodeClick(event, object, selected)}
          >
            <span class="chevron object-expand" title="Expand columns" aria-label="Expand columns">${lucideIcon(ChevronRight, { size: 13 })}</span>
            <span aria-hidden="true">${lucideIcon(iconForLayer(object.layer), { size: 14 })}</span>
            <strong title=${`${object.columnCount || 0} columns`}>${label(displayTitle)}</strong>
          </summary>
          <div class="column-list" aria-label=${`${object.title} fields`}>
            ${fields.map((field) => {
              const compatible = field.compatible !== false
              const rebaseable = !compatible && Boolean(field.rebaseDatasetId)
              const selectable = compatible || rebaseable
              const relationshipPath = field.relationshipPath ?? []
              const fieldSelected = compatible && semanticActive && contextMatches
                ? queryFields.has(field.id)
                : selected && field.kind !== 'metric'
              const compatibilityTitle = compatible
                ? relationshipPath.length
                  ? `Related through ${relationshipPath.join(' → ')}`
                  : field.description || field.id
                : field.compatibilityReason || `Not compatible with ${command.datasetId || objectTableID(object)}`
              return html`
              <div class=${`${field.kind === 'metric' ? 'column-item metric-field' : 'column-item'}${selectable ? '' : ' is-unavailable'}${rebaseable ? ' is-rebaseable' : ''}`} title=${compatibilityTitle}>
                <button
                  type="button"
                  class=${fieldSelected ? 'field-button is-selected' : 'field-button'}
                  aria-pressed=${String(fieldSelected)}
                  aria-disabled=${String(!selectable)}
                  ?disabled=${!selectable}
                  title=${compatible ? `${fieldSelected ? 'Remove' : 'Add'} ${field.label}${relationshipPath.length ? ` · ${compatibilityTitle}` : ''}` : compatibilityTitle}
                  @click=${() => this.toggleUnifiedField(field, object, explore, semanticActive)}
                >
                  <span class="field-check" aria-hidden="true">${lucideIcon(fieldSelected ? SquareCheckBig : Square, { size: 13 })}</span>
                  <span title=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'} aria-label=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'}>${lucideIcon(field.kind === 'metric' ? Sigma : fieldTypeIcon(field.type), { size: 13 })}</span>
                  <span>${field.label || field.id}</span>
                  <code>${compatible ? field.kind === 'metric' ? 'metric' : relationshipPath.length ? 'related' : field.type || '' : rebaseable ? 'changes grain' : 'unavailable'}</code>
                </button>
                ${field.kind === 'dimension' && compatible && semanticActive && contextMatches
                  ? html`<button type="button" class="field-action" title="Filter ${field.label}" aria-label="Filter ${field.label}" @click=${() => this.openFilter(field)}>${lucideIcon(Filter, { size: 13 })}</button>`
                  : nothing}
              </div>
            `})}
          </div>
        </details>
      `
    })
  }

  private renderSelected(_object: DataExplorerObjectSignal, preview: DataPreviewSignal, command: DataExplorerCommand) {
    return html`
      <div class="content" aria-label="Data preview">
        <lv-data-preview-table
          .preview=${preview}
          .command=${command}
          @lv-data-preview-table-command=${(event: CustomEvent<Partial<DataExplorerCommand>>) => this.emitCommand(event.detail)}
        ></lv-data-preview-table>
      </div>
    `
  }

  private renderExploreSelected(object: DataExplorerObjectSignal, exploreSignal: DataExploreSignal) {
    const explore = exploreSignal ?? emptyExplorer.explore
    const command = this.optimisticExplore ?? explore.command
    const selectedModel = explore.models.find((model) => model.id === command.modelId) ?? explore.selectedModel
    const datasets = selectedModel?.datasets ?? explore.datasets ?? []
    const selectedDataset = datasets.find((dataset) => dataset.id === command.datasetId) ?? explore.selectedDataset
    const queryFields = new Set([...(command.dimensions ?? []), ...(command.metrics ?? [])])
    const result = explore.result
    const hasQuery = queryFields.size > 0 || Boolean(command.time)
    return html`
      <div class="content" aria-label="Data exploration">
        <section class="semantic-result" aria-label="Governed result table">
            <section class="query-bar" aria-label="Query">
              <div class="query-row">
                <span class="query-label">Fields</span>
                <div class="selection-shelf">
                  ${(command.dimensions ?? []).map((id) => this.renderQueryChip(id, 'dimension', explore.fields, command))}
                  ${(command.metrics ?? []).map((id) => this.renderQueryChip(id, 'metric', explore.fields, command))}
                  ${!queryFields.size ? html`<span class="empty">Select fields from the expanded model tables.</span>` : nothing}
                </div>
                <div class="query-actions">
                  <button type="button" class="text-button" title="Run now" @click=${() => this.emitExplore(command, true)}>${lucideIcon(Play, { size: 14 })} Run</button>
                  <button type="button" class="icon-button" title="Return to all table columns" aria-label="Return to all table columns" @click=${() => this.selectObject(object)}>${lucideIcon(RotateCcw, { size: 16 })}</button>
                </div>
              </div>
              <div class="query-row">
                <span class="query-label">Filters</span>
                <div class="filter-pills">
                  ${(command.filters ?? []).map((filter, index) => html`
                    <button type="button" class="chip" title="Remove filter" @click=${() => this.removeExploreFilter(index, command)}>
                      ${fieldLabel(filter.field, explore.fields)} ${filter.operator.replaceAll('_', ' ')} ${filter.values.join(', ')} ${lucideIcon(X, { size: 12 })}
                    </button>
                  `)}
                  ${!command.filters?.length ? html`<span class="empty">No filters</span>` : nothing}
                </div>
                <label>Rows
                  <select .value=${String(command.limit)} @change=${(event: Event) => this.emitExplore({ ...command, limit: Number((event.target as HTMLSelectElement).value) })}>
                    ${[50, 100, 250, 500, 1000].map((limit) => html`<option value=${limit}>${limit}</option>`)}
                  </select>
                </label>
              </div>
            </section>
            ${this.filterField ? this.renderFilterEditor(command, explore.fields) : nothing}
            <div class="result-meta" aria-live="polite">
              <span><strong>${selectedModel?.title ?? label(command.modelId)}</strong>${selectedDataset ? ` · ${selectedDataset.title}` : ''}</span>
              ${selectedDataset?.grain ? html`<span>Grain: ${selectedDataset.grain}</span>` : nothing}
              ${hasQuery && !result.error ? html`<span>${result.rowsReturned} rows · ${result.durationMs} ms${result.truncated ? ' · truncated' : ''}</span>` : nothing}
              ${result.error ? html`<span class="result-error">${result.error}</span>` : nothing}
              ${(result.warnings ?? []).map((warning) => html`<span>${warning}</span>`)}
            </div>
            ${hasQuery
              ? html`<lv-data-explore-table
                  .command=${command}
                  .result=${result}
                  .visibleColumns=${this.exploreVisibleColumns}
                  @lv-data-explore-table-command=${(event: CustomEvent<Partial<DataExploreCommand>>) => this.emitExplore({ ...command, ...event.detail })}
                ></lv-data-explore-table>`
              : html`<p class="empty">Select at least one field to build a governed result table.</p>`}
        </section>
      </div>
    `
  }

  private renderExploreQueryDetails(object: DataExplorerObjectSignal, explore: DataExploreSignal, command: DataExploreCommand) {
    const result = explore.result
    return html`
      <section class="query-view" aria-label="Query details">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Query target</dt><dd>${label(command.modelId)} / ${label(command.datasetId)}</dd></div>
          <div class="metadata-card"><dt>Fields</dt><dd>${command.dimensions.length + command.metrics.length}</dd></div>
          <div class="metadata-card"><dt>Filters</dt><dd>${command.filters.length}</dd></div>
          <div class="metadata-card"><dt>Rows returned</dt><dd>${result.rowsReturned}</dd></div>
        </dl>
        <h3 class="query-heading">${lucideIcon(Code2, { size: 17 })} Generated SQL</h3>
        <p class="query-copy">This is the governed query generated from the selected fields, relationships, filters, and metrics.</p>
        <pre class="query-code">${result.sql || 'Select fields to generate a query.'}</pre>
        ${result.plan ? html`<h3 class="query-heading">Query plan</h3><pre class="query-code">${result.plan}</pre>` : nothing}
        ${object.description ? html`<p class="query-copy">${object.description}</p>` : nothing}
      </section>
    `
  }

  private selectObject(object: DataExplorerObjectSignal): void {
    this.optimisticExplore = null
    this.closeFilter()
    const currentExplore = this.dataExplorer?.explore?.command ?? emptyExplorer.explore.command
    const tableID = objectTableID(object)
    const localDimensions = localPreviewDimensions(object, this.dataExplorer?.explore?.fields ?? [])
    const semanticActive = this.dataExplorer?.command?.mode === 'explore'
    const explore: DataExploreCommand = {
      ...currentExplore,
      modelId: object.modelId ?? '',
      datasetId: tableID,
      dimensions: localDimensions,
      metrics: [],
      filters: [],
      sort: [],
      requestSeq: 0,
      resetVersion: 0,
      columnWidths: {},
    }
    this.emitCommand({
      mode: semanticActive ? 'explore' : 'browse',
      explore,
      objectKey: object.key,
      offset: 0,
      limit: 100,
      block: 'all',
      start: 0,
      count: 100,
      requestSeq: 0,
      resetVersion: (this.dataExplorer?.command?.resetVersion ?? 0) + 1,
      sort: {},
      visibleColumns: [],
      columnWidths: {},
    })
  }

  private handleObjectNodeClick(event: MouseEvent, object: DataExplorerObjectSignal, selected: boolean): void {
    const path = event.composedPath()
    if (path.some((target) => target instanceof HTMLElement && target.classList.contains('object-expand'))) return
    event.preventDefault()
    if (!selected || this.dataExplorer.command?.mode === 'explore' || this.optimisticExplore) this.selectObject(object)
  }

  private renderSchema(object: DataExplorerObjectSignal, columns: NonNullable<DataExplorerObjectSignal['columns']>) {
    return html`
      <section class="schema-view" aria-label="Schema">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Data layer</dt><dd>${layerLabel(object.layer)}</dd></div>
          <div class="metadata-card"><dt>Project generation</dt><dd>${label(this.page?.context?.projectId)} · ${label(this.page?.context?.generationId)}</dd></div>
          <div class="metadata-card"><dt>Model</dt><dd>${label(object.modelId)}</dd></div>
          <div class="metadata-card"><dt>Grain</dt><dd>${object.grain ? label(object.grain) : 'Not declared'}</dd></div>
          ${object.description ? html`<div class="metadata-card"><dt>Description</dt><dd>${object.description}</dd></div>` : nothing}
        </dl>
        <table class="schema-table">
          <thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Key</th><th>Default</th><th>Description</th></tr></thead>
          <tbody>
            ${columns.map((column) => html`
              <tr>
                <td><code>${column.label || column.key}</code>${column.label !== column.key ? html`<div class="schema-muted">${column.key}</div>` : nothing}</td>
                <td><code>${column.type || '—'}</code></td>
                <td>${column.nullable === undefined ? 'Unknown' : column.nullable ? 'Yes' : 'No'}</td>
                <td>${column.primaryKey ? 'Primary key' : '—'}</td>
                <td><code>${column.defaultValue || '—'}</code></td>
                <td class=${column.description ? '' : 'schema-muted'}>${column.description || 'No description'}</td>
              </tr>
            `)}
          </tbody>
        </table>
      </section>
    `
  }

  private renderQueryDetails(object: DataExplorerObjectSignal, preview: DataPreviewSignal) {
    return html`
      <section class="query-view" aria-label="Query details">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Query target</dt><dd>${queryTargetLabel(object)}</dd></div>
          <div class="metadata-card"><dt>Rows</dt><dd>${label(preview.totalRowLabel || object.rowCountLabel)}</dd></div>
          <div class="metadata-card"><dt>Sort</dt><dd>${preview.sort?.column ? `${preview.sort.column} ${preview.sort.direction || 'ascending'}` : 'Source order'}</dd></div>
        </dl>
        <h3 class="query-heading">${lucideIcon(Code2, { size: 17 })} Generated SQL</h3>
        <p class="query-copy">This is the governed query executed for the current preview. Sorting and pagination are applied by the explorer.</p>
        <pre class="query-code">${preview.sql || 'No SQL is available for this preview.'}</pre>
      </section>
    `
  }

  private emitCommand(partial: Partial<DataExplorerCommand>) {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    const next: DataExplorerCommand = {
      mode: partial.mode ?? current.mode ?? 'browse',
      explore: partial.explore ?? current.explore ?? this.dataExplorer?.explore?.command,
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
    if (!this.embedded && (partial.objectKey !== undefined || partial.mode !== undefined || partial.explore !== undefined)) {
      replaceDataExplorerURL(next)
    }
    this.dispatchEvent(new CustomEvent('lv-data-explorer-command', { bubbles: true, composed: true, detail: next }))
  }
}

function localPreviewDimensions(object: DataExplorerObjectSignal, fields: DataExploreFieldSignal[]): string[] {
  const tableID = objectTableID(object)
  const localFields = fields.filter((field) => field.kind !== 'metric' && field.modelTable === tableID)
  const localByColumn = new Map(localFields.map((field) => [fieldColumnID(field), field.id]))
  const ordered = (object.columns ?? []).map((column) => localByColumn.get(column.key) ?? `${tableID}.${column.key}`)
  const seen = new Set(ordered)
  for (const field of localFields) {
    if (!seen.has(field.id)) ordered.push(field.id)
  }
  return ordered
}

function objectTableID(object: DataExplorerObjectSignal): string {
  return object.table?.trim() || object.title.trim()
}

function fieldColumnID(field: DataExploreFieldSignal): string {
  const parts = field.id.split('.')
  return parts[parts.length - 1] || field.id
}

function exploreContextMatchesObject(command: DataExploreCommand, object: DataExplorerObjectSignal): boolean {
  return command.modelId === (object.modelId ?? '')
}

function filterObjects(objects: DataExplorerObjectSignal[], query: string): DataExplorerObjectSignal[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return objects
  return objects.filter((object) => objectSearchValues(object)
    .some((value) => value.toLowerCase().includes(normalized)))
}

function objectSearchValues(object: DataExplorerObjectSignal): string[] {
  return [
    object.title,
    object.description,
    object.layer,
    object.resourceId,
    object.modelId,
    object.table,
    ...(object.columns ?? []).flatMap((column) => [column.key, column.label, column.type, column.description]),
  ].map((value) => String(value ?? ''))
}

function objectColumnMatchesSearch(object: DataExplorerObjectSignal, query: string): boolean {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return false
  return (object.columns ?? []).some((column) => [column.key, column.label, column.type, column.description]
    .some((value) => String(value ?? '').toLowerCase().includes(normalized)))
}

function groupObjectsByModel(objects: DataExplorerObjectSignal[], models: DataExploreSignal['models'] = []): ResourceGroup[] {
  const groups = new Map<string, ResourceGroup>()
  const modelTitles = new Map(models.map((model) => [model.id, model.title]))
  for (const object of objects) {
    if (object.layer === 'source') continue
    const id = object.modelId || object.layer
    if (!groups.has(id)) {
      groups.set(id, { id, title: modelTitles.get(id) || object.modelId || 'Data objects', objects: [] })
    }
    groups.get(id)!.objects.push(object)
  }
  return Array.from(groups.values()).filter((group) => group.objects.length > 0)
}

type ExploreFieldGroup = {
  id: string
  kind: 'dimension' | 'metric'
  label: string
  fields: DataExploreFieldSignal[]
}

function groupExploreFields(fields: DataExploreFieldSignal[]): ExploreFieldGroup[] {
  const groups = new Map<string, ExploreFieldGroup>()
  for (const field of fields) {
    const crossDatasetMetric = field.kind === 'metric' && !field.modelTable
    const id = crossDatasetMetric ? 'cross-dataset:metric' : `${field.modelTable}:${field.kind}`
    if (!groups.has(id)) {
      groups.set(id, {
        id,
        kind: field.kind,
        label: crossDatasetMetric ? 'Multiple datasets · Metrics' : `${label(field.modelTable)} · ${field.kind === 'metric' ? 'Metrics' : 'Dimensions'}`,
        fields: [],
      })
    }
    groups.get(id)!.fields.push(field)
  }
  return Array.from(groups.values())
}

function fieldLabel(id: string, fields: DataExploreFieldSignal[]): string {
  return fields.find((field) => field.id === id)?.label ?? label(id)
}

function replaceDataExplorerURL(command: DataExplorerCommand) {
  if (typeof window === 'undefined') return
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (mode === 'explore') {
    params.set('mode', 'explore')
    if (command.explore?.modelId) params.set('model', command.explore.modelId)
    if (command.explore?.datasetId) params.set('dataset', command.explore.datasetId)
    for (const field of command.explore?.dimensions ?? []) params.append('dimension', field)
    for (const metric of command.explore?.metrics ?? []) params.append('metric', metric)
    for (const filter of command.explore?.filters ?? []) params.append('filter', JSON.stringify(filter))
    for (const sort of command.explore?.sort ?? []) params.append('sort', JSON.stringify(sort))
    if (command.explore?.time) params.set('time', JSON.stringify(command.explore.time))
    if (command.explore?.limit && command.explore.limit !== 100) params.set('limit', String(command.explore.limit))
  } else if (objectKey) {
    params.set('object', objectKey)
  }
  const next = params.toString() ? `/explore?${params.toString()}` : '/explore'
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

function queryTargetLabel(object: DataExplorerObjectSignal): string {
  const target = object.source || object.table || object.title
  const model = object.modelId ? `${object.modelId} · ` : ''
  return `${layerLabel(object.layer)} · ${model}${target}`
}

function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}

function clampBrowserWidth(value: number): number {
  return Math.min(440, Math.max(280, Math.round(value)))
}

if (!customElements.get('lv-data-explorer')) customElements.define('lv-data-explorer', DataExplorerPage)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer': DataExplorerPage
  }
}
