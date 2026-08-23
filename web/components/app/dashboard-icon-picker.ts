import { LitElement, css, html, unsafeCSS } from 'lit'
import { property, state } from 'lit/decorators.js'
import { RotateCcw, Search } from 'lucide'
import { canonicalLucideIconNames, lucideIconAliases } from '../../generated/lucide-icon-catalog'
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'

const colors = ['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'] as const
const rowHeight = 38
const columnCount = 9
const viewportHeight = 254

class DashboardIconPicker extends LitElement {
  @property() icon = 'layout-dashboard'
  @property() color = 'purple'
  @property() label = 'Dashboard'
  @state() private query = ''
  @state() private viewportScrollTop = 0

  static styles = css`
    :host { display: block; width: min(22.5rem, 100%); }
    .picker { overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--shadow-floating-large); color: var(--lv-fg-default); }
    .title { padding: var(--base-size-8) var(--base-size-12); border-bottom: var(--lv-border-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    .colors { display: flex; align-items: center; gap: var(--base-size-8); padding: var(--base-size-12); border-bottom: var(--lv-border-muted); }
    .color { width: var(--base-size-20); height: var(--base-size-20); padding: 0; border: 2px solid transparent; border-radius: var(--lv-radius-full); background: var(--display-gray-bgColor-muted); cursor: pointer; box-shadow: inset 0 0 0 8px var(--display-gray-fgColor); }
    .color[aria-pressed='true'] { outline: var(--borderWidth-thick) solid var(--lv-bg-panel); outline-offset: calc(var(--base-size-4) * -1); border-color: var(--lv-fg-default); }
    ${unsafeCSS(colors.map((color) => `.color.color-${color} { background: var(--display-${color}-bgColor-muted); box-shadow: inset 0 0 0 8px var(--display-${color}-fgColor); }`).join('\n'))}
    .search { position: relative; display: flex; align-items: center; border-bottom: var(--lv-border-muted); }
    .search svg { position: absolute; left: var(--base-size-12); color: var(--lv-fg-muted); pointer-events: none; }
    input { width: 100%; height: var(--control-medium-size); padding: 0 var(--base-size-12) 0 var(--base-size-36); border: 0; background: transparent; color: var(--lv-fg-default); font: var(--lv-type-body); outline: 0; }
    input::placeholder { color: var(--lv-fg-muted); }
    input:focus-visible { outline: var(--focus-outline); outline-offset: -2px; }
    .viewport { position: relative; height: ${viewportHeight}px; overflow-y: auto; scrollbar-width: thin; }
    .canvas { position: relative; width: 100%; }
    .icon { position: absolute; display: grid; width: 34px; height: 34px; place-items: center; padding: 0; border: 0; border-radius: var(--lv-radius-default); background: transparent; cursor: pointer; }
    .icon:hover, .icon:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    ${unsafeCSS(colors.map((color) => `
      .picker.color-${color} .icon { color: var(--display-${color}-fgColor, var(--lv-fg-default)); }
      .picker.color-${color} .icon[aria-pressed='true'] { background: var(--display-${color}-bgColor-muted, var(--lv-bg-control-hover)); }
    `).join('\n'))}
    .empty { display: grid; height: ${viewportHeight}px; place-items: center; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .footer { display: flex; justify-content: flex-end; padding: var(--base-size-8) var(--base-size-12); border-top: var(--lv-border-muted); }
    .reset { display: inline-flex; align-items: center; gap: var(--base-size-6); padding: var(--base-size-4) var(--base-size-8); border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-muted); cursor: pointer; font: var(--lv-type-caption); }
    .reset:hover, .reset:focus-visible { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
  `

  render() {
    const activeColor = colors.includes(this.color as typeof colors[number]) ? this.color : 'purple'
    const names = this.filteredNames()
    const rows = Math.ceil(names.length / columnCount)
    const firstRow = Math.max(0, Math.floor(this.viewportScrollTop / rowHeight) - 2)
    const lastRow = Math.min(rows, Math.ceil((this.viewportScrollTop + viewportHeight) / rowHeight) + 2)
    const visible = names.slice(firstRow * columnCount, lastRow * columnCount)
    return html`
      <section class=${`picker color-${activeColor}`} role="dialog" aria-label=${`Customize ${this.label}`}>
        <div class="title">Icon and color</div>
        <div class="colors" aria-label="Dashboard color">
          ${colors.map((color) => html`<button type="button" class=${`color color-${color}`} title=${color} aria-label=${color} aria-pressed=${this.color === color} @click=${() => this.select({ color })}></button>`)}
        </div>
        <label class="search">
          ${lucideIcon(Search, { size: 15, strokeWidth: 1.8 })}
          <input type="search" placeholder="Search icons…" aria-label="Search icons" .value=${this.query} @input=${this.search}>
        </label>
        ${names.length ? html`
          <div class="viewport" @scroll=${this.scrolled}>
            <div class="canvas" style=${`height:${rows * rowHeight}px`}>
              ${visible.map((name, index) => {
                const absolute = firstRow * columnCount + index
                const row = Math.floor(absolute / columnCount)
                const column = absolute % columnCount
                return html`<button type="button" class="icon" style=${`left:${10 + column * 38}px;top:${row * rowHeight + 2}px`} title=${name} aria-label=${name} aria-pressed=${this.icon === name} @click=${() => this.select({ icon: name })}>${lucideIcon(lucideIconByCanonicalName(name), { size: 17, strokeWidth: 1.8 })}</button>`
              })}
            </div>
          </div>
        ` : html`<div class="empty">No icons match “${this.query.trim()}”.</div>`}
        <div class="footer"><button type="button" class="reset" @click=${this.reset}>${lucideIcon(RotateCcw, { size: 13 })} Use defaults</button></div>
      </section>
    `
  }

  private filteredNames(): readonly string[] {
    const query = this.query.trim().toLocaleLowerCase()
    if (!query) return canonicalLucideIconNames
    return canonicalLucideIconNames.filter((name) => name.includes(query) || (lucideIconAliases[name] ?? []).some((alias) => alias.includes(query)))
  }

  private search = (event: Event) => { this.query = (event.currentTarget as HTMLInputElement).value; this.viewportScrollTop = 0 }
  private scrolled = (event: Event) => { this.viewportScrollTop = (event.currentTarget as HTMLElement).scrollTop }
  private reset = () => this.select({ icon: 'default', color: 'default' })
  private select(detail: { icon?: string; color?: string }) {
    this.dispatchEvent(new CustomEvent('lv-dashboard-appearance-select', { bubbles: true, composed: true, detail }))
  }
}

if (!customElements.get('lv-dashboard-icon-picker')) customElements.define('lv-dashboard-icon-picker', DashboardIconPicker)
