import { LitElement, css, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import type { ExplorationSpec } from '../../generated/exploration'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { visualArtifactExploreHref } from './visual-artifact-explore'
import '../dashboard/visualization/host'

class VisualArtifact extends LitElement {
  @property() type: string = ''
  @property({ attribute: 'artifact-id' }) artifactId = ''
  @property({ attribute: false }) payload?: VisualizationEnvelope
  @property({ attribute: false }) exploration?: ExplorationSpec
  @property({ attribute: 'conversation-id' }) conversationId = ''

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 0;
    }

    *,
    *::before,
    *::after {
      box-sizing: border-box;
    }

    .artifact {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      width: 100%;
      height: 100%;
      min-width: 0;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface, var(--lv-bg-panel));
      box-shadow: var(--shadow-resting-small);
    }

    /* Without an action row, the content is the first grid child. Keep it in
       the full-height track instead of leaving the second track empty. */
    .artifact:not(.has-actions) {
      grid-template-rows: minmax(0, 1fr);
    }

    lv-visualization-host {
      display: block;
      width: 100%;
      height: 100%;
    }

    .actions {
      display: flex;
      justify-content: flex-end;
      padding: var(--lv-space-sm);
      border-bottom: var(--lv-border-muted);
    }

    .explore-action {
      color: var(--lv-fg-accent);
      font: var(--lv-type-body);
    }

    .content {
      min-width: 0;
      min-height: 0;
    }

    .state {
      display: grid;
      height: 100%;
      min-height: 8rem;
      place-items: center;
      padding: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      text-align: center;
    }
  `

  render() {
    if (!this.type) {
      return this.renderState('Unsupported artifact: unknown')
    }
    if (!this.payload) {
      return this.renderState('Artifact data is unavailable.')
    }
    const exploreHref = visualArtifactExploreHref(this.exploration, this.conversationId)
    return html`
      <div class=${`artifact ${isTabularVisualType(this.payload.spec.kind) ? 'table' : 'chart'}${exploreHref ? ' has-actions' : ''}`}>
        ${exploreHref ? html`<div class="actions"><a class="explore-action" href=${exploreHref}>Explore visual</a></div>` : nothing}
        <div class="content"><lv-visualization-host .envelope=${this.payload}></lv-visualization-host></div>
      </div>
    `
  }

  private renderState(message: string) {
    return html`<div class="artifact"><div class="state">${message}</div></div>`
  }
}

function isTabularVisualType(type: string): boolean {
  return type === 'table' || type === 'matrix' || type === 'pivot'
}

if (!customElements.get('lv-visual-artifact')) customElements.define('lv-visual-artifact', VisualArtifact)
