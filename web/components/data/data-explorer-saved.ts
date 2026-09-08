import { css, html, nothing } from 'lit'
import type { DataExplorerCommand, SavedExplorationCommandSignal, SavedExplorationStateSignal } from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import { absoluteDataExplorerURL, dataExplorerExportURL, dataExplorerURL, savedExplorationURL, updateDataExplorerURL, type DataExplorerHistoryMode } from './data-explorer-url'

export const emptySavedExplorations: SavedExplorationStateSignal = {
  enabled: false,
  list: { items: [], includeArchived: false },
  command: { action: 'create' },
  save: { state: 'saved' },
}

export const savedExplorationStyles = css`
  .saved-explorations {
    display: grid;
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

  .saved-explorations-header {
    justify-content: space-between;
  }

  .saved-explorations-title {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  .saved-exploration-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-6);
  }

  .saved-exploration-item {
    overflow: hidden;
    max-width: 14rem;
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    padding: var(--base-size-4) var(--base-size-8);
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

  .saved-exploration-current {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-8);
  }

  .saved-exploration-current-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  .saved-exploration-status {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .saved-exploration-sharing {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-8);
    font: var(--lv-type-caption);
  }

  .saved-exploration-sharing-copy {
    color: var(--lv-fg-muted);
  }

  .saved-exploration-sharing-group {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-6);
  }

  .saved-exploration-sharing a {
    color: var(--lv-fg-default);
  }

  .saved-exploration-share-status {
    color: var(--lv-fg-muted);
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
  activeSpec(): ExplorationSpec
  onSavedTitleInput(value: string): void
  onDuplicateTitleInput(value: string): void
  onSavedVisibilityInput(value: SavedExplorationVisibility): void
  onCurrentSavedVisibilityInput(value: SavedExplorationVisibility): void
  onCommand(command: SavedExplorationCommandSignal): void
  onReopen(current: SavedExplorationCurrent): void
  onShare?(url: string): void | Promise<void>
  shareStatus?(): string
  shareFallbackURL?(): string
  onShareStatus?(message: string, fallbackURL?: string): void
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
  const activeCommand = { mode: 'explore', explore: { spec: options.activeSpec() } } as DataExplorerCommand
  const shareURL = dataExplorerURL(activeCommand)
  const latestSavedURL = current ? savedExplorationURL(current.id, current.status === 'archived') : ''
  const hasCanonicalState = Boolean(options.activeSpec().modelId?.trim())
  const shareStatus = options.shareStatus?.() ?? ''
  const shareFallbackURL = options.shareFallbackURL?.() ?? ''
  return html`
    <section class="saved-explorations" aria-label="Saved explorations">
      <div class="saved-explorations-header">
        <span class="saved-explorations-title">Saved explorations</span>
        <span class="saved-exploration-status" role=${unavailable ? 'alert' : nothing}>${state.save?.message ?? state.save?.state ?? 'saved'}</span>
      </div>
      <div class="saved-exploration-sharing" aria-label="Exploration sharing and downloads">
        ${hasCanonicalState ? html`
          <span class="saved-exploration-sharing-group">
            <span class="saved-exploration-sharing-copy">Current query link · includes unpublished changes</span>
            <a href=${shareURL} aria-label="Open current exploration query link">Share current query</a>
            <button type="button" class="text-button" aria-label="Copy current exploration query link" @click=${() => void shareExplorationLink(shareURL, options)}>Copy link</button>
          </span>
        ` : html`<span class="saved-exploration-sharing-copy">Select a semantic model to enable current-query sharing and downloads.</span>`}
        ${current ? html`
          <span class="saved-exploration-sharing-group">
            <span class="saved-exploration-sharing-copy">Latest saved version · viewer access required</span>
            <a href=${latestSavedURL} aria-label="Open latest saved version">Latest saved version</a>
            <button type="button" class="text-button" aria-label="Copy latest saved version link" @click=${() => void shareExplorationLink(latestSavedURL, options)}>Copy link</button>
          </span>
        ` : nothing}
        <span class="saved-exploration-sharing-copy">Shared links rerun live data under recipient access and do not grant permission.</span>
        ${shareStatus ? html`
          <span class="saved-exploration-share-status" role="status" aria-live="polite">${shareStatus}</span>
          ${shareFallbackURL ? html`<a href=${shareFallbackURL} class="saved-exploration-share-fallback">Open link directly</a>` : nothing}
        ` : nothing}
        ${hasCanonicalState ? html`
          <a href=${dataExplorerExportURL(activeCommand, 'csv')}>Download CSV (current query)</a>
          <a href=${dataExplorerExportURL(activeCommand, 'parquet')}>Download Parquet (current query)</a>
        ` : nothing}
      </div>
      <div class="saved-exploration-list">
        ${items.map((item) => html`<a class="saved-exploration-item" href=${savedExplorationURL(item.id, item.status === 'archived')}>${item.title}</a>`)}
      </div>
      ${unavailable ? nothing : current ? html`
        <div class="saved-exploration-current">
          <div>
            <div class="saved-exploration-current-name">${current.title}</div>
            <div class="saved-exploration-status">${current.status}${current.detached ? ' · reopened' : ''}</div>
          </div>
          <div class="saved-exploration-actions">
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
        </div>
      ` : html`<div class="saved-exploration-actions"><input type="text" aria-label="Saved exploration name" placeholder="Name this exploration" .value=${options.savedTitle()} @input=${(event: Event) => options.onSavedTitleInput((event.target as HTMLInputElement).value)} />${savedVisibilitySelect(options.savedVisibility(), options.onSavedVisibilityInput)}<button type="button" class="text-button" @click=${() => createSavedExploration(options)}>Save current</button></div>`}
    </section>
  `
}

export async function copyExplorationLink(url: string): Promise<void> {
  const absoluteURL = absoluteDataExplorerURL(url)
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(absoluteURL)
    return
  }
  if (typeof document === 'undefined') throw new Error('Clipboard is unavailable')
  const input = document.createElement('textarea')
  input.value = absoluteURL
  input.setAttribute('readonly', '')
  input.style.position = 'fixed'
  input.style.opacity = '0'
  document.body.appendChild(input)
  let copied = false
  try {
    input.select()
    copied = document.execCommand('copy')
  } finally {
    input.remove()
  }
  if (!copied) throw new Error('Clipboard copy was rejected')
}

async function shareExplorationLink(url: string, options: SavedExplorationViewOptions): Promise<void> {
  const absoluteURL = absoluteDataExplorerURL(url)
  try {
    if (options.onShare) await options.onShare(absoluteURL)
    else await copyExplorationLink(url)
    options.onShareStatus?.('Link copied.', '')
  } catch {
    options.onShareStatus?.('Copy failed. Open the link directly below.', absoluteURL)
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
