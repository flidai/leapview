import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'
import type { VisualizationEnvelope } from '../../generated/visualization'
import '../dashboard/visualization/host'

class VisualArtifact extends LitElement {
  @property() type: string = ''
  @property({ attribute: 'artifact-id' }) artifactId = ''
  @property({ attribute: false }) payload?: VisualizationEnvelope

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
      display: block;
      width: 100%;
      height: 100%;
      min-width: 0;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface, var(--lv-bg-panel));
      box-shadow: var(--shadow-resting-small);
    }

    lv-visualization-host {
      display: block;
      width: 100%;
      height: 100%;
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
    return html`
      <div class=${`artifact ${isTabularVisualType(this.payload.spec.kind) ? 'table' : 'chart'}`}>
        <lv-visualization-host .envelope=${this.payload}></lv-visualization-host>
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
