import { LitElement, css, html, nothing } from 'lit'
import { property, query, state } from 'lit/decorators.js'
import { Check, ChevronDown } from 'lucide'
import { toggleAnchoredPopover } from './anchored-popover'
import { lucideIcon } from './lucide-icons'

export type SelectMenuOption = {
  value: string
  label: string
  disabled?: boolean
}

export class SelectMenu extends LitElement {
  @property({ attribute: false }) options: SelectMenuOption[] = []
  @property() value = ''
  @property() label = 'Select an option'
  @property({ type: Boolean }) disabled = false

  @state() private open = false
  @query('.trigger') private trigger?: HTMLButtonElement
  @query('.menu') private menu?: HTMLElement

  static styles = css`
    :host {
      display: inline-block;
      max-width: 100%;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .trigger {
      display: inline-flex;
      min-width: 0;
      max-width: 100%;
      min-height: var(--lv-control-medium, var(--control-medium-size, var(--base-size-32)));
      box-sizing: border-box;
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small);
      background: var(--lv-button-bg-rest, var(--lv-bg-control));
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      padding: 0 var(--lv-space-control, var(--base-size-10));
      text-align: left;
      white-space: nowrap;
    }

    .trigger:hover,
    .trigger:focus-visible,
    .trigger[aria-expanded='true'] {
      border-color: var(--lv-border-accent, var(--lv-line-accent));
      outline: 0;
    }

    .leading,
    .chevron,
    .check {
      display: inline-flex;
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      color: var(--lv-fg-muted);
    }

    .leading ::slotted(*) {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .value {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
    }

    .menu {
      position: fixed;
      z-index: var(--z-index-dropdown, 100);
      inset: auto;
      display: none;
      overflow: auto;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-overlay, var(--lv-bg-panel));
      box-shadow: var(--lv-shadow-floating-lg);
      margin: 0;
      padding: var(--base-size-4);
    }

    .menu:popover-open { display: grid; }

    .option {
      display: grid;
      width: 100%;
      min-height: var(--lv-control-medium, var(--control-medium-size, var(--base-size-32)));
      box-sizing: border-box;
      grid-template-columns: var(--base-size-16) minmax(0, 1fr);
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-transparent, 1px solid transparent);
      border-radius: var(--lv-radius-small);
      background: transparent;
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      padding: var(--base-size-4) var(--base-size-8);
      text-align: left;
    }

    .option:hover,
    .option:focus-visible,
    .option[aria-selected='true'] {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      outline: 0;
    }

    .option:disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled, .55);
    }

    .check {
      width: var(--base-size-16);
      height: var(--base-size-16);
      color: var(--lv-fg-accent);
    }
  `

  render() {
    const selected = this.options.find((option) => option.value === this.value)
    return html`
      <button
        class="trigger"
        type="button"
        aria-label=${this.label}
        aria-haspopup="listbox"
        aria-expanded=${String(this.open)}
        ?disabled=${this.disabled}
        @click=${this.handleTriggerClick}
        @keydown=${this.handleTriggerKeydown}
      >
        <span class="leading" aria-hidden="true"><slot name="leading"></slot></span>
        <span class="value">${selected?.label ?? this.label}</span>
        <span class="chevron" aria-hidden="true">${lucideIcon(ChevronDown, { size: 14, strokeWidth: 2 })}</span>
      </button>
      <div class="menu" popover="auto" role="listbox" aria-label=${this.label} @toggle=${this.handlePopoverToggle} @keydown=${this.handleOptionKeydown}>
        ${this.options.map((option) => html`
          <button
            class="option"
            type="button"
            role="option"
            aria-selected=${String(option.value === this.value)}
            data-value=${option.value}
            ?disabled=${option.disabled}
            @click=${() => this.select(option)}
          >
            <span class="check" aria-hidden="true">${option.value === this.value ? lucideIcon(Check, { size: 14, strokeWidth: 2 }) : nothing}</span>
            <span>${option.label}</span>
          </button>
        `)}
      </div>
    `
  }

  close(returnFocus = false): void {
    if (this.menu?.matches(':popover-open')) this.menu.hidePopover()
    this.open = false
    if (returnFocus) this.trigger?.focus()
  }

  private handleTriggerClick = (): void => {
    if (!this.trigger || !this.menu) return
    this.open = toggleAnchoredPopover(this.trigger, this.menu, {
      minWidth: this.trigger.getBoundingClientRect().width,
      maxWidth: 320,
      maxHeight: 320,
      gap: 4,
    })
    if (this.open) queueMicrotask(() => this.focusSelectedOption())
  }

  private handleTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    if (!this.open) this.handleTriggerClick()
    queueMicrotask(() => this.focusSelectedOption())
  }

  private handlePopoverToggle = (event: Event): void => {
    this.open = (event as Event & { newState?: string }).newState === 'open'
    this.dispatchEvent(new CustomEvent('lv-select-toggle', {
      bubbles: true,
      composed: true,
      detail: { open: this.open },
    }))
  }

  private select(option: SelectMenuOption): void {
    if (option.disabled) return
    this.value = option.value
    this.dispatchEvent(new CustomEvent('lv-select-change', {
      bubbles: true,
      composed: true,
      detail: { value: option.value },
    }))
    this.close(true)
  }

  private focusSelectedOption(): void {
    const options = this.optionElements()
    ;(options.find((option) => option.getAttribute('aria-selected') === 'true') ?? options[0])?.focus()
  }

  private handleOptionKeydown = (event: KeyboardEvent): void => {
    const options = this.optionElements()
    const index = options.indexOf(this.shadowRoot?.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const offset = event.key === 'ArrowDown' ? 1 : -1
      options[(index + offset + options.length) % options.length]?.focus()
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      options[event.key === 'Home' ? 0 : options.length - 1]?.focus()
    } else if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.close(true)
    } else if (event.key === 'Tab') {
      this.close()
    }
  }

  private optionElements(): HTMLButtonElement[] {
    return Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('.option:not(:disabled)'))
  }
}

if (!customElements.get('lv-select-menu')) customElements.define('lv-select-menu', SelectMenu)

declare global {
  interface HTMLElementTagNameMap {
    'lv-select-menu': SelectMenu
  }
}
