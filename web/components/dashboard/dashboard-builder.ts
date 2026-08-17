import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type {
  DashboardBuilderDiagnosticSignal,
  DashboardBuilderFieldSignal,
  DashboardBuilderPageSignal,
  DashboardBuilderSignal,
  DashboardBuilderDatasetSignal,
  DashboardBuilderVisualSignal,
  DashboardBuilderVisualSlotSignal,
  DashboardVisualizationSignal,
  DashboardStatus,
} from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import './visualization/host'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'

const emptyStatus: DashboardStatus = {
  loading: false,
  error: '',
  generation: 0,
  lastUpdated: '',
  refreshId: '',
  setupRequired: false,
  progressPercent: 100,
}

// Keep the first picker intentionally small and accessible. These are the
// established chart/table types with useful empty-draft defaults; the closed
// server registry remains authoritative for future visual types.
const builderVisualTypes = ['bar', 'line', 'area', 'column', 'table'] as const

type DashboardBuilderVisualWithPreview = DashboardBuilderVisualSignal & { visualId?: string }

/** Draft dashboard authoring surface. Runtime dashboard rendering remains a
 * separate component and envelope; this component only edits the bounded
 * builder projection delivered by the stream. */
class LeapViewDashboardBuilder extends DatastarLit(LitElement) {
  @property({ attribute: 'back-href' }) backHref = ''
  @property({ attribute: 'page-base-href' }) pageBaseHref = ''
  @property({ attribute: 'preview-href' }) previewHref = ''
  @property({ attribute: 'export-yaml-href' }) exportYAMLHref = ''

  @state() private fieldQuery = ''
  @state() private localPageID = ''
  @state() private localVisualID = ''
  @state() private visualType = 'bar'
  private readonly visualizationDecoder = new DashboardVisualizationSignalDecoder()

  // Add-page uses server-generated identifiers. Keep the page set that was
  // visible when the intent was sent so the authoritative response can select
  // the page created by that intent, even when the response's selectedPageId
  // still reflects the page that was active before the mutation.
  private pendingAddPage: { revision: string; pageIDs: Set<string> } | null = null

