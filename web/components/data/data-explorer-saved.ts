import { css, html, nothing, type TemplateResult } from 'lit'
import { ChevronDown, Download, Ellipsis, Link, Share2 } from 'lucide'
import type { DataExploreFieldSignal, DataExplorerCommand, SavedExplorationCommandSignal, SavedExplorationStateSignal } from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import { lucideIcon } from '../shared/lucide-icons'
import { dataExplorerExportURL, dataExplorerURL, savedExplorationShareURL, updateDataExplorerURL, type DataExplorerHistoryMode } from './data-explorer-url'

export const emptySavedExplorations: SavedExplorationStateSignal = {
  enabled: false,
  list: { items: [], includeArchived: false },
  command: { action: 'create' },
  save: { state: 'saved' },
}

export const savedExplorationStyles = css`
  .saved-explorations {
    position: relative;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-8) var(--base-size-16);
  }

  .saved-explorations-header,
  .saved-exploration-actions {
    display: flex;
    align-items: center;
    gap: var(--base-size-8);
  }

  .saved-exploration-actions {
    display: grid;
    position: absolute;
    z-index: var(--zIndex-overlay);
    top: 100%;
    right: var(--base-size-12);
    width: min(20rem, calc(100% - 24px));
    max-height: min(24rem, 60svh);
    overflow: auto;
    box-sizing: border-box;
    padding: var(--base-size-12);
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-overlay);
    box-shadow: var(--lv-shadow-floating-sm);
  }

  .saved-exploration-actions[hidden] { display: none; }
  .saved-exploration-actions label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
  .saved-exploration-actions .archive-action { border-top: var(--lv-border-muted); margin-top: var(--base-size-4); }
  .saved-exploration-toolbar { display: flex; gap: var(--base-size-4); margin-left: auto; flex: none; }
  .saved-actions-toggle { display: inline-flex; align-items: center; gap: var(--base-size-4); }

  .saved-exploration-actions input,
  .saved-exploration-actions select {
    min-height: 2rem;
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    padding: 0 var(--base-size-8);
    background: var(--lv-bg-control);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .saved-exploration-actions input {
    box-sizing: border-box;
    width: 100%;
    min-width: 0;
  }

  .saved-exploration-actions .text-button {
    white-space: nowrap;
    justify-content: flex-start;
    text-align: left;
  }

  .saved-explorations-header {
    flex: 1;
    flex-wrap: wrap;
    min-width: 0;
  }

  .saved-explorations-title {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  .saved-exploration-picker {
    position: relative;
    min-width: 0;
  }

  .saved-exploration-picker summary {
    display: inline-flex;
    min-height: var(--control-small-size);
    align-items: center;
    gap: var(--base-size-4);
    color: var(--lv-fg-muted);
    cursor: pointer;
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
    list-style: none;
    max-width: 100%;
    text-transform: none;
  }
  .saved-exploration-picker summary span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .saved-exploration-picker summary svg { flex: none; }

  .saved-exploration-picker summary::-webkit-details-marker {
    display: none;
  }

  .saved-exploration-picker summary:hover,
  .saved-exploration-picker summary:focus-visible {
    color: var(--lv-fg-default);
  }

  .saved-exploration-picker summary:focus-visible {
    outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
    outline-offset: var(--base-size-2);
  }

  .saved-exploration-list {
    position: absolute;
    top: calc(100% + var(--base-size-4));
    left: 0;
    z-index: var(--zIndex-overlay);
    display: grid;
    width: min(18rem, calc(100vw - 2rem));
    max-height: min(20rem, calc(100vh - 6rem));
    gap: var(--base-size-2);
    overflow: auto;
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-overlay);
    box-shadow: var(--lv-shadow-floating-sm);
    padding: var(--base-size-4);
  }

  .saved-exploration-item {
    overflow: hidden;
    min-height: var(--control-small-size);
    border-radius: var(--lv-radius-default);
    padding: var(--base-size-6) var(--base-size-8);
    color: var(--lv-fg-default);
    text-decoration: none;
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
  }

  .saved-exploration-item:hover,
  .saved-exploration-item:focus-visible {
    background: var(--lv-bg-control-hover);
    outline: 0;
  }

  .saved-exploration-status {
    min-width: 0;
    overflow-wrap: anywhere;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .header { position: relative; }
  .saved-exploration-sharing {
    position: static;
    min-width: 0;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .saved-exploration-sharing > summary {
    display: inline-flex;
    min-height: var(--control-medium-size);
    align-items: center;
    gap: var(--base-size-6);
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-control);
    color: var(--lv-fg-default);
    cursor: pointer;
    list-style: none;
    padding: 0 var(--base-size-12);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    text-transform: none;
    white-space: nowrap;
  }

  .saved-exploration-sharing summary::-webkit-details-marker {
    display: none;
  }

  .saved-exploration-sharing > summary:hover,
  .saved-exploration-sharing[open] > summary {
    background: var(--lv-bg-control-hover);
  }

  .saved-exploration-sharing summary:focus-visible {
    outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
    outline-offset: var(--base-size-2);
  }

  .saved-exploration-sharing-actions {
    box-sizing: border-box;
    position: absolute;
    top: calc(100% + var(--base-size-4));
    right: var(--base-size-12);
    z-index: var(--zIndex-overlay);
    display: grid;
    width: min(20rem, calc(100% - 24px));
    max-height: min(32rem, 65svh);
    gap: var(--base-size-4);
    overflow: auto;
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-overlay);
    box-shadow: var(--lv-shadow-floating-sm);
    padding: var(--base-size-8);
  }

  .saved-exploration-sharing-label {
    padding: var(--base-size-4) var(--base-size-8);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
    text-transform: uppercase;
  }

  .saved-exploration-sharing-label.export-label {
    border-top: var(--lv-border-muted);
    margin-top: var(--base-size-4);
    padding-top: var(--base-size-12);
  }

  .saved-exploration-sharing-actions .text-button,
  .saved-exploration-download,
  .saved-exploration-share-fallback {
    display: flex;
    box-sizing: border-box;
    width: 100%;
    align-items: center;
    gap: var(--base-size-8);
    min-height: var(--control-medium-size);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    padding: var(--base-size-6) var(--base-size-8);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    text-decoration: none;
    text-align: left;
    cursor: pointer;
  }

  .saved-exploration-sharing-actions svg { flex: none; color: var(--lv-fg-muted); }
  .saved-exploration-sharing-actions .dashboard-append-picker > summary { font: var(--lv-type-body); text-transform: none; }
  .saved-exploration-sharing-actions .dashboard-append-chevron { margin-left: auto; }
  .saved-exploration-sharing-actions .dashboard-append-picker[open] .dashboard-append-chevron { transform: rotate(180deg); }
  .saved-exploration-sharing-actions [role='status'] { padding: var(--base-size-4) var(--base-size-8); }

  .saved-exploration-sharing-actions .text-button:hover,
  .saved-exploration-sharing-actions .text-button:focus-visible,
  .saved-exploration-download:hover,
  .saved-exploration-download:focus-visible,
  .saved-exploration-share-fallback:hover,
  .saved-exploration-share-fallback:focus-visible {
    background: var(--lv-bg-control-hover);
  }

  .saved-exploration-sharing-hint {
    border-top: var(--lv-border-muted);
    margin: var(--base-size-4) 0 0;
    padding: var(--base-size-8) var(--base-size-8) var(--base-size-4);
    line-height: 1.5;
  }

  .saved-exploration-export-unavailable {
    padding: var(--base-size-6) var(--base-size-8);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

`

