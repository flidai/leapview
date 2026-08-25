import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronLeft, ChevronRight, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/loading-spinner'

type SubSidebarItem = {
  id: string
  title?: string
  meta?: string
  href?: string
  active?: boolean
  disabled?: boolean
  pending?: boolean
}

type SubSidebarConfig = {
  label?: string
  railLabel?: string
  ariaLabel?: string
  storageKey?: string
  activeId?: string
  emptyText?: string
  disabled?: boolean
  collapsible?: boolean
  numbered?: boolean
  items?: SubSidebarItem[]
}

type ResolvedConfig = Required<Pick<SubSidebarConfig, 'label' | 'railLabel' | 'ariaLabel' | 'storageKey' | 'emptyText'>> & {
  activeId: string
  disabled: boolean
  collapsible: boolean
  numbered: boolean
  items: SubSidebarItem[]
}

type HoverTitle = {
  index: string
  title: string
  top: number
  active: boolean
}

const defaultConfig: ResolvedConfig = {
  label: 'Items',
  railLabel: 'Items',
  ariaLabel: 'Sub navigation',
  storageKey: 'leapview-sub-sidebar-collapsed',
  activeId: '',
  emptyText: 'No items.',
  disabled: false,
  collapsible: true,
  numbered: true,
  items: [],
}

const configConverter = {
  fromAttribute(value: string | null): SubSidebarConfig {
    if (!value) return {}
    try {
      const parsed = JSON.parse(value)
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as SubSidebarConfig : {}
    } catch {
      return {}
    }
  },
  toAttribute(value: SubSidebarConfig): string {
    return JSON.stringify(value ?? {})
  },
}

class SubSidebar extends LitElement {
  @property({ attribute: 'config', converter: configConverter }) config: SubSidebarConfig = {}
  @state() private collapsed = storedCollapsed(defaultConfig.storageKey)
  @state() private hoverTitle?: HoverTitle
  private loadedStorageKey = defaultConfig.storageKey

