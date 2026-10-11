import { LitElement, css, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import { X } from 'lucide'
import { lucideIcon } from './lucide-icons'
import { composedFocusableElements, wrapModalTab } from './modal-focus'

class LeapViewDrawer extends LitElement {
  @property({ type: Boolean, reflect: true }) open = false
  @property({ type: Boolean, reflect: true }) modal = true
  @property({ type: Boolean, attribute: 'close-on-outside' }) closeOnOutside = false
  @property() label = 'Drawer'
  @property({ reflect: true }) size: 'default' | 'wide' = 'default'

  static styles = css`
    :host {
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    button {
      font: inherit;
    }

    .overlay {
      position: fixed;
      inset: 0;
      z-index: calc(var(--z-index-inspector) - 1);
      display: flex;
      justify-content: flex-end;
      background: var(--lv-modal-backdrop);
    }

    .overlay-nonmodal {
      background: transparent;
      pointer-events: none;
    }

    .overlay-nonmodal .drawer {
      pointer-events: auto;
    }

    .drawer {
      display: grid;
      width: min(30rem, 100vw);
      max-width: 100vw;
      height: 100svh;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      border-left: var(--lv-border-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-lg);
      animation: drawer-slide-in var(--lv-transition-fast);
    }

    :host([size='wide']) .drawer {
      width: min(34rem, 100vw);
    }

    .header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: start;
      gap: var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-16) var(--base-size-20);
    }

    .title-block {
      min-width: 0;
    }

    .close {
      display: inline-flex;
      width: var(--lv-control-medium);
      height: var(--lv-control-medium);
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      transition:
        color var(--lv-transition-fast),
        background-color var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .close:hover,
    .close:focus-visible {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .body {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-20);
    }

    .icon {
      display: inline-flex;
      width: var(--lv-icon-sm);
      height: var(--lv-icon-sm);
      align-items: center;
      justify-content: center;
      color: currentColor;
    }

    @media (max-width: 44rem) {
      .drawer {
        width: 100vw;
        border-left: 0;
      }
    }

    @media (prefers-reduced-motion: reduce) {
      .drawer {
        animation-duration: 1ms;
      }
    }

    @keyframes drawer-slide-in {
      from {
        transform: translateX(var(--base-size-16));
        opacity: .96;
      }
      to {
        transform: translateX(0);
        opacity: 1;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('keydown', this.handleWindowKeyDown)
    window.addEventListener('pointerdown', this.handleWindowPointerDown)
  }

  disconnectedCallback(): void {
    window.removeEventListener('keydown', this.handleWindowKeyDown)
    window.removeEventListener('pointerdown', this.handleWindowPointerDown)
    super.disconnectedCallback()
  }

  render() {
    if (!this.open) return nothing
    return html`
      <div class=${this.modal ? 'overlay' : 'overlay overlay-nonmodal'} @click=${this.handleOverlayClick}>
        <aside
          class="drawer"
          role="dialog"
          aria-modal=${this.modal ? 'true' : nothing}
          aria-label=${this.label}
          @keydown=${this.handleKeyDown}
        >
          <header class="header">
            <div class="title-block">
              <slot name="title"></slot>
              <slot name="subtitle"></slot>
            </div>
            <button class="close" type="button" aria-label=${`Close ${this.label}`} @click=${this.close}>
              <span class="icon" aria-hidden="true">${lucideIcon(X, { size: 16 })}</span>
            </button>
          </header>
          <div class="body">
            <slot></slot>
          </div>
        </aside>
      </div>
    `
  }

  focusFirst(): void {
    window.setTimeout(() => {
      const close = this.renderRoot.querySelector<HTMLElement>('.close')
      const focusable = composedFocusableElements(this.renderRoot)
      // Keep initial focus on content, independently of the native Tab order.
      const initial = focusable.find(element => element !== close) ?? close
      initial?.focus()
    }, 0)
  }

  private readonly close = (): void => {
    this.dispatchEvent(new CustomEvent('lv-drawer-close', { bubbles: true, composed: true }))
  }

  private readonly handleOverlayClick = (event: Event): void => {
    if (this.modal && event.target === event.currentTarget) this.close()
  }

  private readonly handleWindowKeyDown = (event: KeyboardEvent): void => {
    if (!this.open || event.defaultPrevented || event.key !== 'Escape') return
    event.preventDefault()
    this.close()
  }

  private readonly handleWindowPointerDown = (event: PointerEvent): void => {
    if (!this.open || this.modal || !this.closeOnOutside || event.composedPath().includes(this)) return
    this.close()
  }

  private readonly handleKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') {
      event.preventDefault()
      this.close()
      return
    }
    if (event.key !== 'Tab' || !this.modal || event.defaultPrevented) return
    // Keep the existing composed-tree edge wrapping for nested controls.
    wrapModalTab(event, this.renderRoot)
  }
}

if (!customElements.get('lv-drawer')) customElements.define('lv-drawer', LeapViewDrawer)

declare global {
  interface HTMLElementTagNameMap {
    'lv-drawer': LeapViewDrawer
  }
}
