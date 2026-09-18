import { css, html } from 'lit'
import { Archive, ChevronRight, MoreHorizontal, Pencil, Pin, PinOff, Trash2 } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/loading-spinner'

export type SidebarHistory = {
  label: string
  emptyText?: string
  items: SidebarHistoryItem[]
}

export type SidebarHistoryItem = {
  pinned?: boolean
  id: string
  title: string
  href: string
  active?: boolean
  pending?: boolean
}

export const sidebarChatHistoryStyles = css`
  .history {
    display: grid;
    gap: var(--base-size-4);
    min-height: 0;
    padding-top: var(--base-size-8);
  }

  .history-label {
    display: flex;
    align-items: center;
    gap: var(--base-size-4);
    min-height: var(--control-small-size);
    cursor: pointer;
    list-style: none;
    overflow: hidden;
    margin:
      0
      var(--control-xsmall-paddingInline-normal)
      0
      calc(var(--base-size-12) + var(--lv-border-width));
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
    letter-spacing: 0;
  }

  .history-label::-webkit-details-marker { display: none; }
  .history-label:hover { color: var(--lv-fg-default); }
  .history-label:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); border-radius: var(--lv-radius-default); }
  .history-label-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .history-chevron { display: inline-flex; flex-shrink: 0; }
  .history[open] .history-chevron { transform: rotate(90deg); }
  .history:not([open]) .history-list { display: none; }

  .history-list {
    display: grid;
    gap: var(--base-size-2);
    min-height: 0;
  }

  .nav-item.history-item {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .nav-item.history-item.pinned {
    grid-template-columns: auto minmax(0, 1fr) auto;
  }

  .history-row { position: relative; display: flex; align-items: center; min-width: 0; border-radius: var(--lv-radius-default); }
  .history-row .history-item { flex: 1; min-width: 0; }
  .chat-pin { display: inline-flex; flex-shrink: 0; color: var(--lv-fg-muted); }
  .chat-menu { position: absolute; right: 4px; z-index: 3; }
  .chat-menu > summary { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); cursor: pointer; list-style: none; opacity: 0; }
  .chat-menu > summary::-webkit-details-marker { display: none; }
  .chat-menu[open] > summary,
  .history-row:hover .chat-menu > summary,
  .history-row:focus-within .chat-menu > summary { opacity: 1; }
  .chat-menu > summary:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel); }
  .chat-menu > summary:focus-visible { opacity: 1; outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
  .chat-menu-panel { position: absolute; right: 0; top: calc(100% + 4px); display: grid; min-width: 176px; padding: 4px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
  .chat-action { display: grid; grid-template-columns: 20px minmax(0, 1fr); align-items: center; gap: 8px; width: 100%; min-height: 32px; padding: 6px 8px; border: 0; border-radius: var(--lv-radius-small); background: transparent; color: var(--lv-fg-default); cursor: pointer; text-align: left; font: var(--lv-type-body-compact); }
  .history-row:hover, .history-row:focus-within { background: var(--lv-bg-panel-muted); }
  .history-row:hover .history-item, .history-row:focus-within .history-item { padding-right: 36px; background: transparent; }
  .chat-action:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel); }
  .chat-action.danger:hover { color: var(--lv-fg-danger); }
  .chat-action.unavailable { cursor: not-allowed; color: var(--lv-fg-muted); opacity: .6; }
  .chat-action:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
  @media (hover: none) {
    .chat-menu > summary { opacity: 1; background: var(--lv-bg-panel); }
    .history-row .history-item { padding-right: 36px; }
  }

  .history-title {
    overflow: hidden;
    min-width: 0;
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  .history-empty {
    padding: var(--base-size-4) var(--base-size-12);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  @media (max-width: 640px) {
    .history,
    :host([data-collapsed]) .history {
      display: grid;
    }
  }
`

export function renderSidebarChatHistory(
  history: SidebarHistory | undefined,
  pendingRemovalIds: readonly string[],
  followInternalLink: (event: MouseEvent, href: string) => void,
  chatAction: (action: string, item: SidebarHistoryItem) => void,
) {
  if (!history) return null
  const pending = new Set(pendingRemovalIds)
  const items = (Array.isArray(history.items) ? history.items : []).filter(item => !pending.has(item.id))
  return html`
    <details class="history" open>
      <summary class="history-label">
        <span class="history-label-text">${history.label || 'Chats'}</span>
        <span class="history-chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
      </summary>
      <div class="history-list">
        ${items.length === 0 ? html`<span class="history-empty">${history.emptyText || 'No chats yet.'}</span>` : null}
        ${items.map((item) => renderSidebarChatHistoryItem(item, followInternalLink, chatAction))}
      </div>
    </details>
  `
}

function renderSidebarChatHistoryItem(
  item: SidebarHistoryItem,
  followInternalLink: (event: MouseEvent, href: string) => void,
  chatAction: (action: string, item: SidebarHistoryItem) => void,
) {
  const title = item.title || 'Conversation'
  return html`
    <div class="history-row">
      <a class=${`nav-item history-item${item.pinned ? ' pinned' : ''}`} href=${item.href} aria-current=${item.active ? 'page' : 'false'} aria-label=${title} title=${title} @click=${(event: MouseEvent) => followInternalLink(event, item.href)}>
        ${item.pinned ? html`<span class="chat-pin" title="Pinned chat" aria-label="Pinned chat">${lucideIcon(Pin, { size: 13 })}</span>` : null}
        <span class="history-title">${title}</span>
        ${item.pending ? html`<lv-loading-spinner size="small" aria-label="Title loading"></lv-loading-spinner>` : null}
      </a>
      <details class="chat-menu">
        <summary aria-label=${`More actions for ${title}`} title="More actions">${lucideIcon(MoreHorizontal, { size: 17 })}</summary>
        <div class="chat-menu-panel" role="menu" aria-label=${`Actions for ${title}`}>
          <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => runChatAction(event, 'select', item, chatAction)}> <span aria-hidden="true"></span><span>Select</span></button>
          <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => runChatAction(event, item.pinned ? 'unpin' : 'pin', item, chatAction)}>${lucideIcon(item.pinned ? PinOff : Pin, { size: 16 })}<span>${item.pinned ? 'Unpin chat' : 'Pin chat'}</span></button>
          <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => runChatAction(event, 'rename', item, chatAction)}>${lucideIcon(Pencil, { size: 16 })}<span>Rename</span></button>
          <button class="chat-action unavailable" type="button" role="menuitem" disabled title="Chat projects are not supported yet"> <span aria-hidden="true"></span><span>Add to project</span></button>
          <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => runChatAction(event, 'archive', item, chatAction)}>${lucideIcon(Archive, { size: 16 })}<span>Archive chat</span></button>
          <button class="chat-action danger" type="button" role="menuitem" @click=${(event: MouseEvent) => runChatAction(event, 'delete', item, chatAction)}>${lucideIcon(Trash2, { size: 16 })}<span>Delete chat</span></button>
        </div>
      </details>
    </div>
  `
}

function runChatAction(event: MouseEvent, action: string, item: SidebarHistoryItem, chatAction: (action: string, item: SidebarHistoryItem) => void): void {
  event.stopPropagation()
  const menu = (event.currentTarget as HTMLElement).closest('details')
  if (menu instanceof HTMLDetailsElement) menu.open = false
  chatAction(action, item)
}