  static styles = css`
    :host {
      --lv-sub-sidebar-width: var(--lv-sub-sidebar-width-expanded);
      display: block;
      width: var(--lv-sub-sidebar-width);
      height: 100%;
      min-height: 0;
      box-sizing: border-box;
      overflow: hidden;
      border-right: var(--lv-border-muted);
      background: var(--lv-sidebar-bg);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      transition: width var(--motion-transition-stateChange);
    }

    :host([data-collapsed]) {
      --lv-sub-sidebar-width: var(--lv-page-rail-width-collapsed);
      z-index: var(--zIndex-sticky);
      overflow: visible;
    }

    aside {
      position: sticky;
      top: 0;
      display: grid;
      width: 100%;
      height: 100%;
      min-height: 0;
      max-height: 100svh;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      background: var(--lv-sidebar-bg);
      transition: width var(--motion-transition-stateChange);
    }

    :host([data-collapsed]) aside {
      overflow: visible;
    }

    header {
      display: grid;
      min-width: 0;
      padding: var(--base-size-8);
    }

    .top-row {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      justify-content: space-between;
    }

    .section-title {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
      letter-spacing: 0;
      text-transform: uppercase;
    }

    .collapse {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      flex: 0 0 auto;
      place-items: center;
      margin-left: auto;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--fgColor-disabled);
      cursor: pointer;
      padding: 0;
    }

    .collapse:hover,
    .collapse:focus-visible {
      border-color: var(--lv-line-muted);
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .collapse svg {
      width: 14px;
      height: 14px;
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    .collapse[hidden] {
      display: none;
    }

    nav {
      display: grid;
      align-content: start;
      gap: var(--base-size-2);
      min-width: 0;
      min-height: 0;
      overflow-x: hidden;
      overflow-y: auto;
      padding: var(--base-size-8) var(--base-size-4);
      scrollbar-gutter: stable;
      scrollbar-color: var(--lv-scrollbar-thumb) transparent;
      scrollbar-width: thin;
    }

    nav::-webkit-scrollbar {
      width: var(--base-size-6);
    }

    nav::-webkit-scrollbar-track {
      background: transparent;
    }

    nav::-webkit-scrollbar-thumb {
      border-radius: var(--lv-radius-full);
      background: var(--lv-scrollbar-thumb);
    }

    nav::-webkit-scrollbar-thumb:hover {
      background: var(--lv-scrollbar-thumb-hover);
    }

    a {
      text-decoration: none;
    }

    .item-link {
      position: relative;
      display: grid;
      width: 100%;
      grid-template-columns: var(--control-xsmall-size) minmax(0, 1fr);
      min-height: calc(var(--control-small-size) + var(--base-size-2));
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--fgColor-disabled);
      cursor: pointer;
      padding: 0 var(--control-xsmall-paddingInline-normal);
      text-align: left;
      font: var(--lv-type-body);
    }

    .item-link.unnumbered {
      grid-template-columns: minmax(0, 1fr);
      padding-inline: 10px;
    }

    .item-link:hover,
    .item-link:focus-visible {
      background: var(--lv-bg-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .item-link[aria-current='page'] {
      border-color: transparent;
      background: var(--lv-bg-hover);
      color: var(--lv-fg-default);
    }

    .item-link[aria-current='page']::before {
      content: '';
      position: absolute;
      inset-block: var(--base-size-8);
      left: 0;
      width: var(--base-size-2);
      border-radius: var(--lv-radius-full);
      background: var(--lv-accent);
    }

    .item-link:disabled {
      cursor: default;
      opacity: 0.72;
    }

    .item-index {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-variant-numeric: tabular-nums;
      line-height: 1;
    }

    .item-link:hover .item-index,
    .item-link:focus-visible .item-index,
    .item-link[aria-current='page'] .item-index {
      color: var(--lv-fg-default);
    }

    .item-text {
      display: grid;
      min-width: 0;
      gap: 1px;
    }

    .item-title,
    .item-meta {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .item-title-row {
      display: inline-flex;
      min-width: 0;
      align-items: center;
      gap: 6px;
    }

    .item-title {
      font: var(--lv-type-body-compact);
    }

    .pending-spinner {
      flex: 0 0 auto;
    }

    .item-link:hover .item-title,
    .item-link:focus-visible .item-title,
    .item-link[aria-current='page'] .item-title {
      font-weight: var(--base-text-weight-medium);
    }

    .item-meta {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: 1;
    }

    .empty {
      padding: 8px 9px;
      color: var(--lv-fg-muted);
      font: var(--lv-type-secondary);
    }

    :host([data-collapsed]) header {
      padding: var(--base-size-8) var(--base-size-4) var(--base-size-6);
    }

    :host([data-collapsed]) .section-title,
    :host([data-collapsed]) .item-text,
    :host([data-collapsed]) .empty {
      display: none;
    }

    :host([data-collapsed]) .top-row {
      display: grid;
      justify-items: center;
    }

    :host([data-collapsed]) .collapse {
      margin-left: 0;
    }

    :host([data-collapsed]) nav {
      scrollbar-gutter: auto;
      scrollbar-width: none;
    }

    :host([data-collapsed]) nav::-webkit-scrollbar {
      display: none;
      width: 0;
    }

    :host([data-collapsed]) .item-link {
      grid-template-columns: var(--control-xsmall-size);
      justify-content: center;
      min-height: calc(var(--control-small-size) + var(--base-size-2));
      padding-inline: 0;
    }

    :host([data-collapsed]) .item-link:hover .item-index,
    :host([data-collapsed]) .item-link:focus-visible .item-index,
    :host([data-collapsed]) .item-link[aria-current='page'] .item-index {
      color: var(--lv-fg-default);
    }

    :host([data-collapsed]) .item-link[aria-current='page']::before {
      inset-block: var(--base-size-8);
      left: 0;
      width: var(--base-size-2);
    }

    .hover-title {
      display: none;
    }

    :host([data-collapsed]) .hover-title {
      position: absolute;
      z-index: var(--zIndex-popover);
      left: var(--base-size-8);
      min-height: calc(var(--control-small-size) + var(--base-size-2));
      max-width: 12rem;
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-6);
      padding: 0 var(--control-xsmall-paddingInline-normal) 0 0;
      background: var(--lv-sidebar-bg);
      color: var(--lv-fg-default);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      line-height: 1;
      pointer-events: none;
      transform: translateY(-50%);
      animation: rail-title-fade-in var(--motion-duration-micro) var(--motion-easing-enter);
      white-space: nowrap;
    }

    :host([data-collapsed]) .hover-title[data-active]::before {
      content: '';
      position: absolute;
      inset-block: var(--base-size-8);
      left: var(--base-size-negative-2);
      width: var(--base-size-2);
      border-radius: var(--lv-radius-full);
      background: var(--lv-accent);
    }

    .hover-title-index {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      color: var(--lv-fg-default);
      font-variant-numeric: tabular-nums;
      font-weight: var(--base-text-weight-medium);
    }

    .hover-title-name {
      overflow: hidden;
      text-overflow: ellipsis;
      animation: rail-title-name-fold-out var(--motion-duration-short) var(--motion-easing-enter);
      transform-origin: left center;
    }

    .rail-label {
      display: none;
    }

    :host([data-collapsed]) .rail-label {
      display: block;
      margin: var(--base-size-8) auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      letter-spacing: 0;
      line-height: 1;
      text-orientation: mixed;
      text-transform: uppercase;
      transform: rotate(180deg);
      writing-mode: vertical-rl;
    }

    @media (max-width: 640px) {
      :host,
      :host([data-collapsed]) {
        --lv-sub-sidebar-width: 100%;
        width: 100%;
        height: auto;
        min-height: auto;
        overflow: hidden;
        border-right: 0;
        border-bottom: var(--lv-border-muted);
      }

      aside {
        position: static;
        width: 100%;
        height: auto;
        max-height: none;
        grid-template-rows: auto;
      }

      .top-row {
        padding-block: var(--lv-space-control) var(--base-size-6);
      }

      nav {
        display: flex;
        gap: var(--base-size-4);
        overflow-x: auto;
        overflow-y: hidden;
        padding-block: var(--base-size-6) var(--lv-space-control);
        scrollbar-gutter: auto;
      }

      .rail-label,
      .hover-title {
        display: none;
      }

      .item-link,
      .item-link.unnumbered {
        width: max-content;
        min-width: max-content;
      }
    }

    @keyframes rail-title-fade-in {
      from {
        opacity: 0;
      }

      to {
        opacity: 1;
      }
    }

    @keyframes rail-title-name-fold-out {
      from {
        opacity: 0;
        transform: translateX(-4px) scaleX(0.86);
      }

      to {
        opacity: 1;
        transform: translateX(0) scaleX(1);
      }
    }

  `

