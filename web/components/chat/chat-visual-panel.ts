import { LitElement, css, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import { ArrowUpRight, X } from 'lucide'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/visual-artifact'

export class ChatVisualPanel extends LitElement {
  @property() title = ''
  @property({ attribute: 'artifact-id' }) artifactId = ''
  @property({ attribute: false }) payload?: VisualizationEnvelope
  @property({ attribute: false }) explorerHref = ''
  @property({ type: Boolean }) saving = false
  @property({ type: Boolean }) saved = false
  @property({ type: Boolean }) modal = false
  @property() saveError = ''
  private displaySource?: VisualizationEnvelope
  private displayVisual?: VisualizationEnvelope

  static styles = css`
    :host {
      display: block;
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      height: 100%;
      border-left: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    *, *::before, *::after { box-sizing: inherit; }

    aside {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .header {
      display: flex;
      min-height: var(--lv-control-large);
      align-items: center;
      gap: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-16);
    }

    h2 {
      min-width: 0;
      flex: 1;
      overflow: hidden;
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .actions { display: flex; align-items: center; gap: var(--base-size-8); }

    button, a {
      display: inline-flex;
      min-height: var(--lv-control-medium);
      align-items: center;
      justify-content: center;
      gap: var(--base-size-4);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: 0 var(--base-size-12);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-secondary);
      text-decoration: none;
    }

    button:hover:not(:disabled), a:hover { background: var(--lv-bg-control-hover); }
    button:focus-visible, a:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: var(--base-size-2); }
    button:disabled { cursor: default; opacity: 0.6; }

    .close { width: var(--lv-control-medium); padding: 0; }
    .close svg, a svg { width: var(--base-size-16); height: var(--base-size-16); }

    .content { min-height: 0; overflow: auto; padding: var(--base-size-16); }
    lv-visual-artifact { display: block; height: min(28rem, 60vh); min-height: 16rem; }
    .feedback { margin: var(--base-size-12) 0 0; font: var(--lv-type-secondary); }
    .feedback.error { color: var(--lv-fg-danger); }

    @media (max-width: 42rem) {
      .header { flex-wrap: wrap; }
      h2 { flex-basis: 100%; }
      .actions { width: 100%; justify-content: flex-end; }
    }
  `

  render() {
    return html`
      <aside role=${this.modal ? 'dialog' : 'region'} aria-modal=${this.modal ? 'true' : nothing} aria-label="Visual details" @keydown=${this.onKeyDown}>
        <div class="header">
          <h2>${this.title || 'Visual result'}</h2>
          <div class="actions">
            ${this.explorerHref ? html`
              <a href=${this.explorerHref} aria-label="Open visual in Data Explorer" title="Open in Data Explorer">${lucideIcon(ArrowUpRight, { size: 16 })}<span>Explore</span></a>
              <button type="button" ?disabled=${this.saving || this.saved} @click=${this.save} aria-label="Save visual to Data Explorer">${this.saved ? 'Saved' : this.saving ? 'Saving…' : 'Save'}</button>
            ` : nothing}
            <button class="close" type="button" aria-label="Close visual details" @click=${this.close}>${lucideIcon(X, { size: 16 })}</button>
          </div>
        </div>
        <div class="content">
          <lv-visual-artifact type=${this.payload?.spec.kind ?? ''} artifact-id=${this.artifactId} .payload=${this.visualDisplayPayload()}></lv-visual-artifact>
          ${this.saveError ? html`<p class="feedback error" role="alert">${this.saveError}</p>` : nothing}
          ${this.saved ? html`<p class="feedback" role="status">Saved to Data Explorer.</p>` : nothing}
        </div>
      </aside>
    `
  }

  private visualDisplayPayload(): VisualizationEnvelope | undefined {
    if (this.payload === this.displaySource) return this.displayVisual
    this.displaySource = this.payload
    this.displayVisual = this.payload?.spec.kind === 'proportional'
      ? { ...this.payload, spec: { ...this.payload.spec, presentation: { ...this.payload.spec.presentation, legend: 'hidden' } } }
      : this.payload
    return this.displayVisual
  }

  focusClose(): void {
    this.renderRoot.querySelector<HTMLButtonElement>('.close')?.focus()
  }

  private onKeyDown(event: KeyboardEvent): void {
    if (event.key !== 'Escape') return
    event.preventDefault()
    event.stopPropagation()
    this.close()
  }

  private close(): void {
    this.dispatchEvent(new CustomEvent('lv-chat-visual-close', { bubbles: true, composed: true }))
  }

  private save(): void {
    if (!this.explorerHref || this.saving || this.saved) return
    this.dispatchEvent(new CustomEvent('lv-chat-visual-save', {
      bubbles: true,
      composed: true,
      detail: { artifactId: this.artifactId, title: this.title, explorerHref: this.explorerHref },
    }))
  }
}

if (!customElements.get('lv-chat-visual-panel')) customElements.define('lv-chat-visual-panel', ChatVisualPanel)