  static styles = css`
    :host {
      display: block;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
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
      gap: var(--base-size-12);
      min-height: var(--control-medium-size);
      padding: var(--base-size-8) var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .back {
      color: inherit;
      font: var(--lv-type-body-compact);
      text-decoration: none;
      white-space: nowrap;
    }

    .back:focus-visible,
    button:focus-visible,
    input:focus-visible,
    [role='button']:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .title-wrap {
      min-width: 0;
      margin-right: auto;
    }

    .title {
      margin: 0;
      overflow: hidden;
      font: var(--lv-type-section-title, var(--lv-type-body));
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .meta {
      display: flex;
      align-items: center;
      gap: var(--base-size-6);
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .meta::before {
      width: var(--base-size-6);
      height: var(--base-size-6);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
      content: '';
    }

    .meta[data-state='dirty']::before,
    .meta[data-state='saving']::before,
    .meta[data-state='error']::before {
      background: var(--lv-fg-warning);
    }

    .meta[data-state='saved']::before {
      background: var(--lv-fg-success);
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
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      text-decoration: none;
      cursor: pointer;
    }

    button:hover,
    .button:hover {
      background: var(--lv-button-bg-hover);
    }

    button.primary {
      border-color: var(--lv-button-accent-border-rest);
      color: var(--lv-button-accent-fg-rest);
      background: var(--lv-button-accent-bg-rest);
    }

    button.primary:hover {
      background: var(--lv-button-accent-bg-hover);
    }

    .more-actions {
      position: relative;
    }

    .more-actions summary {
      display: inline-flex;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      align-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      cursor: pointer;
      list-style: none;
    }

    .more-actions summary::-webkit-details-marker {
      display: none;
    }

    .more-actions summary:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .more-actions summary:hover {
      background: var(--lv-button-bg-hover);
    }

    .more-menu {
      position: absolute;
      z-index: 2;
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      min-width: 10rem;
      gap: var(--base-size-2);
      padding: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .more-menu button,
    .more-menu .button {
      justify-content: flex-start;
      width: 100%;
      border-color: transparent;
      background: transparent;
      text-align: left;
    }

    .more-menu button:hover,
    .more-menu .button:hover {
      background: var(--lv-bg-panel-muted);
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
      background: var(--lv-bg-panel);
    }

    .fields {
      border-right: var(--lv-border-muted);
    }

    .properties {
      border-left: var(--lv-border-muted);
    }

    .pane-header {
      position: sticky;
      top: 0;
      z-index: 1;
      padding: 0.9rem 0.85rem 0.6rem;
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .pane-title {
      margin: 0;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .pane-hint {
      margin: 0.25rem 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: 1.4;
    }

    .search {
      width: 100%;
      box-sizing: border-box;
      min-height: var(--control-small-size);
      margin-top: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: var(--lv-fg-default);
      background: var(--lv-bg-input, var(--lv-bg-control));
      font: var(--lv-type-body-compact);
    }

    .table {
      border-bottom: var(--lv-border-muted);
    }

    .table summary {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      padding: 0.6rem 0.85rem;
      color: var(--lv-fg-default);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
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
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      min-height: var(--control-small-size);
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: inherit;
      background: transparent;
      text-align: left;
      cursor: grab;
    }

    .field:hover {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

    .field-kind {
      width: 1.2rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .field-label {
      min-width: 0;
      overflow: hidden;
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-type {
      margin-left: auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .canvas-pane {
      display: grid;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .page-tabs {
      display: flex;
      align-items: center;
      gap: var(--base-size-4);
      overflow-x: auto;
      padding: var(--base-size-6) var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .page-tab {
      flex: 0 0 auto;
      min-height: var(--control-small-size);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: inherit;
      border-color: transparent;
      background: transparent;
      font: inherit;
      text-decoration: none;
      cursor: pointer;
    }

    .page-tab[aria-selected='true'] {
      border-color: var(--lv-line-default);
      background: var(--lv-bg-panel-muted);
      font-weight: var(--base-text-weight-semibold);
    }

    .canvas-scroll {
      position: relative;
      overflow: auto;
      min-width: 0;
      padding: 0;
    }

    .canvas {
      position: relative;
      width: max(100%, 38rem);
      min-width: 38rem;
      min-height: 30rem;
      border: 0;
      border-radius: 0;
      background-color: var(--lv-bg-panel);
      background-image: linear-gradient(to right, color-mix(in srgb, var(--lv-fg-accent) 7%, transparent) 1px, transparent 1px), linear-gradient(to bottom, color-mix(in srgb, var(--lv-fg-accent) 7%, transparent) 1px, transparent 1px);
      background-size: 8.333% 2.5rem;
      box-shadow: none;
    }

    .add-visual {
      display: flex;
      align-items: center;
      gap: var(--base-size-6);
      min-height: var(--control-small-size);
      box-sizing: border-box;
      padding: var(--base-size-6) var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      font: var(--lv-type-caption);
    }

    .add-visual label {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-6);
      color: var(--lv-fg-muted);
    }

    .add-visual select {
      min-height: var(--control-small-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: var(--lv-fg-default);
      background: var(--lv-bg-control, var(--lv-bg-panel));
      font: var(--lv-type-caption);
    }

    .add-visual button {
      min-height: var(--control-small-size);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
    }

    .preview-error {
      margin: 0;
      padding: var(--base-size-8) var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      color: var(--lv-fg-danger, var(--lv-fg-muted));
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel));
      font: var(--lv-type-caption);
    }

    .visual {
      position: absolute;
      display: grid;
      grid-template-rows: auto minmax(0, 1fr) auto;
      min-width: 4rem;
      min-height: 3rem;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-8);
      color: inherit;
      background: color-mix(in srgb, var(--lv-bg-panel) 96%, transparent);
      text-align: left;
      cursor: pointer;
    }

    .visual.has-preview {
      grid-template-rows: minmax(0, 1fr);
      padding: 0;
      overflow: hidden;
    }

    .visual:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .visual:hover,
    .visual[aria-pressed='true'] {
      border-color: var(--lv-fg-accent);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-accent-muted);
    }

    .visual-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type {
      margin-top: 0.2rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .visual-preview {
      display: block;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      pointer-events: none;
    }

    .visual-preview lv-visualization-host {
      display: block;
      width: 100%;
      height: 100%;
    }

    .visual-preview-empty {
      display: grid;
      min-height: 0;
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .visual-empty {
      display: grid;
      place-items: center;
      min-height: 20rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .visual-empty strong {
      display: block;
      margin-bottom: 0.3rem;
      color: var(--lv-fg-default);
    }

    .properties-body {
      display: grid;
      gap: var(--base-size-12);
      padding: var(--base-size-12);
    }

    .property-group {
      display: grid;
      gap: 0.35rem;
    }

    .property-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .property-value {
      font: var(--lv-type-body-compact);
    }

    .slot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: var(--base-size-6);
      font: var(--lv-type-body-compact);
    }

    .slot-kind {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .diagnostics {
      display: grid;
      gap: 0.35rem;
    }

    .diagnostic {
      border-left: 3px solid var(--lv-fg-muted);
      padding: 0.35rem 0.5rem;
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .diagnostic.error {
      border-color: var(--lv-fg-danger);
    }

    .diagnostic.warning {
      border-color: var(--lv-fg-warning);
    }

    .diagnostic.info {
      border-color: var(--lv-fg-accent);
    }

    .evidence {
      display: grid;
      gap: 0.3rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .secondary-details {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .secondary-details summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .secondary-details-content {
      display: grid;
      gap: var(--base-size-12);
      padding-top: var(--base-size-8);
    }

    .state {
      display: grid;
      place-items: center;
      min-height: 60svh;
      padding: 2rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .state strong {
      display: block;
      margin-bottom: 0.35rem;
      color: var(--lv-fg-default);
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
        border-top: var(--lv-border-muted);
        border-left: 0;
      }
    }

    @media (max-width: 640px) {
      :host {
        height: 100%;
        max-height: 100svh;
        overflow-y: auto;
      }

      .builder {
        min-height: auto;
      }

      .body {
        display: block;
      }

      .pane {
        max-height: none;
        border: 0;
        border-bottom: var(--lv-border-muted);
      }

      .fields,
      .properties {
        max-height: 20rem;
      }

      .canvas-pane {
        min-height: 38rem;
      }

      /* The authored grid remains absolute on desktop. On a narrow viewport,
       * switch the canvas to a single-column flow so chart hosts get a real
       * viewport-sized box instead of an off-screen slice of the desktop
       * canvas. The surface stays full-bleed; the canvas scroller remains the
       * only overflow container. */
      .canvas {
        display: grid;
        width: 100%;
        min-width: 0;
        min-height: 0;
        aspect-ratio: auto !important;
        grid-template-columns: minmax(0, 1fr) !important;
        grid-auto-rows: auto;
        gap: var(--base-size-12);
      }

      .canvas .visual {
        position: relative;
        top: auto !important;
        right: auto !important;
        bottom: auto !important;
        left: auto !important;
        width: 100% !important;
        height: 16rem !important;
        min-width: 0;
        min-height: 12rem;
        order: var(--mobile-order, 0);
      }

      .canvas .visual[data-visual-type='kpi'] {
        height: 8rem !important;
        min-height: 8rem;
      }

      .toolbar-actions {
        width: 100%;
        overflow-x: auto;
      }
    }
  `