  updated(changed: PropertyValues<this>): void {
    const config = this.resolvedConfig
    if (changed.has('config')) {
      this.syncCollapsedStorage(config.storageKey)
    }
    this.toggleAttribute('data-collapsed', this.isCollapsed(config))
    if (changed.has('config')) {
      this.scrollActiveItemIntoView()
    }
  }

  render() {
    const config = this.resolvedConfig
    const collapsed = this.isCollapsed(config)
    return html`
      <aside aria-label=${config.ariaLabel}>
        <header>
          <div class="top-row">
            <strong class="section-title">${config.label}</strong>
            <button
              class="collapse"
              type="button"
              ?hidden=${!config.collapsible}
              aria-label=${collapsed ? `Expand ${config.label}` : `Collapse ${config.label}`}
              aria-pressed=${String(collapsed)}
              title=${collapsed ? `Expand ${config.label}` : `Collapse ${config.label}`}
              @click=${() => this.toggleCollapsed(config.storageKey)}
            >
              ${icon(collapsed ? 'chevron-right' : 'chevron-left')}
            </button>
          </div>
        </header>

        <nav aria-label=${config.ariaLabel} @scroll=${this.hideHoverTitle}>
          <span class="rail-label" aria-hidden="true">${config.railLabel}</span>
          ${config.items.length === 0 ? html`<div class="empty">${config.emptyText}</div>` : null}
          ${config.items.map((item, index) => this.renderItem(config, item, index, config.items.length))}
        </nav>
        ${collapsed && this.hoverTitle ? html`
          <div
            class="hover-title"
            style=${`top:${this.hoverTitle.top}px`}
            ?data-active=${this.hoverTitle.active}
          >
            ${this.hoverTitle.index ? html`<span class="hover-title-index" aria-hidden="true">${this.hoverTitle.index}</span>` : null}
            <span class="hover-title-name">${this.hoverTitle.title}</span>
          </div>
        ` : null}
      </aside>
    `
  }

  private get resolvedConfig(): ResolvedConfig {
    const label = cleanText(this.config.label) || defaultConfig.label
    return {
      label,
      railLabel: cleanText(this.config.railLabel) || label,
      ariaLabel: cleanText(this.config.ariaLabel) || label,
      storageKey: cleanText(this.config.storageKey) || defaultConfig.storageKey,
      activeId: cleanText(this.config.activeId),
      emptyText: cleanText(this.config.emptyText) || defaultConfig.emptyText,
      disabled: Boolean(this.config.disabled),
      collapsible: this.config.collapsible !== false,
      numbered: this.config.numbered !== false,
      items: Array.isArray(this.config.items) ? this.config.items.filter((item) => cleanText(item.id) !== '') : [],
    }
  }