export type SavedExplorationVisibility = 'private' | 'organization'
export type SavedExplorationCurrent = NonNullable<SavedExplorationStateSignal['current']>

export type SavedExplorationTrackerCallbacks = {
  onBaselineChanged(current?: SavedExplorationCurrent): void
  onDirty(): void
}

export class SavedExplorationTracker {
  private dirtyFingerprint = ''
  private dirtyBaseline = ''

  constructor(private readonly callbacks: SavedExplorationTrackerCallbacks) {}

  observe(state: SavedExplorationStateSignal, currentSpec?: ExplorationSpec): void {
    const current = state.current
    const currentBaseline = current
      ? `${current.id}\u0000${current.revision.revisionId}\u0000${current.visibility}\u0000${JSON.stringify(current.spec ?? null)}`
      : ''
    if (currentBaseline !== this.dirtyBaseline) {
      this.dirtyBaseline = currentBaseline
      this.dirtyFingerprint = ''
      this.callbacks.onBaselineChanged(current)
    }
    if (!current?.detached || current.status !== 'active' || !current.spec || !currentSpec) return
    const fingerprint = JSON.stringify(currentSpec)
    if (fingerprint !== JSON.stringify(current.spec) && fingerprint !== this.dirtyFingerprint) {
      this.dirtyFingerprint = fingerprint
      this.callbacks.onDirty()
    }
  }
}

