import { LitElement, css, html } from 'lit'
import type { SavedVisualLibrarySignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { ChartNoAxesColumn, GripVertical, Plus } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { savedVisualDragType, type SavedVisualLibraryMessage } from './visual-library-bridge'

class SavedVisualLibrary extends DatastarLit(LitElement) {
  private sent = ''
  private themeObserver?: MutationObserver

  connectedCallback(): void {
    super.connectedCallback()
    if (window.parent === window) return
    const parentRoot = window.parent.document.documentElement
    const attributes = ['data-color-mode', 'data-theme-preference', 'data-light-theme', 'data-dark-theme']
    const syncTheme = () => {
      for (const name of attributes) {
        const value = parentRoot.getAttribute(name)
        if (value === null) document.documentElement.removeAttribute(name)
        else document.documentElement.setAttribute(name, value)
      }
    }
    syncTheme()
    this.themeObserver = new MutationObserver(syncTheme)
    this.themeObserver.observe(parentRoot, { attributes: true, attributeFilter: attributes })
  }

  disconnectedCallback(): void {
    this.themeObserver?.disconnect()
    super.disconnectedCallback()
  }

  static styles = css`
    :host { display: block; color: var(--lv-fg-default); font-family: var(--fontStack-system, system-ui); font-size: 13px; }
    .intro, .empty { padding: 8px 10px; margin: 0; color: var(--lv-fg-muted); font-size:12px; line-height: 1.4; }
    .list { display: flex; flex-direction: column; gap: 6px; padding: 0 8px 10px; }
    .visual { display: flex; align-items: center; gap: 6px; min-height:48px; box-sizing:border-box; padding: 6px; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); cursor: grab; }
    .visual[aria-disabled='true'] { opacity: .5; cursor: default; }
    .copy { flex: 1; min-width: 0; }
    .title { display: -webkit-box; -webkit-line-clamp:2; -webkit-box-orient:vertical; overflow:hidden; overflow-wrap: anywhere; line-height: 1.35; }
    .model { display: block; color: var(--lv-fg-muted); font-size: 11px; margin-top: 4px; }
    svg { width: 16px; height: 16px; flex-shrink: 0; }
    .grip { color: var(--lv-fg-muted); }
    button { display: inline-flex; align-items:center; justify-content:center; min-width:28px; min-height:28px; padding: 6px; border: 0; border-radius: 4px; background: transparent; color: var(--lv-fg-default); cursor: pointer; }
    button:hover { background: var(--lv-bg-control-hover); }
    button:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset:2px; }
    .error { padding: 12px; color: var(--lv-fg-danger); }
    @media (max-width: 240px) {
      .visual { gap: 6px; }
      .grip, .visual > svg { display: none; }
    }
  `

  private get library(): SavedVisualLibrarySignal | null {
    return this.signal<SavedVisualLibrarySignal | null>('savedVisualLibrary', null)
  }

  updated(): void {
    const library = this.library
    if (!library) return
    const key = JSON.stringify(library)
    if (key === this.sent) return
    this.sent = key
    window.parent.postMessage({ type: 'lv-saved-visual-library', library } satisfies SavedVisualLibraryMessage, window.location.origin)
  }

  render() {
    const library = this.library
    if (!library) return html`<p class="intro">Loading saved visuals…</p>`
    return html`
      ${library.error ? html`<p class="error" role="alert">${library.error}</p>` : null}
      <p class="intro">Drag onto the canvas or select + to add.</p>
      ${!library.visuals.length ? html`<p class="empty">Save a visual in the agent to reuse it here.</p>` : null}
      <div class="list">${library.visuals.map(visual => {
        const compatible = !library.modelId || library.modelId === visual.semanticModelId
        return html`
          <div class="visual" data-saved-id=${visual.id} draggable=${compatible ? 'true' : 'false'} aria-disabled=${!compatible}
            @dragstart=${(event: DragEvent) => {
              if (!compatible || !event.dataTransfer) return
              event.dataTransfer.setData(savedVisualDragType, visual.id)
              event.dataTransfer.effectAllowed = 'copy'
            }}>
            <span class="grip">${lucideIcon(GripVertical)}</span>${lucideIcon(ChartNoAxesColumn)}
            <span class="copy"><span class="title" title=${visual.title}>${visual.title}</span>${compatible ? null : html`<span class="model">Different semantic model</span>`}</span>
            <button type="button" aria-label=${`Add ${visual.title} to dashboard`} title="Add to dashboard" ?disabled=${!compatible}
              @click=${() => window.parent.postMessage({ type: 'lv-add-saved-visual', id: visual.id } satisfies SavedVisualLibraryMessage, window.location.origin)}>${lucideIcon(Plus)}</button>
          </div>`
      })}</div>`
  }
}
customElements.define('lv-saved-visual-library', SavedVisualLibrary)