  updated(): void {
    const builder = this.builder
    checkSignalContract('dashboard builder', builder, {
      projectId: 'required',
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
    this.selectPendingAddedPage(builder)
  }

  get builder(): DashboardBuilderSignal | null {
    return this.signal<DashboardBuilderSignal | null>('builder', null)
  }

  get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyStatus)
  }

  get builderVisuals(): Record<string, VisualizationEnvelope> {
    return this.visualizationDecoder.decodeAll(
      this.signal<Record<string, DashboardVisualizationSignal>>('builderVisuals', {}),
    )
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
      return html`<section class="state" aria-live="polite"><div><strong>Loading dashboard builder…</strong><span>Preparing the draft dashboard.</span></div></section>`
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
    const hasMoreActions = builder.capabilities.canShare || builder.capabilities.canExport
    return html`
      <header class="toolbar">
        ${this.backHref ? html`<a class="back" href=${this.backHref} aria-label="Back to dashboard">Back</a>` : html`<span class="back" aria-label="Back to dashboard">Back</span>`}
        <div class="title-wrap">
          <h1 class="title">${builder.title}</h1>
          <div class="meta" data-state=${builder.hasUnpublishedChanges || saveState === 'dirty' ? 'dirty' : saveState} aria-label="Dashboard draft status" aria-live="polite" title=${`${builder.origin.label} · Revision ${builder.revision.number} · ${builder.revision.id}`}>
            <span>${this.titleCase(builder.visibility)} ${this.titleCase(builder.lifecycle)} · Revision ${builder.revision.number} · ${this.saveLabel(builder)}</span>
          </div>
        </div>
        <div class="toolbar-actions" aria-label="Builder actions">
          ${(builder.preview.href || this.previewHref) && builder.capabilities.canPreview
            ? html`<a class="button" href=${builder.preview.href || this.previewHref}>Preview</a>`
            : builder.capabilities.canPreview ? html`<button disabled title="Preview is not available yet">Preview</button>` : nothing}
          ${hasMoreActions ? html`
            <details class="more-actions">
              <summary aria-label="More dashboard actions">More</summary>
              <div class="more-menu" aria-label="More dashboard actions">
                ${builder.capabilities.canShare ? html`<button @click=${this.toggleVisibility} aria-label="Toggle dashboard visibility">${builder.visibility === 'shared' ? 'Make private' : 'Share'}</button>` : nothing}
                ${builder.capabilities.canExport
                  ? this.exportYAMLHref ? html`<a class="button" href=${this.exportYAMLHref} download>Export YAML</a>` : html`<button disabled title="YAML export is not available yet">Export YAML</button>`
                  : nothing}
              </div>
            </details>` : nothing}
          ${builder.capabilities.canPublish ? html`<button class="primary" @click=${this.publish}>Publish</button>` : nothing}
        </div>
      </header>
    `
  }

