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
      display: flex;
      flex-direction: column;
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
      min-height: 0;
      flex: 1;
    }

    .limit-notice { margin: 0; padding: 6px 10px; color: var(--lv-fg-muted); font: var(--lv-type-caption); }

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
    const limitNotice = this.payload.diagnostics.find(item => item.code === 'agent_row_limit_reached')?.message
    const budget = this.payload.spec.dataBudget.maxRows
    const limited = this.payload.dataState.kind === 'inline' && budget > 0
      && this.payload.dataState.datasets.some(dataset => dataset.rows.length >= budget)
    return html`
      <div class=${`artifact ${isTabularVisualType(this.payload.spec.kind) ? 'table' : 'chart'}`}>
        <lv-visualization-host .envelope=${this.payload}></lv-visualization-host>
        ${limitNotice || limited ? html`<p class="limit-notice" role="note">${limitNotice || `Showing up to ${budget} rows. More data may exist.`}</p>` : null}
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
