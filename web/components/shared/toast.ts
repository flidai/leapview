import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { repeat } from 'lit/directives/repeat.js'
import { CircleAlert, CircleCheck, Info, X } from 'lucide'
import { lucideIcon } from './lucide-icons'

export type ToastTone = 'success' | 'info' | 'error'
export type ToastOptions = {
  message: string
  tone?: ToastTone
  durationMs?: number
  action?: { label: string, onClick: () => void }
}

type ToastEntry = ToastOptions & { id: number, remainingMs: number, deadline: number }

/** A token-styled notification; callers own the lifecycle of declarative toasts. */
export class LeapViewToast extends LitElement {
  @property() message = ''
  @property() tone: ToastTone = 'info'
  @property() actionLabel = ''
  @property() actionAriaLabel = ''
  @property({ type: Boolean }) actionDisabled = false
  @property({ type: Boolean }) dismissible = false

  static styles = css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
    .toast { display: flex; box-sizing: border-box; width: 100%; min-height: 44px; align-items: center; gap: var(--base-size-8); border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-8) var(--base-size-12); animation: toast-appear var(--duration-fast) var(--ease-lv) both; }
    @keyframes toast-appear { from { opacity: 0; transform: translateY(-6px); } to { opacity: 1; transform: translateY(0); } }
    @media (prefers-reduced-motion: reduce) { .toast { animation: none; } }
    .icon { flex: none; display: inline-flex; color: var(--lv-fg-muted); }
    .toast[data-tone='success'] .icon { color: var(--lv-fg-success); }
    .toast[data-tone='error'] .icon { color: var(--lv-fg-danger); }
    .message { min-width: 0; flex: 1; overflow-wrap: anywhere; }
    button { display: inline-flex; flex: none; align-items: center; justify-content: center; min-height: 28px; border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-default); font: inherit; cursor: pointer; }
    button:hover { background: var(--lv-bg-control-hover); }
    button:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    button:disabled { opacity: .5; cursor: wait; }
    .action { padding: var(--base-size-4) var(--base-size-8); color: var(--lv-fg-accent); font-weight: var(--base-text-weight-semibold); }
    .dismiss { width: 28px; padding: 0; color: var(--lv-fg-muted); }
  `

  render() {
    const icon = this.tone === 'success' ? CircleCheck : this.tone === 'error' ? CircleAlert : Info
    return html`<div class="toast" data-tone=${this.tone} role=${this.tone === 'error' ? 'alert' : 'status'}>
      <span class="icon" aria-hidden="true">${lucideIcon(icon, { size: 16 })}</span>
      <span class="message">${this.message}</span>
      ${this.actionLabel ? html`<button class="action" type="button" aria-label=${this.actionAriaLabel || this.actionLabel} ?disabled=${this.actionDisabled} @click=${() => this.dispatchEvent(new CustomEvent('lv-toast-action', { bubbles: true, composed: true }))}>${this.actionLabel}</button>` : nothing}
      ${this.dismissible ? html`<button class="dismiss" type="button" aria-label="Dismiss notification" @click=${() => this.dispatchEvent(new CustomEvent('lv-toast-dismiss', { bubbles: true, composed: true }))}>${lucideIcon(X, { size: 16 })}</button>` : nothing}
    </div>`
  }
}

/** The same fixed stack works for imperative app feedback and declarative Undo flows. */
export class LeapViewToastRegion extends LitElement {
  @state() private entries: ToastEntry[] = []
  private timers = new Map<number, number>()
  private nextID = 0

  static styles = css`
    :host { position: fixed; z-index: var(--z-index-toast); top: max(var(--base-size-16), env(safe-area-inset-top)); right: max(var(--base-size-16), env(safe-area-inset-right)); display: block; box-sizing: border-box; width: min(22rem, calc(100vw - var(--base-size-32))); pointer-events: none; }
    .stack { display: grid; gap: var(--base-size-8); max-height: calc(100svh - var(--base-size-32)); overflow-y: auto; pointer-events: none; }
    lv-toast, ::slotted(lv-toast) { pointer-events: auto; }
    @media (max-width: 500px) { :host { top: auto; right: var(--base-size-8); bottom: calc(var(--base-size-64) + env(safe-area-inset-bottom)); width: calc(100vw - var(--base-size-16)); } }
  `

  override disconnectedCallback(): void {
    this.timers.forEach((timer) => window.clearTimeout(timer))
    this.timers.clear()
    super.disconnectedCallback()
  }

  show(options: ToastOptions): number {
    const id = ++this.nextID
    const durationMs = options.action ? 0 : Math.max(0, options.durationMs ?? 5_000)
    const entry = { ...options, id, remainingMs: durationMs, deadline: 0 }
    this.entries = [entry, ...this.entries.slice(0, 3)]
    for (const oldID of this.timers.keys()) {
      if (!this.entries.some((item) => item.id === oldID)) this.clearTimer(oldID)
    }
    if (durationMs) this.resume(id)
    return id
  }

  dismiss(id: number): void {
    this.clearTimer(id)
    this.entries = this.entries.filter((entry) => entry.id !== id)
  }

  private clearTimer(id: number): void {
    const timer = this.timers.get(id)
    if (timer !== undefined) window.clearTimeout(timer)
    this.timers.delete(id)
  }

  private pause(id: number): void {
    const entry = this.entries.find((item) => item.id === id)
    if (!entry || !this.timers.has(id)) return
    entry.remainingMs = Math.max(0, entry.deadline - Date.now())
    this.clearTimer(id)
  }

  private resume(id: number): void {
    const entry = this.entries.find((item) => item.id === id)
    if (!entry || !entry.remainingMs || this.timers.has(id)) return
    entry.deadline = Date.now() + entry.remainingMs
    this.timers.set(id, window.setTimeout(() => this.dismiss(id), entry.remainingMs))
  }

  render() {
    return html`<div class="stack">
      ${repeat(this.entries, (entry) => entry.id, (entry) => html`<lv-toast
        .message=${entry.message} .tone=${entry.tone ?? 'info'}
        .actionLabel=${entry.action?.label ?? ''} dismissible
        @mouseenter=${() => this.pause(entry.id)} @mouseleave=${() => this.resume(entry.id)}
        @focusin=${() => this.pause(entry.id)}
        @focusout=${(event: FocusEvent) => { if (!(event.currentTarget as Element).contains(event.relatedTarget as Node | null)) this.resume(entry.id) }}
        @lv-toast-action=${() => { entry.action?.onClick(); this.dismiss(entry.id) }}
        @lv-toast-dismiss=${() => this.dismiss(entry.id)}
      ></lv-toast>`)}
      <slot></slot>
    </div>`
  }
}

if (!customElements.get('lv-toast')) customElements.define('lv-toast', LeapViewToast)
if (!customElements.get('lv-toast-region')) customElements.define('lv-toast-region', LeapViewToastRegion)

export function showToast(options: ToastOptions): number {
  let region = document.querySelector<LeapViewToastRegion>('body > lv-toast-region[data-global]')
  if (!region) {
    region = document.createElement('lv-toast-region') as LeapViewToastRegion
    region.dataset.global = ''
    document.body.append(region)
  }
  return region.show(options)
}