export type SavedExplorationViewOptions = {
  actionPanel(): 'save' | 'more' | undefined
  onToggleActions(panel?: 'save' | 'more'): void
  savedTitle(): string
  suggestedTitle?(): string
  savedDuplicateTitle(): string
  savedVisibility(): SavedExplorationVisibility
  currentSavedVisibility(current: SavedExplorationCurrent): SavedExplorationVisibility
  canSaveCurrent(): boolean
  activeSpec(): ExplorationSpec
  onSavedTitleInput(value: string): void
  onDuplicateTitleInput(value: string): void
  onSavedVisibilityInput(value: SavedExplorationVisibility): void
  onCurrentSavedVisibilityInput(value: SavedExplorationVisibility): void
  onCommand(command: SavedExplorationCommandSignal): void
  onReopen(current: SavedExplorationCurrent): void
  shareStatus(): string
  shareFallbackURL(): string
  onShareStatus(message: string, fallbackURL: string): void
  dashboardAppend?(): TemplateResult | typeof nothing
}

export class SavedExplorationViewController {
  savedActionPanel: 'save' | 'more' | undefined
  savedTitle = ''
  savedDuplicateTitle = ''
  savedVisibility: SavedExplorationVisibility = 'private'
  currentSavedVisibility: SavedExplorationVisibility = 'private'
  savedShareStatus = ''
  savedShareFallbackURL = ''

  constructor(private readonly host: EventTarget, private readonly refresh: () => void) {}

  readonly handleOutsidePointer = (event: PointerEvent): void => {
    const root = (this.host as HTMLElement).shadowRoot
    root?.querySelectorAll<HTMLDetailsElement>('.saved-exploration-sharing, .saved-exploration-picker').forEach((menu) => {
      if (menu.open && !event.composedPath().includes(menu)) menu.open = false
    })
    const saved = root?.querySelector('.saved-explorations')
    if (this.savedActionPanel && saved && !event.composedPath().includes(saved)) {
      this.savedActionPanel = undefined
      this.refresh()
    }
  }

  baselineChanged(current: SavedExplorationCurrent | null | undefined): void {
    this.savedActionPanel = undefined
    this.savedDuplicateTitle = ''
    this.currentSavedVisibility = current?.visibility ?? 'private'
    this.refresh()
  }

  options(
    canSaveCurrent: boolean,
    activeSpec: ExplorationSpec,
    dashboardAppend?: () => TemplateResult | typeof nothing,
    fields: DataExploreFieldSignal[] = [],
  ): SavedExplorationViewOptions {
    const update = <T>(setter: (value: T) => void) => (value: T) => { setter(value); this.refresh() }
    return {
      actionPanel: () => this.savedActionPanel,
      onToggleActions: (panel) => {
        const picker = (this.host as HTMLElement).shadowRoot?.querySelector<HTMLDetailsElement>('.saved-exploration-picker')
        if (panel && picker) picker.open = false
        this.savedActionPanel = this.savedActionPanel === panel ? undefined : panel
        this.refresh()
      },
      savedTitle: () => this.savedTitle,
      suggestedTitle: () => suggestedExplorationTitle(activeSpec, fields),
      savedDuplicateTitle: () => this.savedDuplicateTitle,
      savedVisibility: () => this.savedVisibility,
      currentSavedVisibility: (current) => this.currentSavedVisibility || current.visibility,
      canSaveCurrent: () => canSaveCurrent,
      activeSpec: () => activeSpec,
      onSavedTitleInput: update((value: string) => { this.savedTitle = value }),
      onDuplicateTitleInput: update((value: string) => { this.savedDuplicateTitle = value }),
      onSavedVisibilityInput: update((value: SavedExplorationVisibility) => { this.savedVisibility = value }),
      onCurrentSavedVisibilityInput: update((value: SavedExplorationVisibility) => { this.currentSavedVisibility = value }),
      onCommand: (command) => this.host.dispatchEvent(new CustomEvent('lv-saved-exploration-command', { bubbles: true, composed: true, detail: command })),
      onReopen: (current) => this.host.dispatchEvent(new CustomEvent('lv-saved-exploration-reopen', {
        bubbles: true, composed: true, detail: { explorationId: current.id, includeArchived: current.status === 'archived' },
      })),
      shareStatus: () => this.savedShareStatus,
      shareFallbackURL: () => this.savedShareFallbackURL,
      onShareStatus: (message, fallbackURL) => update((value: [string, string]) => {
        this.savedShareStatus = value[0]
        this.savedShareFallbackURL = value[1]
      })([message, fallbackURL]),
      dashboardAppend,
    }
  }
}