  private renderFieldPane(builder: DashboardBuilderSignal) {
    const datasets = this.filteredDatasets(builder.semanticModel.datasets ?? [])
    return html`
      <aside class="pane fields" aria-label="Semantic model fields">
        <div class="pane-header">
          <h2 class="pane-title">${builder.semanticModel.title}</h2>
          <p class="pane-hint">Select a visual, then choose or drag a field.</p>
          <label>
            <span class="sr-only">Search fields</span>
            <input class="search" type="search" placeholder="Search fields" .value=${this.fieldQuery} @input=${this.onFieldQuery} />
          </label>
        </div>
        ${datasets.length === 0
          ? html`<p class="pane-hint" style="padding: 0.85rem">No fields match this search.</p>`
          : datasets.map((dataset) => this.renderDataset(dataset))}
      </aside>
    `
  }

  private renderDataset(dataset: DashboardBuilderDatasetSignal) {
    return html`
      <details class="table" open>
        <summary>${dataset.title}<span class="field-type">${dataset.fields.length}</span></summary>
        <div class="field-list">
          ${dataset.fields.map((field) => html`
            <button class="field" draggable=${this.builder?.capabilities.canEdit ? 'true' : 'false'} ?disabled=${!this.builder?.capabilities.canEdit} title=${this.builder?.capabilities.canEdit ? `Add ${field.label} to the selected visual` : 'Editing is not permitted'} aria-label="Add ${field.label}" @click=${() => this.addField(field)} @dragstart=${(event: DragEvent) => this.dragField(event, field)}>
              <span class="field-kind" aria-hidden="true">${field.kind === 'metric' ? '∑' : '◇'}</span>
              <span class="field-label">${field.label}</span>
              ${field.dataType.toLowerCase() === 'unknown' ? nothing : html`<span class="field-type">${field.dataType}</span>`}
            </button>
          `)}
        </div>
      </details>
    `
  }

