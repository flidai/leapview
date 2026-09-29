import { css, html, nothing } from 'lit'
import { ChevronDown, Share2 } from 'lucide'
import type { DataExplorerCommand, SavedExplorationCommandSignal, SavedExplorationStateSignal } from '../../generated/signals'
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
    flex-wrap: wrap;
    justify-content: flex-start;
  }

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
    min-width: 10rem;
  }

  .saved-exploration-actions .text-button {
    white-space: nowrap;
  }

  .saved-explorations-header {
    flex: none;
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
  }

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
    margin-left: auto;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .saved-exploration-sharing {
    position: relative;
    min-width: 0;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .saved-exploration-sharing summary {
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

  .saved-exploration-sharing summary:hover,
  .saved-exploration-sharing[open] summary {
    background: var(--lv-bg-control-hover);
  }

  .saved-exploration-sharing summary:focus-visible {
    outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
    outline-offset: var(--base-size-2);
  }

  .saved-exploration-sharing-actions {
    position: absolute;
    top: calc(100% + var(--base-size-4));
    right: 0;
    z-index: var(--zIndex-overlay);
    display: grid;
    width: min(20rem, calc(100vw - 2rem));
    max-height: min(26rem, calc(100vh - 6rem));
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

  .saved-exploration-sharing-actions .text-button,
  .saved-exploration-download,
  .saved-exploration-share-fallback {
    display: flex;
    width: 100%;
    align-items: center;
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
  savedTitle(): string
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

export function renderSavedExplorations(state: SavedExplorationStateSignal, options: SavedExplorationViewOptions) {
  const current = state.current
  const items = state.list?.items ?? []
  const unavailable = state.save?.state === 'error'
  if (!state.enabled) return nothing
  const hasCanonicalState = options.canSaveCurrent() && Boolean(options.activeSpec().modelId?.trim())
  return html`
    <section class="saved-explorations" aria-label="Saved explorations">
      <div class="saved-explorations-header">
        ${items.length ? html`
          <details class="saved-exploration-picker">
            <summary aria-label="Open saved explorations">Saved explorations (${items.length})${lucideIcon(ChevronDown, { size: 13 })}</summary>
            <div class="saved-exploration-list">
              ${items.map((item) => html`<a class="saved-exploration-item" href=${savedExplorationShareURL(item.id, item.status === 'archived')} aria-current=${item.id === current?.id ? 'page' : nothing}>${item.title}</a>`)}
            </div>
          </details>
        ` : html`<span class="saved-explorations-title">Saved explorations</span>`}
      </div>
      ${unavailable ? nothing : current ? html`
        <div class="saved-exploration-actions" role="group" aria-label=${`Saved exploration actions for ${current.title}`}>
            <input type="text" aria-label="Duplicate saved exploration name" placeholder="Copy name (optional)" .value=${options.savedDuplicateTitle()} @input=${(event: Event) => options.onDuplicateTitleInput((event.target as HTMLInputElement).value)} />
            ${current.status === 'active' && current.detached ? savedVisibilitySelect(options.currentSavedVisibility(current), options.onCurrentSavedVisibilityInput) : nothing}
            ${current.detached
              ? current.status === 'active'
                ? html`<button type="button" class="text-button" @click=${() => saveSavedExploration(current, options)}>Save</button>`
                : html`<span class="saved-exploration-status">Read-only archived copy</span>`
              : html`<button type="button" class="text-button" @click=${() => options.onReopen(current)}>Reopen</button>`}
            ${hasCanonicalState ? html`
              <input type="text" aria-label="Current query name" placeholder="Name current query (optional)" .value=${options.savedTitle()} @input=${(event: Event) => options.onSavedTitleInput((event.target as HTMLInputElement).value)} />
              <button type="button" class="text-button" @click=${() => saveCurrentQuery(options)}>Save as current query</button>
            ` : nothing}
            <button type="button" class="text-button" @click=${() => duplicateSavedExploration(current, options)}>Duplicate saved version</button>
            ${current.status === 'active' ? html`<button type="button" class="text-button" @click=${() => archiveSavedExploration(current, options)}>Archive</button>` : nothing}
        </div>
      ` : hasCanonicalState ? html`<div class="saved-exploration-actions"><input type="text" aria-label="Saved exploration name" placeholder="Name this exploration" .value=${options.savedTitle()} @input=${(event: Event) => options.onSavedTitleInput((event.target as HTMLInputElement).value)} />${savedVisibilitySelect(options.savedVisibility(), options.onSavedVisibilityInput)}<button type="button" class="text-button" @click=${() => createSavedExploration(options)}>Save current</button></div>` : nothing}
      ${unavailable || state.save?.message || (state.save?.state && state.save.state !== 'saved') ? html`<span class="saved-exploration-status" role=${unavailable ? 'alert' : nothing}>${state.save?.message ?? state.save?.state}</span>` : nothing}
    </section>
  `
}

export function renderExplorationShareMenu(state: SavedExplorationStateSignal, options: SavedExplorationViewOptions) {
  if (!state.enabled) return nothing
  const currentQueryURL = options.canSaveCurrent() && options.activeSpec().modelId?.trim()
    ? dataExplorerURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand)
    : ''
  const savedURL = state.current ? savedExplorationShareURL(state.current.id, state.current.status === 'archived') : ''
  if (!currentQueryURL && !savedURL) return nothing
  return html`
    <details class="saved-exploration-sharing">
      <summary aria-label="Share or export exploration">${lucideIcon(Share2, { size: 15 })}<span>Share</span></summary>
      <div class="saved-exploration-sharing-actions">
        <span class="saved-exploration-sharing-label">Share</span>
        ${currentQueryURL ? html`<button type="button" class="text-button" @click=${() => void copyExplorationLink(currentQueryURL, options)}>Copy current query link</button>` : nothing}
        ${savedURL ? html`<button type="button" class="text-button" @click=${() => void copyExplorationLink(savedURL, options)}>Copy saved version link</button>` : nothing}
        ${currentQueryURL ? html`
          <span class="saved-exploration-sharing-label">Export</span>
          <a class="saved-exploration-download" href=${dataExplorerExportURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand, 'csv')}>Download CSV</a>
          <a class="saved-exploration-download" href=${dataExplorerExportURL({ mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand, 'parquet')}>Download Parquet</a>
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
) {
  return html`<label>Visibility <select aria-label="Saved exploration visibility" .value=${value} @change=${(event: Event) => onChange((event.target as HTMLSelectElement).value as SavedExplorationVisibility)}><option value="private">Personal</option><option value="organization">Organization</option></select></label>`
}

function saveSavedExploration(current: SavedExplorationCurrent, options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const spec = activeSpec.modelId?.trim() ? activeSpec : (current.spec ?? activeSpec)
  options.onCommand({ action: 'update', explorationId: current.id, title: current.title, slug: current.slug, visibility: options.currentSavedVisibility(current), spec, expectedRevision: current.revision })
}

function createSavedExploration(options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const title = options.savedTitle().trim() || `Exploration · ${activeSpec.modelId || 'untitled'}`
  options.onCommand({ action: 'create', title, visibility: options.savedVisibility(), spec: activeSpec })
}

function saveCurrentQuery(options: SavedExplorationViewOptions): void {
  const activeSpec = options.activeSpec()
  const title = options.savedTitle().trim() || `Exploration · ${activeSpec.modelId || 'untitled'}`
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