export function synchronizeSavedExplorationURL(
  command: DataExplorerCommand,
  state: SavedExplorationStateSignal,
  embedded: boolean,
  signalAvailable: boolean,
): void {
  if (!embedded && signalAvailable) updateSavedExplorationURL(command, 'replace', state)
}

export function updateSavedExplorationURL(command: DataExplorerCommand, mode: DataExplorerHistoryMode, state: SavedExplorationStateSignal): void {
  updateDataExplorerURL(command, mode, state.list?.selectedId, savedExplorationSelectionIncludesArchived(state))
}

// Field labels carry authored meaning; the field contract does not expose aggregation.
export function suggestedExplorationTitle(spec: ExplorationSpec, fields: DataExploreFieldSignal[] = []): string {
  const fieldTitle = (id: string) => fields.find((field) => field.id === id)?.label?.trim()
    || id.split('.').at(-1)?.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) || 'Field'
  const summarize = (names: string[]) => {
    const compact = names.slice(0, 2).map((name) => name.length > 32 ? `${name.slice(0, 31)}…` : name).join(', ')
    return names.length > 2 ? `${compact} +${names.length - 2}` : compact
  }
  const metrics = summarize(spec.metrics.map((field) => fieldTitle(field.field)))
  const dimensions = spec.dimensions.map((field) => fieldTitle(field.field))
  if (spec.time && !spec.dimensions.some((field) => field.field === spec.time?.field)) dimensions.push(fieldTitle(spec.time.field))
  const grouping = summarize(dimensions)
  return metrics ? grouping ? `${metrics} by ${grouping}` : metrics : grouping ? `Explore ${grouping}` : 'Untitled exploration'
}

export function explorationDisplayTitle(
  current: SavedExplorationCurrent | null | undefined,
  spec: ExplorationSpec,
  fields: DataExploreFieldSignal[] = [],
): string {
  if (current && current.title !== `Exploration · ${current.spec?.modelId || 'untitled'}`) return current.title
  return suggestedExplorationTitle(spec, fields)
}