  private renderCanvas(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    if (!page) {
      return html`<main class="canvas-pane" aria-label="Dashboard canvas"><div class="state"><div><strong>No pages yet</strong><span>Create a page to start designing this dashboard.</span>${builder.capabilities.canAddPage ? html`<div><button @click=${this.addPage} aria-label="Add page">Add page</button></div>` : nothing}</div></div></main>`
    }
    const width = Math.max(12, page.grid.columns || 12)
    return html`
      <main class="canvas-pane" aria-label="Dashboard canvas">
        <nav class="page-tabs" aria-label="Dashboard pages" role="tablist">
          ${builder.pages.map((item) => this.pageBaseHref
            ? html`<a class="page-tab" role="tab" aria-selected=${item.id === page.id} href=${this.pageHref(item.id)}>${item.title}</a>`
            : html`<button class="page-tab" role="tab" aria-selected=${item.id === page.id} @click=${() => this.selectPage(item.id)}>${item.title}</button>`)}
          ${builder.capabilities.canAddPage ? html`<button class="page-tab" @click=${this.addPage} aria-label="Add page">Add page</button>` : nothing}
        </nav>
        <div class="canvas-scroll">
          ${builder.capabilities.canAddVisual ? this.renderAddVisualControl() : nothing}
          ${builder.preview.error ? html`<p class="preview-error" role="alert">${builder.preview.error}</p>` : nothing}
          <div class="canvas" style=${`aspect-ratio: ${page.canvas.width || 16} / ${page.canvas.height || 9}; grid-template-columns: repeat(${width}, 1fr);`} @dragover=${(event: DragEvent) => event.preventDefault()} @drop=${this.dropField}>
            ${page.visuals.length === 0
              ? html`<div class="visual-empty"><div><strong>This page is empty</strong><span>Drag a field here or add a visual to begin.</span></div></div>`
              : page.visuals.map((visual) => this.renderVisual(visual, page))}
          </div>
        </div>
      </main>
    `
  }

  private renderVisual(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal) {
    const selected = visual.id === this.effectiveVisualID(this.builder, page)
    const preview = this.builderVisuals[this.visualSignalID(visual)]
    const mobileOrder = this.mobileVisualOrder(visual, page)
    const left = `${Math.max(0, visual.placement.col - 1) * (100 / Math.max(1, page.grid.columns))}%`
    const top = `${Math.max(0, visual.placement.row - 1) * (page.grid.rowHeight || 40)}px`
    const width = `${Math.max(1, visual.placement.colSpan) * (100 / Math.max(1, page.grid.columns))}%`
    const height = `${Math.max(1, visual.placement.rowSpan) * (page.grid.rowHeight || 40)}px`
    return html`
      <div class="visual ${preview ? 'has-preview' : ''}" data-visual-type=${visual.type.toLowerCase()} role="button" tabindex="0" aria-pressed=${selected} aria-label="Select ${visual.title}" style=${`left:${left};top:${top};width:${width};height:${height};--mobile-order:${mobileOrder}`} @click=${() => this.selectVisual(visual.id)} @keydown=${(event: KeyboardEvent) => this.selectVisualOnKey(event, visual.id)}>
        ${preview
          ? html`<span class="visual-preview" aria-hidden="true" inert><lv-visualization-host .envelope=${preview}></lv-visualization-host></span>`
          : html`<span class="visual-title">${visual.title}</span><span class="visual-preview-empty">${this.builder?.preview.error ? 'Preview unavailable' : 'Add fields to preview'}</span><span class="visual-type">${visual.type} · ${visual.slots.length} field slots</span>`}
      </div>
    `
  }

