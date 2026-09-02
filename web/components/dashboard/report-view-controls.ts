import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { ChevronDown, Maximize2, Minus, Monitor, MonitorSmartphone, MoveHorizontal, Plus, Smartphone, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import {
  clampScale,
  resolvedLayoutMode,
  storedCustomScale,
  storedLayoutMode,
  storedZoomMode,
  type LayoutMode,
  type PresentationMode,
  type ZoomCommand,
  type ZoomState,
} from './report-view-state'

class ReportZoom extends LitElement {
  @state() private layoutMode: LayoutMode = storedLayoutMode()
  @state() private layout = resolvedLayoutMode(this.layoutMode)
  @state() private mode: PresentationMode = storedZoomMode()
  @state() private scale = storedCustomScale()

  static styles = css`
    :host {
      position: relative;
      display: inline-block;
      max-width: 100%;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .zoom {
      display: inline-flex;
      max-width: 100%;
      align-items: center;
      min-height: 32px;
      gap: var(--base-size-2);
      white-space: nowrap;
    }

    details {
      position: relative;
      flex: 0 0 auto;
    }

    summary {
      display: inline-flex;
      width: 32px;
      min-width: 32px;
      height: 32px;
      box-sizing: border-box;
      align-items: center;
      justify-content: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      list-style: none;
      white-space: nowrap;
    }

    summary::-webkit-details-marker {
      display: none;
    }

    summary:hover,
    summary:focus-visible,
    details[open] summary {
      background: color-mix(in srgb, var(--lv-bg-panel) 80%, var(--lv-fg-muted));
      color: var(--lv-fg-default);
    }

    summary:focus-visible {
      outline: 0;
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: var(--base-size-2);
    }

    .layout-trigger {
      width: 38px;
      gap: var(--base-size-2);
    }

    .layout-trigger > svg {
      width: 17px;
      height: 17px;
    }

    .layout-trigger .chevron {
      width: 10px;
      height: 10px;
      color: var(--lv-fg-muted);
    }

    .menu {
      position: absolute;
      z-index: var(--zIndex-popover, 300);
      bottom: calc(100% + var(--base-size-6));
      display: grid;
      box-sizing: border-box;
      gap: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-8);
      box-shadow: var(--shadow-floating-small);
    }

    .layout-menu {
      left: 0;
      width: 208px;
    }

    .zoom-menu {
      right: 0;
      width: 148px;
    }

    .group-label {
      color: var(--lv-fg-muted);
      padding: var(--base-size-4) var(--base-size-8) 0;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold, 600);
      text-transform: uppercase;
    }

    .divider {
      height: 1px;
      margin: var(--base-size-4) 0;
      background: var(--lv-line-muted);
    }

    .menu-button {
      display: flex;
      width: 100%;
      min-height: 32px;
      box-sizing: border-box;
      align-items: center;
      justify-content: space-between;
      padding: var(--base-size-6) var(--base-size-8);
      text-align: left;
      font: var(--lv-type-body-compact);
    }

    .option-label {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-8);
    }

    .option-label svg {
      width: 16px;
      height: 16px;
    }

    .menu-button[aria-pressed='true'] {
      background: color-mix(in srgb, var(--lv-bg-panel) 88%, var(--lv-fg-link));
      color: var(--lv-fg-link);
      font-weight: var(--base-text-weight-semibold, 600);
    }

    .current {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    button {
      display: grid;
      width: 32px;
      min-width: 32px;
      height: 32px;
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      font: inherit;
    }

    button:hover,
    button:focus-visible {
      background: color-mix(in srgb, var(--lv-bg-panel) 80%, var(--lv-fg-muted));
      color: var(--lv-fg-default);
      outline: 0;
    }

    button[aria-pressed='true'] {
      background: color-mix(in srgb, var(--lv-bg-panel) 88%, var(--lv-fg-link));
      color: var(--lv-fg-link);
    }

    svg {
      width: 16px;
      height: 16px;
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    input {
      appearance: none;
      width: 100%;
      min-width: 0;
      height: 16px;
      background: transparent;
      cursor: pointer;
    }

    input::-webkit-slider-runnable-track {
      height: 4px;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-muted);
    }

    input::-webkit-slider-thumb {
      appearance: none;
      width: 12px;
      height: 12px;
      margin-top: -4px;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
    }

    input::-moz-range-track {
      height: 4px;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-muted);
    }

    input::-moz-range-thumb {
      width: 12px;
      height: 12px;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
    }

    input:focus-visible {
      outline: 0;
    }

    input:focus-visible::-webkit-slider-thumb {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: 2px;
    }

    input:focus-visible::-moz-range-thumb {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: 2px;
    }

    .slider {
      display: grid;
      width: clamp(86px, 15vw, 176px);
      min-width: 0;
      margin-inline: var(--base-size-4);
      padding-inline: var(--base-size-8);
    }

    .group-separator {
      width: 1px;
      height: 20px;
      flex: 0 0 auto;
      margin-inline: var(--base-size-4);
      background: var(--lv-line-muted);
    }

    .mode-label {
      width: auto;
      min-width: 38px;
      padding-inline: var(--base-size-6);
      font: var(--lv-type-caption);
    }

    .percent-trigger {
      width: auto;
      min-width: 46px;
      padding-inline: var(--base-size-6);
    }

    @media (max-width: 520px) {
      .slider {
        display: none;
      }

      .zoom-out {
        order: 1;
      }

      .percent-menu {
        order: 2;
      }

      .zoom-in {
        order: 3;
      }

      .group-separator {
        margin-inline: var(--base-size-2);
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('lv-report-zoom-state', this.onZoomState as EventListener)
  }

  disconnectedCallback(): void {
    document.removeEventListener('lv-report-zoom-state', this.onZoomState as EventListener)
    super.disconnectedCallback()
  }

  private onZoomState = (event: CustomEvent<ZoomState>): void => {
    this.layoutMode = event.detail.layoutMode
    this.layout = event.detail.layout
    this.mode = event.detail.mode
    this.scale = event.detail.scale
  }

  private command(detail: ZoomCommand): void {
    const command = this.layout === 'mobile' && detail.layout === undefined
      ? { ...detail, layout: 'desktop' as const }
      : detail
    this.dispatchEvent(new CustomEvent('lv-report-zoom-command', {
      detail: command,
      bubbles: true,
      composed: true,
    }))
    this.shadowRoot?.querySelectorAll('details').forEach(details => details.removeAttribute('open'))
  }

  private nudge(delta: number): void {
    this.command({ mode: 'custom', scale: clampScale(this.scale + delta) })
  }

  private slide(event: Event): void {
    const input = event.currentTarget as HTMLInputElement
    this.command({ mode: 'custom', scale: clampScale(Number(input.value) / 100) })
  }

  private layoutLabel(): string {
    return `Layout, ${capitalize(this.layoutMode)}, currently ${capitalize(this.layout)}`
  }

  render() {
    const percent = Math.round(this.scale * 100)
    return html`
      <div class="zoom" role="group" aria-label="Report view controls">
        <details data-control="layout">
          <summary class="layout-trigger" title="Layout" aria-label=${this.layoutLabel()}>
            ${lucideIcon(layoutIcon(this.layoutMode))}
            <span class="chevron" aria-hidden="true">${lucideIcon(ChevronDown)}</span>
          </summary>
          <div class="menu layout-menu" role="group" aria-label="Report layout">
            <span class="group-label">Layout</span>
            ${(['auto', 'desktop', 'mobile'] as const).map(layout => html`
              <button
                class="menu-button"
                type="button"
                data-layout=${layout}
                aria-pressed=${String(this.layoutMode === layout)}
                @click=${() => this.command({ layout })}
              >
                <span class="option-label">${lucideIcon(layoutIcon(layout))}<span>${capitalize(layout)}</span></span>
                ${layout === 'auto' ? html`<span class="current">${capitalize(this.layout)}</span>` : nothing}
              </button>
            `)}
          </div>
        </details>
        <span class="group-separator" aria-hidden="true"></span>
        <button class="fit-action" data-mode="fit-width" type="button" title="Fit width" aria-label="Fit width" aria-pressed=${String(this.layout === 'desktop' && this.mode === 'fit-width')} @click=${() => this.command({ mode: 'fit-width' })}>
          ${zoomIcon('fit-width')}
        </button>
        <button class="fit-action" data-mode="fit-page" type="button" title="Fit page" aria-label="Fit page" aria-pressed=${String(this.layout === 'desktop' && this.mode === 'fit-page')} @click=${() => this.command({ mode: 'fit-page' })}>
          ${zoomIcon('fit-page')}
        </button>
        <button class="fit-action mode-label" data-mode="actual-size" type="button" title="Actual size" aria-label="Actual size" aria-pressed=${String(this.layout === 'desktop' && this.mode === 'actual-size')} @click=${() => this.command({ mode: 'actual-size' })}>
          1:1
        </button>
        <span class="group-separator" aria-hidden="true"></span>
        <button class="zoom-out" type="button" title="Zoom out" aria-label="Zoom out" @click=${() => this.nudge(-0.1)}>
          ${zoomIcon('minus')}
        </button>
        <div class="slider">
          <input type="range" min="10" max="200" .value=${String(percent)} aria-label="Zoom percent" aria-valuetext=${`${percent}%`} @input=${this.slide} />
        </div>
        <button class="zoom-in" type="button" title="Zoom in" aria-label="Zoom in" @click=${() => this.nudge(0.1)}>
          ${zoomIcon('plus')}
        </button>
        <details class="percent-menu" data-control="zoom-presets">
          <summary class="percent-trigger" title="Zoom presets" aria-label=${`Zoom presets, ${percent}%`}>${percent}%</summary>
          <div class="menu zoom-menu" role="group" aria-label="Zoom presets">
            <span class="group-label">Zoom</span>
            ${zoomPresets.map(scale => html`
              <button
                class="menu-button"
                type="button"
                data-scale=${String(scale)}
                aria-pressed=${String(this.mode === 'custom' && Math.abs(this.scale - scale) < 0.001)}
                @click=${() => this.command({ mode: 'custom', scale })}
              >${Math.round(scale * 100)}%</button>
            `)}
          </div>
        </details>
      </div>
    `
  }
}

function capitalize(value: string): string {
  return `${value.slice(0, 1).toUpperCase()}${value.slice(1)}`
}

const zoomPresets = [0.5, 0.75, 1, 1.25, 1.5, 2] as const

function layoutIcon(layout: LayoutMode): IconNode {
  if (layout === 'desktop') return Monitor
  if (layout === 'mobile') return Smartphone
  return MonitorSmartphone
}

function zoomIcon(name: 'fit-width' | 'fit-page' | 'minus' | 'plus') {
  const icons: Record<'fit-width' | 'fit-page' | 'minus' | 'plus', IconNode> = {
    'fit-width': MoveHorizontal,
    'fit-page': Maximize2,
    minus: Minus,
    plus: Plus,
  }

  return lucideIcon(icons[name])
}

customElements.define('lv-report-zoom', ReportZoom)
