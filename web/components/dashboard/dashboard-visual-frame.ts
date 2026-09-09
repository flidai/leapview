import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'

/** Provides the bounded frame shared by dashboard visual cards. */
class DashboardVisualFrame extends LitElement {
  @property({ type: Boolean, reflect: true }) transparent = false

  static styles = css`
    :host {
      display: block;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      box-sizing: border-box;
    }

    .frame {
      position: relative;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-sizing: border-box;
    }

    :host([data-agent-referenced]) .frame {
      box-shadow: inset 0 0 0 2px var(--lv-line-accent);
    }

    :host([transparent]) .frame {
      border-color: transparent;
      background: transparent;
    }

    :host([data-canvas-filter-visual]) {
      overflow: visible;
      z-index: 5;
    }

    :host([data-canvas-filter-visual]) .frame {
      overflow: visible;
    }

    ::slotted(*) {
      display: block;
      width: 100%;
      height: 100%;
    }
  `

  render() {
    return html`
      <article class="frame">
        <slot></slot>
      </article>
    `
  }
}

if (!customElements.get('lv-dashboard-visual-frame')) customElements.define('lv-dashboard-visual-frame', DashboardVisualFrame)