  private renderProperties(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    return html`
      <aside class="pane properties" aria-label="Properties">
        <div class="pane-header"><h2 class="pane-title">${visual ? 'Visual properties' : 'Page properties'}</h2><p class="pane-hint">Configure fields and review validation.</p></div>
        <div class="properties-body">
          ${visual ? this.renderVisualProperties(visual) : this.renderPageProperties(page)}
          <details class="secondary-details">
            <summary>Diagnostics &amp; source evidence</summary>
            <div class="secondary-details-content">
              ${this.renderDiagnostics(builder.diagnostics)}
              ${this.renderEvidence(builder)}
            </div>
          </details>
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
    if (evidence.kind === 'project' && evidence.projectId && evidence.dashboardId && evidence.generationId) {
      return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>project · ${evidence.projectId}/${evidence.dashboardId} · ${evidence.generationId}${evidence.path ? ` · ${evidence.path}` : ''}</span></div></section>`
    }
    return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>Unavailable</span></div></section>`
  }

  private filteredDatasets(datasets: DashboardBuilderDatasetSignal[]): DashboardBuilderDatasetSignal[] {
    const query = this.fieldQuery.trim().toLowerCase()
    if (!query) return datasets
    return datasets.map((dataset) => ({ ...dataset, fields: dataset.fields.filter((field) => `${field.label} ${field.id} ${field.dataType}`.toLowerCase().includes(query)) })).filter((dataset) => dataset.title.toLowerCase().includes(query) || dataset.fields.length > 0)
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

  private toggleVisibility = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canShare) return
    this.emitCommand('set_visibility', { visibility: builder.visibility === 'shared' ? 'private' : 'shared' })
  }
  private publish = (): void => this.emitCommand('publish')

  private addPage = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canAddPage) return
    this.pendingAddPage = {
      revision: this.revisionKey(builder),
      pageIDs: new Set(builder.pages.map((page) => page.id)),
    }
    this.emitCommand('add_page', { pageId: '', title: '' })
  }
  private addVisual = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canAddVisual) return
    this.emitCommand('add_visual', { pageId: this.selectedPage(builder)?.id ?? '', visualId: '', componentId: '', type: this.visualType, title: '' })
  }

  private addField(field: DashboardBuilderFieldSignal): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page && builder ? this.selectedVisual(page, builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !visual) return
    this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: field.id, role: field.kind })
  }

  private dropField = (event: DragEvent): void => {
    event.preventDefault()
    const fieldID = event.dataTransfer?.getData('text/leapview-field') || event.dataTransfer?.getData('text/plain')
    if (!fieldID || !this.builder?.capabilities.canEdit) return
    const builder = this.builder
    const page = this.selectedPage(builder)
    if (!page) return
    const visual = this.selectedVisual(page, builder)
    const field = (builder.semanticModel.datasets ?? []).flatMap((dataset) => dataset.fields).find((item) => item.id === fieldID)
    if (!field || !visual) return
    this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: field.id, role: field.kind })
  }

  private dragField(event: DragEvent, field: DashboardBuilderFieldSignal): void {
    if (!this.builder?.capabilities.canEdit) return
    event.dataTransfer?.setData('text/leapview-field', field.id)
    event.dataTransfer?.setData('text/plain', field.id)
  }

  private renderAddVisualControl() {
    return html`<div class="add-visual" aria-label="Add visual">
      <label>Visual type
        <select .value=${this.visualType} @change=${(event: Event) => { this.visualType = (event.currentTarget as HTMLSelectElement).value }}>
          ${builderVisualTypes.map((type) => html`<option value=${type}>${type}</option>`)}
        </select>
      </label>
      <button @click=${this.addVisual}>Add visual</button>
    </div>`
  }

  private selectPage(pageID: string): void {
    this.localPageID = pageID
    this.localVisualID = ''
    this.emit('lv-builder-page-select', { ...this.commandDetail(), pageId: pageID })
  }

  private pageHref(pageID: string): string {
    const separator = this.pageBaseHref.includes('?') ? '&' : '?'
    return `${this.pageBaseHref}${separator}page=${encodeURIComponent(pageID)}`
  }

  private selectVisual(visualID: string): void {
    this.localVisualID = visualID
    this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: visualID })
  }

  private selectVisualOnKey(event: KeyboardEvent, visualID: string): void {
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.selectVisual(visualID)
  }

  private visualSignalID(visual: DashboardBuilderVisualSignal): string {
    return (visual as DashboardBuilderVisualWithPreview).visualId || visual.id
  }

  private mobileVisualOrder(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal): number {
    return [...page.visuals]
      .sort((left, right) => left.placement.row - right.placement.row || left.placement.col - right.placement.col || left.id.localeCompare(right.id))
      .findIndex((item) => item.id === visual.id)
  }

  private commandDetail(): Record<string, string> {
    const builder = this.builder
    return {
      dashboardId: builder?.dashboardId ?? '',
      semanticModelId: builder?.semanticModel?.id ?? '',
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

  private selectPendingAddedPage(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddPage
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddPage = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const addedPage = builder.pages.find((page) => !pending.pageIDs.has(page.id))
    this.pendingAddPage = null
    if (!addedPage) return
    this.localPageID = addedPage.id
    this.localVisualID = ''
  }

  private revisionKey(builder: DashboardBuilderSignal): string {
    return `${builder.revision.id}:${builder.revision.number}:${builder.revision.contentHash}`
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

  private titleCase(value: string): string {
    return value.length === 0 ? value : `${value[0].toUpperCase()}${value.slice(1)}`
  }
}

if (!customElements.get('lv-dashboard-builder')) customElements.define('lv-dashboard-builder', LeapViewDashboardBuilder)

declare global {
  interface HTMLElementTagNameMap {
    'lv-dashboard-builder': LeapViewDashboardBuilder
  }
}