export function renderSavedExplorations(state: SavedExplorationStateSignal, options: SavedExplorationViewOptions) {
  const current = state.current
  const items = state.list?.items ?? []
  const legacyItems = state.list?.legacyItems ?? []
  const savedCount = items.length + legacyItems.length
  const unavailable = state.save?.state === 'error'
  if (!state.enabled) return nothing
  const hasCanonicalState = options.canSaveCurrent() && Boolean(options.activeSpec().modelId?.trim())
  const saving = state.save?.state === 'saving'
  const unsaved = state.save?.state === 'dirty' || (current && options.currentSavedVisibility(current) !== current.visibility)
    || (current?.spec && hasCanonicalState && JSON.stringify(current.spec) !== JSON.stringify(options.activeSpec()))
  const saveStatus = unavailable ? state.save.message || 'Could not save'
    : saving ? 'Saving…'
    : !current ? 'Not saved'
    : unsaved ? 'Unsaved changes'
    : current.status === 'archived' ? 'Archived · read only' : 'Saved'
  return html`
    <section class="saved-explorations" aria-label="Saved explorations" @keydown=${(event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      const section = event.currentTarget as HTMLElement
      const trigger = section.querySelector<HTMLButtonElement>('.saved-actions-toggle[aria-expanded="true"]')
      if (trigger) { options.onToggleActions(); trigger.focus() }
      const picker = section.querySelector<HTMLDetailsElement>('.saved-exploration-picker[open]')
      if (picker) { picker.open = false; picker.querySelector('summary')?.focus() }
      event.stopPropagation()
    }}>
      <div class="saved-explorations-header">
        <span class="saved-exploration-status" role=${unavailable ? 'alert' : 'status'}>${saveStatus}</span>
        ${savedCount ? html`
          <details class="saved-exploration-picker">
            <summary aria-label="Open saved explorations" title="Open saved explorations" @click=${() => options.onToggleActions()}><span>Saved explorations (${savedCount})</span>${lucideIcon(ChevronDown, { size: 13 })}</summary>
            <div class="saved-exploration-list">
              ${items.map((item) => html`<a class="saved-exploration-item" href=${savedExplorationShareURL(item.id, item.status === 'archived')} aria-current=${item.id === current?.id ? 'page' : nothing}>${item.title}</a>`)}
              ${legacyItems.map((item) => html`<a class="saved-exploration-item" href=${item.openHref}>${item.name}</a>`)}
            </div>
          </details>
        ` : nothing}
      </div>
      ${html`
        <div class="saved-exploration-toolbar">
          ${current?.detached && current.status === 'active' ? html`<button type="button" class="text-button" ?disabled=${saving} @click=${() => saveSavedExploration(current, options)}>Save</button>` : nothing}
          ${hasCanonicalState ? html`<button type="button" class="text-button saved-actions-toggle" aria-expanded=${String(options.actionPanel() === 'save')} aria-controls="saved-query-form" @click=${() => options.onToggleActions('save')}>${current ? 'Save as…' : 'Save'}</button>` : nothing}
          ${current ? html`<button type="button" class="text-button saved-actions-toggle" aria-label="More saved exploration actions" title="More saved exploration actions" aria-expanded=${String(options.actionPanel() === 'more')} aria-controls="saved-version-actions" @click=${() => options.onToggleActions('more')}>${lucideIcon(Ellipsis, { size: 18 })}</button>` : nothing}
        </div>
        ${hasCanonicalState ? html`<div id="saved-query-form" class="saved-exploration-actions" role="group" aria-label=${current ? 'Save current query as a copy' : 'Save exploration'} ?hidden=${options.actionPanel() !== 'save'}>
          <label>Name<input type="text" aria-label=${current ? 'Current query name' : 'Saved exploration name'} placeholder=${options.suggestedTitle?.() || suggestedExplorationTitle(options.activeSpec())} .value=${options.savedTitle()} @input=${(event: Event) => options.onSavedTitleInput((event.target as HTMLInputElement).value)} /></label>
          ${current ? nothing : savedVisibilitySelect(options.savedVisibility(), options.onSavedVisibilityInput, saving)}
          <button type="button" class="text-button" ?disabled=${saving} aria-label=${current ? 'Save current query as a copy' : 'Save current exploration'} @click=${() => current ? saveCurrentQuery(options) : createSavedExploration(options)}>${current ? 'Save copy' : 'Save'}</button>
        </div>` : nothing}
        ${current ? html`<div id="saved-version-actions" class="saved-exploration-actions" role="group" aria-label=${`Saved exploration actions for ${current.title}`} ?hidden=${options.actionPanel() !== 'more'}>
          ${current.status === 'archived' ? html`<span class="saved-exploration-status">Archived · read only</span>` : nothing}
          <button type="button" class="text-button" @click=${() => options.onReopen(current)}>Reopen saved version</button>
          ${current.status === 'active' && current.detached ? html`${savedVisibilitySelect(options.currentSavedVisibility(current), options.onCurrentSavedVisibilityInput, saving)}<button type="button" class="text-button" ?disabled=${saving} @click=${() => saveSavedExploration(current, options)}>Save changes</button>` : nothing}
          <label>Copy name<input type="text" aria-label="Duplicate saved exploration name" placeholder=${`Copy of ${current.title}`} .value=${options.savedDuplicateTitle()} @input=${(event: Event) => options.onDuplicateTitleInput((event.target as HTMLInputElement).value)} /></label>
          <button type="button" class="text-button" ?disabled=${saving} @click=${() => duplicateSavedExploration(current, options)}>Duplicate saved version</button>
          ${current.status === 'active' ? html`<button type="button" class="text-button archive-action" ?disabled=${unsaved || saving} title=${unsaved ? 'Save your changes before archiving' : 'Archive saved exploration'} @click=${() => archiveSavedExploration(current, options)}>Archive</button>` : nothing}
        </div>` : nothing}
      `}
    </section>
  `
}

