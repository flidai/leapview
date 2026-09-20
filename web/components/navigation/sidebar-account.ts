import { css, html } from 'lit'
import { ChevronDown, LogOut, Settings } from 'lucide'
import { toggleAnchoredPopover } from '../shared/anchored-popover'
import { submitAuthForm } from '../shared/auth-form'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/user-avatar'

export const sidebarAccountStyles = css`
  .account-menu { min-width: 0; }
  .user-card { box-sizing: border-box; display: grid; width: 100%; min-width: 0; grid-template-columns: var(--control-small-size) minmax(0, 1fr) var(--base-size-16); min-height: calc(var(--control-medium-size) + var(--base-size-2)); align-items: center; gap: var(--base-size-4); border: 0; border-radius: var(--lv-radius-default); background: var(--lv-button-invisible-bg-rest); color: var(--lv-fg-default); padding: 0 var(--base-size-12); text-align: left; cursor: pointer; }
  .user-card:hover, .user-card[aria-expanded="true"] { background: var(--lv-button-invisible-bg-hover, var(--control-bgColor-hover)); }
  .user-card:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  .user-text, .user-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .user-name { display: block; font: var(--lv-type-body); font-weight: var(--base-text-weight-medium); }
  .user-chevron { display: flex; color: var(--lv-fg-muted); }
  .user-loading { grid-column: 1 / -1; }
  .account-popover { position: fixed; inset: auto; box-sizing: border-box; margin: 0; padding: var(--base-size-4); overflow: auto; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay, var(--lv-bg-panel)); color: var(--lv-fg-default); box-shadow: var(--lv-shadow-floating-lg); }
  .account-popover:popover-open { display: grid; gap: var(--base-size-2); }
  .account-role { margin: 0; padding: var(--base-size-8); overflow-wrap: anywhere; color: var(--lv-fg-muted); font: var(--lv-type-caption); border-bottom: var(--lv-border-muted); }
  .account-popover [role="menuitem"] { display: flex; box-sizing: border-box; width: 100%; min-height: var(--control-medium-size); align-items: center; gap: var(--base-size-8); padding: var(--base-size-8); border: var(--lv-border-transparent); border-radius: var(--lv-radius-default); background: var(--lv-button-invisible-bg-rest); color: var(--lv-fg-default); font: var(--lv-type-body); text-align: left; text-decoration: none; cursor: pointer; }
  .account-popover [role="menuitem"]:hover, .account-popover [role="menuitem"]:focus-visible { background: var(--lv-button-invisible-bg-hover, var(--control-bgColor-hover)); outline: var(--focus-outline); outline-offset: calc(-1 * var(--borderWidth-thick)); }
`

type Account = { name?: string; avatarUrl?: string; role?: string; settingsHref?: string; id: string; navigate: (event: MouseEvent, href: string) => void }

export function renderSidebarAccount(account: Account) {
  const name = account.name?.trim()
  if (!name) return html`<div class="user-card" aria-label="Loading account" aria-busy="true"><span class="user-name user-loading">Loading…</span></div>`
  const href = account.settingsHref || '/admin/profile'
  return html`<div class="account-menu">
    <button class="user-card" type="button" aria-label=${`Account menu for ${name}`} aria-haspopup="menu" aria-expanded="false" aria-controls=${account.id} title=${name} @click=${toggleAccount} @keydown=${triggerKeydown}>
      <lv-user-avatar .name=${name} .imageUrl=${account.avatarUrl ?? ''} aria-hidden="true"></lv-user-avatar>
      <span class="user-text"><strong class="user-name">${name}</strong></span>
      <span class="user-chevron" aria-hidden="true">${lucideIcon(ChevronDown, { size: 16 })}</span>
    </button>
    <div id=${account.id} class="account-popover" popover="auto" role="menu" aria-label="Account" @beforetoggle=${onToggle} @keydown=${menuKeydown}>
      ${account.role ? html`<p class="account-role" role="presentation">${account.role}</p>` : null}
      <a role="menuitem" tabindex="-1" href=${href} @click=${(event: MouseEvent) => { closeAccount(event.currentTarget as HTMLElement); account.navigate(event, href) }}>${lucideIcon(Settings, { size: 16 })}<span>Settings</span></a>
      <button role="menuitem" tabindex="-1" type="button" @click=${() => submitAuthForm('/auth/logout')}>${lucideIcon(LogOut, { size: 16 })}<span>Log out</span></button>
    </div>
  </div>`
}

function elements(target: HTMLElement) {
  const root = target.closest('.account-menu')!
  return { trigger: root.querySelector<HTMLButtonElement>('.user-card')!, menu: root.querySelector<HTMLElement>('.account-popover')! }
}
function toggleAccount(event: Event) {
  const { trigger, menu } = elements(event.currentTarget as HTMLElement)
  const open = toggleAnchoredPopover(trigger, menu, { minWidth: 224, maxWidth: 320, maxHeight: 240 })
  trigger.setAttribute('aria-expanded', String(open))
  if (open) {
    // Align the measured menu above a bottom-of-sidebar trigger without an empty gap.
    const anchor = trigger.getBoundingClientRect(), bounds = menu.getBoundingClientRect()
    if (bounds.bottom < anchor.top) menu.style.top = `${Math.max(8, anchor.top - bounds.height - 4)}px`
    menu.querySelector<HTMLElement>('[role="menuitem"]')?.focus({ preventScroll: true })
  }
}
function closeAccount(target: HTMLElement, restoreFocus = false) {
  const { trigger, menu } = elements(target)
  if (menu.matches(':popover-open')) menu.hidePopover()
  trigger.setAttribute('aria-expanded', 'false')
  if (restoreFocus) trigger.focus({ preventScroll: true })
}
function onToggle(event: Event) {
  const { trigger } = elements(event.currentTarget as HTMLElement)
  trigger.setAttribute('aria-expanded', String((event as Event & { newState: string }).newState === 'open'))
}
function triggerKeydown(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return
  event.preventDefault()
  const { menu } = elements(event.currentTarget as HTMLElement)
  if (!menu.matches(':popover-open')) toggleAccount(event)
  const items = menu.querySelectorAll<HTMLElement>('[role="menuitem"]')
  items[event.key === 'ArrowUp' ? items.length - 1 : 0]?.focus()
}
function menuKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' || event.key === 'Tab') {
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation() }
    closeAccount(event.currentTarget as HTMLElement, true)
    return
  }
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const menu = event.currentTarget as HTMLElement
  const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
  const index = items.indexOf(event.target as HTMLElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
  items[next]?.focus()
}