  private renderItem(config: ResolvedConfig, item: SubSidebarItem, index: number, count: number) {
    const active = Boolean(item.active || item.id === config.activeId)
    const indexLabel = formatItemNumber(index, count)
    const title = cleanText(item.title) || item.id
    const disabled = Boolean(config.disabled || item.disabled)
    const hoverIndex = config.numbered ? indexLabel : ''
    const content = html`
      ${config.numbered ? html`<span class="item-index" aria-hidden="true">${indexLabel}</span>` : null}
      <span class="item-text">
        <span class="item-title-row">
          <span class="item-title">${title}</span>
          ${item.pending ? html`<lv-loading-spinner class="pending-spinner" size="small" aria-label="Title loading"></lv-loading-spinner>` : null}
        </span>
        ${cleanText(item.meta) ? html`<span class="item-meta">${item.meta}</span>` : null}
      </span>
    `
    const listeners = {
      mouseenter: (event: MouseEvent) => this.showHoverTitle(event, title, hoverIndex, active),
      mouseleave: this.hideHoverTitle,
      focus: (event: FocusEvent) => this.showHoverTitle(event, title, hoverIndex, active),
      blur: this.hideHoverTitle,
    }

    if (item.href) {
      return html`
        <a
          class=${`item-link${config.numbered ? '' : ' unnumbered'}`}
          href=${item.href}
          aria-current=${active ? 'page' : 'false'}
          aria-label=${title}
          @mouseenter=${listeners.mouseenter}
          @mouseleave=${listeners.mouseleave}
          @focus=${listeners.focus}
          @blur=${listeners.blur}
        >
          ${content}
        </a>
      `
    }

    return html`
      <button
        class=${`item-link${config.numbered ? '' : ' unnumbered'}`}
        type="button"
        aria-current=${active ? 'page' : 'false'}
        aria-label=${title}
        ?disabled=${disabled}
        @click=${() => this.selectItem(item.id, disabled)}
        @mouseenter=${listeners.mouseenter}
        @mouseleave=${listeners.mouseleave}
        @focus=${listeners.focus}
        @blur=${listeners.blur}
      >
        ${content}
      </button>
    `
  }

  private selectItem(id: string, disabled: boolean): void {
    if (!id || disabled) return
    this.dispatchEvent(new CustomEvent('lv-sub-sidebar-select', {
      bubbles: true,
      composed: true,
      detail: { id },
    }))
  }

  private toggleCollapsed(storageKey: string): void {
    if (!this.resolvedConfig.collapsible) return
    this.collapsed = !this.collapsed
    try {
      localStorage.setItem(storageKey, String(this.collapsed))
    } catch {
      // Session state still updates when storage is unavailable.
    }
  }

  private syncCollapsedStorage(storageKey: string): void {
    if (storageKey === this.loadedStorageKey) return
    this.loadedStorageKey = storageKey
    this.collapsed = storedCollapsed(storageKey)
  }

  private scrollActiveItemIntoView(): void {
    requestAnimationFrame(() => {
      const active = this.renderRoot.querySelector<HTMLElement>('.item-link[aria-current="page"]')
      active?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    })
  }

  private showHoverTitle(event: MouseEvent | FocusEvent, title: string, index: string, active: boolean): void {
    if (!this.isCollapsed(this.resolvedConfig)) return
    const target = event.currentTarget
    const aside = this.renderRoot.querySelector<HTMLElement>('aside')
    if (!(target instanceof HTMLElement) || !aside) return
    const targetRect = target.getBoundingClientRect()
    const asideRect = aside.getBoundingClientRect()
    this.hoverTitle = {
      index,
      title,
      top: targetRect.top - asideRect.top + targetRect.height / 2,
      active,
    }
  }

  private hideHoverTitle = (): void => {
    this.hoverTitle = undefined
  }

  private isCollapsed(config: ResolvedConfig): boolean {
    return config.collapsible && this.collapsed
  }
}

function storedCollapsed(storageKey: string): boolean {
  try {
    return localStorage.getItem(storageKey) === 'true'
  } catch {
    return false
  }
}

function formatItemNumber(index: number, count: number): string {
  const number = String(index + 1)
  return count >= 10 ? number.padStart(2, '0') : number
}

function cleanText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function icon(name: 'chevron-left' | 'chevron-right') {
  const icons: Record<'chevron-left' | 'chevron-right', IconNode> = {
    'chevron-left': ChevronLeft,
    'chevron-right': ChevronRight,
  }

  return lucideIcon(icons[name])
}

if (!customElements.get('lv-sub-sidebar')) {
  customElements.define('lv-sub-sidebar', SubSidebar)
}