export function renderExplorationShareMenu(state: SavedExplorationStateSignal, options: SavedExplorationViewOptions) {
  if (!state.enabled && !options.dashboardAppend) return nothing
  const currentQueryURL = options.canSaveCurrent() && options.activeSpec().modelId?.trim()
    ? dataExplorerURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand)
    : ''
  const savedURL = state.current ? savedExplorationShareURL(state.current.id, state.current.status === 'archived') : ''
  if (!currentQueryURL && !savedURL && !options.dashboardAppend) return nothing
  return html`
    <details class="saved-exploration-sharing" @keydown=${(event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      const menu = event.currentTarget as HTMLDetailsElement
      menu.open = false
      menu.querySelector<HTMLElement>(':scope > summary')?.focus()
      event.stopPropagation()
    }}>
      <summary aria-label="Share or export exploration" @click=${(event: Event) => {
        if (!(event.currentTarget as HTMLElement).closest('details')?.open) options.onShareStatus('', '')
      }}>${lucideIcon(Share2, { size: 15 })}<span>Share</span></summary>
      <div class="saved-exploration-sharing-actions">
        <span class="saved-exploration-sharing-label">Share</span>
        ${currentQueryURL ? html`<button type="button" class="text-button" @click=${() => void copyExplorationLink(currentQueryURL, options)}>${lucideIcon(Link, { size: 16 })}Copy current query link</button>` : nothing}
        ${savedURL ? html`<button type="button" class="text-button" @click=${() => void copyExplorationLink(savedURL, options)}>${lucideIcon(Link, { size: 16 })}Copy saved version link</button>` : nothing}
        ${options.dashboardAppend?.() ?? nothing}
        ${currentQueryURL ? html`
          <span class="saved-exploration-sharing-label export-label">Export</span>
          <a class="saved-exploration-download" href=${dataExplorerExportURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand, 'csv')}>${lucideIcon(Download, { size: 16 })}Download CSV</a>
          <a class="saved-exploration-download" href=${dataExplorerExportURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand, 'parquet')}>${lucideIcon(Download, { size: 16 })}Download Parquet</a>
          <span class="saved-exploration-export-unavailable">Export complete results up to 10,000 rows and 32 MiB. Add filters for larger results.</span>
        ` : nothing}
        <span class="saved-exploration-sharing-hint">Links run live data with the viewer’s access.</span>
        ${options.shareStatus() ? html`<span role="status" aria-live="polite">${options.shareStatus()}</span>` : nothing}
        ${options.shareFallbackURL() ? html`<a class="saved-exploration-share-fallback" href=${options.shareFallbackURL()}>Open link</a>` : nothing}
      </div>
    </details>
  `
}

export async function copyExplorationLink(path: string, options: Pick<SavedExplorationViewOptions, 'onShareStatus'>): Promise<void> {
  const url = new URL(path, window.location.origin).href
  try {
    await navigator.clipboard.writeText(url)
    options.onShareStatus('Link copied.', '')
  } catch {
    options.onShareStatus('Clipboard unavailable. Open the link directly.', url)
  }
}

function savedVisibilitySelect(
  value: SavedExplorationVisibility,
  onChange: (value: SavedExplorationVisibility) => void,
  disabled = false,
) {
  return html`<label>Visibility <select aria-label="Saved exploration visibility" .value=${value} ?disabled=${disabled} @change=${(event: Event) => onChange((event.target as HTMLSelectElement).value as SavedExplorationVisibility)}><option value="private">Personal</option><option value="organization">Organization</option></select></label>`
}

function saveSavedExploration(current: SavedExplorationCurrent, options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const spec = activeSpec.modelId?.trim() ? activeSpec : (current.spec ?? activeSpec)
  options.onCommand({ action: 'update', explorationId: current.id, title: current.title, slug: current.slug, visibility: options.currentSavedVisibility(current), spec, expectedRevision: current.revision })
}

function createSavedExploration(options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const title = options.savedTitle().trim() || options.suggestedTitle?.() || suggestedExplorationTitle(activeSpec)
  options.onCommand({ action: 'create', title, visibility: options.savedVisibility(), spec: activeSpec })
}

function saveCurrentQuery(options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const title = options.savedTitle().trim() || options.suggestedTitle?.() || suggestedExplorationTitle(activeSpec)
  options.onCommand({ action: 'create', title, visibility: 'private', spec: activeSpec })
}

function duplicateSavedExploration(current: SavedExplorationCurrent, options: SavedExplorationViewOptions): void {
  const title = options.savedDuplicateTitle().trim() || `Copy of ${current.title}`
  options.onCommand({ action: 'duplicate', sourceExplorationId: current.id, title, visibility: current.visibility, expectedSourceRevision: current.revision })
}

function archiveSavedExploration(current: SavedExplorationCurrent, options: SavedExplorationViewOptions): void {
  options.onCommand({ action: 'archive', explorationId: current.id, expectedRevision: current.revision })
}

export function savedExplorationSelectionIncludesArchived(state: SavedExplorationStateSignal): boolean {
  const selected = state.list?.selectedId?.trim() ?? ''
  if (state.current?.status === 'archived' && (!selected || state.current.id === selected)) return true
  return (state.list?.items ?? []).some((item) => item.id === selected && item.status === 'archived')
}
