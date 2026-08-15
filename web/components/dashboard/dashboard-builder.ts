import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type {
  DashboardBuilderDiagnosticSignal,
  DashboardBuilderFieldSignal,
  DashboardBuilderPageSignal,
  DashboardBuilderSignal,
  DashboardBuilderTableSignal,
  DashboardBuilderVisualSignal,
  DashboardBuilderVisualSlotSignal,
  DashboardStatus,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'

const emptyStatus: DashboardStatus = {
  loading: false,
  error: '',
  generation: 0,
  lastUpdated: '',
  refreshId: '',
  setupRequired: false,
  progressPercent: 100,
}

/** Draft dashboard authoring surface. Runtime dashboard rendering remains a
 * separate component and envelope; this component only edits the bounded
 * builder projection delivered by the stream. */
class LeapViewDashboardBuilder extends DatastarLit(LitElement) {
  @property({ attribute: 'back-href' }) backHref = ''
  @property({ attribute: 'preview-href' }) previewHref = ''
  @property({ attribute: 'export-yaml-href' }) exportYAMLHref = ''

  @state() private fieldQuery = ''
  @state() private localPageID = ''
  @state() private localVisualID = ''

  static styles = css`
    :host {
      display: block;
      min-height: 100svh;
      color: var(--lv-fg-default, #1f2328);
      background: var(--lv-bg-app, #f6f8fa);
      font-family: var(--fontStack-system, system-ui, sans-serif);
    }

    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .builder {
      display: grid;
      min-height: 100svh;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .toolbar {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      min-height: 3.75rem;
      padding: 0.6rem 1rem;
      border-bottom: 1px solid var(--lv-border-muted, #d8dee4);
      background: var(--lv-bg-panel, #fff);
    }

    .back {
      color: inherit;
      font-size: 0.875rem;
      text-decoration: none;
      white-space: nowrap;
    }

    .back:focus-visible,
    button:focus-visible,
    input:focus-visible,
    [role='button']:focus-visible {
      outline: 2px solid var(--lv-accent-emphasis, #0969da);
      outline-offset: 2px;
    }

    .title-wrap {
      min-width: 0;
      margin-right: auto;
    }

    .title {
      margin: 0;
      overflow: hidden;
      font-size: 1rem;
      font-weight: 650;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .meta {
      display: flex;
      flex-wrap: wrap;
      gap: 0.3rem;
      margin-top: 0.15rem;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.7rem;
    }

    .badge {
      display: inline-flex;
      align-items: center;
      border: 1px solid var(--lv-border-muted, #d8dee4);
      border-radius: 999px;
      padding: 0.12rem 0.45rem;
      background: var(--lv-bg-subtle, #f6f8fa);
      white-space: nowrap;
    }

    .badge.draft,
    .badge.dirty {
      border-color: #bf8700;
      color: #7a4e00;
      background: #fff8c5;
    }

    .badge.shared {
      border-color: #54aeff;
      color: #0550ae;
      background: #ddf4ff;
    }

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: 0.4rem;
    }

    button,
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 2rem;
      border: 1px solid var(--lv-border-default, #d0d7de);
      border-radius: 0.35rem;
      padding: 0.35rem 0.65rem;
      color: inherit;
      background: var(--lv-bg-panel, #fff);
      font: inherit;
      font-size: 0.78rem;
      text-decoration: none;
      cursor: pointer;
    }

    button:hover,
    .button:hover {
      background: var(--lv-bg-subtle, #f6f8fa);
    }

    button.primary {
      border-color: #1f883d;
      color: #fff;
      background: #1f883d;
    }

    button.primary:hover {
      background: #1a7f37;
    }

    button:disabled {
      cursor: not-allowed;
      opacity: 0.55;
    }

    .body {
      display: grid;
      min-height: 0;
      grid-template-columns: minmax(13rem, 17rem) minmax(0, 1fr) minmax(15rem, 20rem);
    }

    .pane {
      min-width: 0;
      overflow: auto;
      background: var(--lv-bg-panel, #fff);
    }

    .fields {
      border-right: 1px solid var(--lv-border-muted, #d8dee4);
    }

    .properties {
      border-left: 1px solid var(--lv-border-muted, #d8dee4);
    }

    .pane-header {
      position: sticky;
      top: 0;
      z-index: 1;
      padding: 0.9rem 0.85rem 0.6rem;
      border-bottom: 1px solid var(--lv-border-muted, #d8dee4);
      background: var(--lv-bg-panel, #fff);
    }

    .pane-title {
      margin: 0;
      font-size: 0.8rem;
      font-weight: 650;
    }

    .pane-hint {
      margin: 0.25rem 0 0;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.7rem;
      line-height: 1.4;
    }

    .search {
      width: 100%;
      box-sizing: border-box;
      margin-top: 0.65rem;
      border: 1px solid var(--lv-border-default, #d0d7de);
      border-radius: 0.3rem;
      padding: 0.45rem 0.55rem;
      color: inherit;
      background: var(--lv-bg-default, #fff);
      font: inherit;
      font-size: 0.78rem;
    }

    .table {
      border-bottom: 1px solid var(--lv-border-muted, #d8dee4);
    }

    .table summary {
      padding: 0.6rem 0.85rem;
      color: var(--lv-fg-default, #1f2328);
      font-size: 0.75rem;
      font-weight: 600;
      cursor: pointer;
      list-style-position: inside;
    }

    .field-list {
      display: grid;
      gap: 0.2rem;
      padding: 0 0.6rem 0.6rem;
    }

    .field {
      display: flex;
      align-items: center;
      gap: 0.4rem;
      width: 100%;
      box-sizing: border-box;
      border: 1px solid transparent;
      border-radius: 0.3rem;
      padding: 0.38rem 0.45rem;
      color: inherit;
      background: transparent;
      text-align: left;
      cursor: grab;
    }

    .field:hover {
      border-color: var(--lv-border-muted, #d8dee4);
      background: var(--lv-bg-subtle, #f6f8fa);
    }

    .field-kind {
      width: 1.2rem;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.65rem;
      text-align: center;
    }

    .field-label {
      min-width: 0;
      overflow: hidden;
      font-size: 0.76rem;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-type {
      margin-left: auto;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.64rem;
    }

    .canvas-pane {
      display: grid;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      background: var(--lv-bg-app, #f6f8fa);
    }

    .page-tabs {
      display: flex;
      align-items: center;
      gap: 0.3rem;
      overflow-x: auto;
      padding: 0.55rem 0.75rem;
      border-bottom: 1px solid var(--lv-border-muted, #d8dee4);
      background: var(--lv-bg-panel, #fff);
    }

    .page-tab {
      flex: 0 0 auto;
      border-color: transparent;
      background: transparent;
    }

    .page-tab[aria-selected='true'] {
      border-color: var(--lv-border-default, #d0d7de);
      background: var(--lv-bg-subtle, #f6f8fa);
      font-weight: 600;
    }

    .canvas-scroll {
      overflow: auto;
      padding: 1.2rem;
    }

    .canvas {
      position: relative;
      min-width: 38rem;
      min-height: 30rem;
      border: 1px solid var(--lv-border-muted, #d8dee4);
      border-radius: 0.45rem;
      background-color: #fff;
      background-image: linear-gradient(to right, rgba(9, 105, 218, 0.07) 1px, transparent 1px), linear-gradient(to bottom, rgba(9, 105, 218, 0.07) 1px, transparent 1px);
      background-size: 8.333% 2.5rem;
      box-shadow: 0 2px 8px rgba(31, 35, 40, 0.06);
    }

    .visual {
      position: absolute;
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      min-width: 4rem;
      min-height: 3rem;
      box-sizing: border-box;
      border: 1px solid var(--lv-border-default, #d0d7de);
      border-radius: 0.35rem;
      padding: 0.55rem;
      color: inherit;
      background: rgba(255, 255, 255, 0.96);
      text-align: left;
      cursor: pointer;
    }

    .visual:hover,
    .visual[aria-pressed='true'] {
      border-color: var(--lv-accent-emphasis, #0969da);
      box-shadow: 0 0 0 2px rgba(9, 105, 218, 0.15);
    }

    .visual-title {
      overflow: hidden;
      font-size: 0.76rem;
      font-weight: 600;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type {
      margin-top: 0.2rem;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.68rem;
    }

    .visual-empty {
      display: grid;
      place-items: center;
      min-height: 20rem;
      color: var(--lv-fg-muted, #656d76);
      text-align: center;
    }

    .visual-empty strong {
      display: block;
      margin-bottom: 0.3rem;
      color: var(--lv-fg-default, #1f2328);
    }

    .properties-body {
      display: grid;
      gap: 1rem;
      padding: 0.85rem;
    }

    .property-group {
      display: grid;
      gap: 0.35rem;
    }

    .property-label {
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.68rem;
      font-weight: 600;
      letter-spacing: 0.02em;
      text-transform: uppercase;
    }

    .property-value {
      font-size: 0.8rem;
    }

    .slot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      border: 1px solid var(--lv-border-muted, #d8dee4);
      border-radius: 0.3rem;
      padding: 0.45rem;
      font-size: 0.74rem;
    }

    .slot-kind {
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.66rem;
    }

    .diagnostics {
      display: grid;
      gap: 0.35rem;
    }

    .diagnostic {
      border-left: 3px solid var(--lv-fg-muted, #656d76);
      padding: 0.35rem 0.5rem;
      background: var(--lv-bg-subtle, #f6f8fa);
      font-size: 0.72rem;
    }

    .diagnostic.error {
      border-color: #cf222e;
    }

    .diagnostic.warning {
      border-color: #bf8700;
    }

    .diagnostic.info {
      border-color: #0969da;
    }

    .evidence {
      display: grid;
      gap: 0.3rem;
      color: var(--lv-fg-muted, #656d76);
      font-size: 0.7rem;
    }

    .state {
      display: grid;
      place-items: center;
      min-height: 60svh;
      padding: 2rem;
      color: var(--lv-fg-muted, #656d76);
      text-align: center;
    }

    .state strong {
      display: block;
      margin-bottom: 0.35rem;
      color: var(--lv-fg-default, #1f2328);
    }

    @media (max-width: 960px) {
      .toolbar {
        flex-wrap: wrap;
      }

      .title-wrap {
        min-width: 10rem;
      }

      .body {
        grid-template-columns: minmax(12rem, 16rem) minmax(0, 1fr);
      }

      .properties {
        grid-column: 1 / -1;
        max-height: 19rem;
        border-top: 1px solid var(--lv-border-muted, #d8dee4);
        border-left: 0;
      }
    }

    @media (max-width: 640px) {
      .body {
        display: block;
      }

      .pane {
        max-height: none;
        border: 0;
        border-bottom: 1px solid var(--lv-border-muted, #d8dee4);
      }

      .fields,
      .properties {
        max-height: 20rem;
      }

      .canvas-pane {
        min-height: 38rem;
      }

      .toolbar-actions {
        width: 100%;
        overflow-x: auto;
      }
    }
  `

  updated(): void {
    checkSignalContract('dashboard builder', this.builder, {
      workspaceId: 'required',
      dashboardId: 'required',
      draftId: 'required',
      revision: 'required',
      semanticModel: 'required',
      pages: 'required',
      capabilities: 'required',
      diagnostics: 'required',
      preview: 'required',
      save: 'required',
    })
  }

  get builder(): DashboardBuilderSignal | null {
    return this.signal<DashboardBuilderSignal | null>('builder', null)
  }

  get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyStatus)
  }

  render() {
    const builder = this.builder
    if (!builder) {
      const status = this.status
      if (status.error) {
        return html`<section class="state" role="alert" aria-live="assertive">
          <div><strong>Dashboard builder could not load</strong><span>${status.error}</span></div>
        </section>`
      }
      return html`<section class="state" aria-live="polite"><div><strong>Loading dashboard builder…</strong><span>Preparing the draft workspace.</span></div></section>`
    }
    const page = this.selectedPage(builder)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    return html`
      <section class="builder" aria-label="Dashboard builder">
        ${this.renderToolbar(builder)}
        <div class="body">
          ${this.renderFieldPane(builder)}
          ${this.renderCanvas(builder, page)}
          ${this.renderProperties(builder, page, visual)}
        </div>
      </section>
    `
  }

  private renderToolbar(builder: DashboardBuilderSignal) {
    const saveState = builder.save.state
    const canSave = builder.capabilities.canEdit && saveState !== 'saving'
    return html`
      <header class="toolbar">
        ${this.backHref ? html`<a class="back" href=${this.backHref} aria-label="Back to dashboard">← Back</a>` : html`<span class="back" aria-label="Back to dashboard">← Back</span>`}
        <div class="title-wrap">
          <h1 class="title">${builder.title}</h1>
          <div class="meta" aria-label="Dashboard draft metadata">
            <span class="badge">${builder.origin.label}</span>
            <span class="badge ${builder.visibility}">${builder.visibility}</span>
            <span class="badge ${builder.lifecycle}">${builder.lifecycle}</span>
            <span class="badge" title=${builder.revision.id}>Revision ${builder.revision.number}</span>
            <span class="badge ${builder.hasUnpublishedChanges || saveState === 'dirty' ? 'dirty' : ''}" aria-live="polite">${this.saveLabel(builder)}</span>
          </div>
        </div>
        <div class="toolbar-actions" aria-label="Builder actions">
          ${(this.previewHref || builder.preview.href) && builder.capabilities.canPreview
            ? html`<a class="button" href=${this.previewHref || builder.preview.href}>Preview</a>`
            : builder.capabilities.canPreview ? html`<button disabled title="Preview is not available yet">Preview</button>` : nothing}
          ${builder.capabilities.canShare ? html`<button @click=${this.share}>Share</button>` : nothing}
          ${builder.capabilities.canExport
            ? this.exportYAMLHref ? html`<a class="button" href=${this.exportYAMLHref} download>Export YAML</a>` : html`<button disabled title="YAML export is not available yet">Export YAML</button>`
            : nothing}
          ${builder.capabilities.canPublish ? html`<button class="primary" @click=${this.publish}>Publish</button>` : nothing}
          ${builder.capabilities.canEdit ? html`<button @click=${this.save} ?disabled=${!canSave}>${saveState === 'saving' ? 'Saving…' : 'Save draft'}</button>` : nothing}
        </div>
      </header>
    `
  }

  private renderFieldPane(builder: DashboardBuilderSignal) {
    const tables = this.filteredTables(builder.semanticModel.tables)
    return html`
      <aside class="pane fields" aria-label="Semantic model fields">
        <div class="pane-header">
          <h2 class="pane-title">${builder.semanticModel.title}</h2>
          <p class="pane-hint">Drag fields onto a visual, or use Add to place them in the selected slot.</p>
          <label>
            <span class="sr-only">Search fields</span>
            <input class="search" type="search" placeholder="Search fields" .value=${this.fieldQuery} @input=${this.onFieldQuery} />
          </label>
        </div>
        ${tables.length === 0
          ? html`<p class="pane-hint" style="padding: 0.85rem">No fields match this search.</p>`
          : tables.map((table) => this.renderTable(table))}
      </aside>
    `
  }

  private renderTable(table: DashboardBuilderTableSignal) {
    return html`
      <details class="table" open>
        <summary>${table.title}<span class="field-type">${table.fields.length}</span></summary>
        <div class="field-list">
          ${table.fields.map((field) => html`
            <button class="field" draggable="true" aria-label="Add ${field.label}" @click=${() => this.addField(field)} @dragstart=${(event: DragEvent) => this.dragField(event, field)}>
              <span class="field-kind" aria-hidden="true">${field.kind === 'measure' ? '∑' : '◇'}</span>
              <span class="field-label">${field.label}</span>
              <span class="field-type">${field.dataType}</span>
            </button>
          `)}
        </div>
      </details>
    `
  }

  private renderCanvas(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    if (!page) {
      return html`<main class="canvas-pane" aria-label="Dashboard canvas"><div class="state"><div><strong>No pages yet</strong><span>Create a page to start designing this dashboard.</span></div></div></main>`
    }
    const width = Math.max(12, page.grid.columns || 12)
    return html`
      <main class="canvas-pane" aria-label="Dashboard canvas">
        <nav class="page-tabs" aria-label="Dashboard pages" role="tablist">
          ${builder.pages.map((item) => html`<button class="page-tab" role="tab" aria-selected=${item.id === page.id} @click=${() => this.selectPage(item.id)}>${item.title}</button>`)}
          ${builder.capabilities.canAddPage ? html`<button class="page-tab" aria-label="Add page" @click=${this.addPage}>＋</button>` : nothing}
        </nav>
        <div class="canvas-scroll">
          <div class="canvas" style=${`aspect-ratio: ${page.canvas.width || 16} / ${page.canvas.height || 9}; grid-template-columns: repeat(${width}, 1fr);`} @dragover=${(event: DragEvent) => event.preventDefault()} @drop=${this.dropField}>
            ${page.visuals.length === 0
              ? html`<div class="visual-empty"><div><strong>This page is empty</strong><span>Drag a field here or add a visual to begin.</span>${builder.capabilities.canAddVisual ? html`<div><button @click=${this.addVisual}>Add visual</button></div>` : nothing}</div></div>`
              : page.visuals.map((visual) => this.renderVisual(visual, page))}
          </div>
        </div>
      </main>
    `
  }

  private renderVisual(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal) {
    const selected = visual.id === this.effectiveVisualID(this.builder, page)
    const left = `${Math.max(0, visual.placement.col - 1) * (100 / Math.max(1, page.grid.columns))}%`
    const top = `${Math.max(0, visual.placement.row - 1) * (page.grid.rowHeight || 40)}px`
    const width = `${Math.max(1, visual.placement.colSpan) * (100 / Math.max(1, page.grid.columns))}%`
    const height = `${Math.max(1, visual.placement.rowSpan) * (page.grid.rowHeight || 40)}px`
    return html`
      <button class="visual" aria-pressed=${selected} aria-label="Select ${visual.title}" style=${`left:${left};top:${top};width:${width};height:${height}`} @click=${() => this.selectVisual(visual.id)}>
        <span class="visual-title">${visual.title}</span>
        <span class="visual-type">${visual.type} · ${visual.slots.length} field slots</span>
      </button>
    `
  }

  private renderProperties(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    return html`
      <aside class="pane properties" aria-label="Properties">
        <div class="pane-header"><h2 class="pane-title">${visual ? 'Visual properties' : 'Page properties'}</h2><p class="pane-hint">Governed fields, formatting, and validation stay attached to the draft.</p></div>
        <div class="properties-body">
          ${visual ? this.renderVisualProperties(visual) : this.renderPageProperties(page)}
          ${this.renderDiagnostics(builder.diagnostics)}
          ${this.renderEvidence(builder)}
        </div>
      </aside>
    `
  }

  private renderVisualProperties(visual: DashboardBuilderVisualSignal) {
    return html`
      <section class="property-group" aria-label="Selected visual">
        <span class="property-label">Visual</span>
        <span class="property-value">${visual.title} · ${visual.type}</span>
      </section>
      <section class="property-group" aria-label="Query fields">
        <span class="property-label">Query slots</span>
        ${visual.slots.length === 0 ? html`<span class="pane-hint">Drop a field into this visual.</span>` : visual.slots.map((slot) => this.renderSlot(slot))}
      </section>
      <section class="property-group" aria-label="Formatting and interactions">
        <span class="property-label">Format &amp; interactions</span>
        <span class="pane-hint">Formatting and cross-filter interactions are configured per visual.</span>
      </section>
      <section class="property-group" aria-label="Visual filters">
        <span class="property-label">Filters</span>
        <span class="pane-hint">${visual.filters.length === 0 ? 'No visual filters' : visual.filters.join(' · ')}</span>
      </section>
    `
  }

  private renderPageProperties(page: DashboardBuilderPageSignal | undefined) {
    if (!page) return html`<span class="pane-hint">Select a page to edit its properties.</span>`
    return html`
      <section class="property-group"><span class="property-label">Page</span><span class="property-value">${page.title}</span></section>
      <section class="property-group"><span class="property-label">Canvas</span><span class="property-value">${page.canvas.width} × ${page.canvas.height}</span></section>
      <section class="property-group"><span class="property-label">Grid</span><span class="property-value">${page.grid.columns} columns · ${page.grid.rowHeight}px rows</span></section>
    `
  }

  private renderSlot(slot: DashboardBuilderVisualSlotSignal) {
    return html`<div class="slot"><span>${slot.label}${slot.required ? ' · required' : ''}</span><span class="slot-kind">${slot.fieldId || slot.kind}</span></div>`
  }

  private renderDiagnostics(diagnostics: DashboardBuilderDiagnosticSignal[]) {
    if (diagnostics.length === 0) return nothing
    return html`<section class="property-group" aria-label="Validation diagnostics"><span class="property-label">Validation</span><div class="diagnostics">${diagnostics.map((item) => html`<div class="diagnostic ${item.severity}" role=${item.severity === 'error' ? 'alert' : 'status'}><strong>${item.code}</strong> ${item.message}</div>`)}</div></section>`
  }

  private renderEvidence(builder: DashboardBuilderSignal) {
    const evidence = builder.sourceEvidence
    if (!evidence) return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>Not available</span></div></section>`
    if (evidence.kind === 'workspace' && evidence.workspaceId && evidence.dashboardId && evidence.revision) {
      return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>workspace · ${evidence.workspaceId}/${evidence.dashboardId} · ${evidence.revision.id} · ${evidence.revision.number} · ${evidence.revision.contentHash}</span></div></section>`
    }
    if (evidence.kind === 'project' && evidence.workspaceId && evidence.dashboardId && evidence.servingStateId) {
      return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>project · ${evidence.workspaceId}/${evidence.dashboardId} · ${evidence.servingStateId}${evidence.path ? ` · ${evidence.path}` : ''}</span></div></section>`
    }
    return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>Unavailable</span></div></section>`
  }

  private filteredTables(tables: DashboardBuilderTableSignal[]): DashboardBuilderTableSignal[] {
    const query = this.fieldQuery.trim().toLowerCase()
    if (!query) return tables
    return tables.map((table) => ({ ...table, fields: table.fields.filter((field) => `${field.label} ${field.id} ${field.dataType}`.toLowerCase().includes(query)) })).filter((table) => table.title.toLowerCase().includes(query) || table.fields.length > 0)
  }

  private selectedPage(builder: DashboardBuilderSignal): DashboardBuilderPageSignal | undefined {
    const id = this.localPageID || builder.selectedPageId
    return builder.pages.find((page) => page.id === id) ?? builder.pages[0]
  }

  private selectedVisual(page: DashboardBuilderPageSignal, builder: DashboardBuilderSignal): DashboardBuilderVisualSignal | undefined {
    const id = this.effectiveVisualID(builder, page)
    return page.visuals.find((visual) => visual.id === id)
  }

  private effectiveVisualID(builder: DashboardBuilderSignal | null, page: DashboardBuilderPageSignal): string {
    if (this.localVisualID && page.visuals.some((visual) => visual.id === this.localVisualID)) return this.localVisualID
    if (builder?.selectedVisualId && page.visuals.some((visual) => visual.id === builder.selectedVisualId)) return builder.selectedVisualId
    return page.visuals[0]?.id ?? ''
  }

  private save = (): void => this.emitCommand('save')
  private share = (): void => this.emitCommand('share')
  private publish = (): void => this.emitCommand('publish')

  private addPage = (): void => this.emitCommand('add_page')
  private addVisual = (): void => this.emitCommand('add_visual')

  private addField(field: DashboardBuilderFieldSignal): void {
    this.emitCommand('add_field', { fieldId: field.id, fieldKind: field.kind })
  }

  private dropField = (event: DragEvent): void => {
    event.preventDefault()
    const fieldID = event.dataTransfer?.getData('text/leapview-field')
    if (fieldID) this.emitCommand('add_field', { fieldId: fieldID })
  }

  private dragField(event: DragEvent, field: DashboardBuilderFieldSignal): void {
    event.dataTransfer?.setData('text/leapview-field', field.id)
    event.dataTransfer?.setData('text/plain', field.id)
  }

  private selectPage(pageID: string): void {
    this.localPageID = pageID
    this.localVisualID = ''
    this.emit('lv-builder-page-select', { ...this.commandDetail(), pageId: pageID })
  }

  private selectVisual(visualID: string): void {
    this.localVisualID = visualID
    this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: visualID })
  }

  private commandDetail(): Record<string, string> {
    const builder = this.builder
    return {
      workspaceId: builder?.workspaceId ?? '',
      dashboardId: builder?.dashboardId ?? '',
      draftId: builder?.draftId ?? '',
      revisionId: builder?.revision.id ?? '',
      revisionNumber: String(builder?.revision.number ?? 0),
      revisionContentHash: builder?.revision.contentHash ?? '',
      pageId: builder ? (this.selectedPage(builder)?.id ?? '') : '',
      visualId: this.localVisualID,
    }
  }

  private emit(name: string, detail: Record<string, unknown>): void {
    this.dispatchEvent(new CustomEvent(name, { bubbles: true, composed: true, detail }))
  }

  private emitCommand(action: string, detail: Record<string, unknown> = {}): void {
    this.emit('lv-builder-command', { ...this.commandDetail(), action, ...detail })
  }

  private onFieldQuery = (event: Event): void => {
    this.fieldQuery = (event.currentTarget as HTMLInputElement).value
  }

  private saveLabel(builder: DashboardBuilderSignal): string {
    if (builder.save.state === 'saving') return 'Saving…'
    if (builder.save.state === 'error') return builder.save.message || 'Save failed'
    if (builder.save.state === 'dirty') return 'Unsaved changes'
    if (builder.hasUnpublishedChanges) return 'Unpublished draft'
    return builder.save.message || 'Saved'
  }
}

if (!customElements.get('lv-dashboard-builder')) customElements.define('lv-dashboard-builder', LeapViewDashboardBuilder)

declare global {
  interface HTMLElementTagNameMap {
    'lv-dashboard-builder': LeapViewDashboardBuilder
  }
}
