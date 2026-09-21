import { css, html } from 'lit'
import { Archive, ChevronRight, Pin, PinOff } from 'lucide'
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
  .history-actions { position: absolute; right: var(--base-size-4); z-index: 3; display: flex; align-items: center; gap: var(--base-size-2); opacity: 0; }
  .history-action { display: inline-grid; width: var(--control-small-size); height: var(--control-small-size); place-items: center; padding: 0; border: 0; border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); cursor: pointer; }
  .history-row:hover .history-actions,
  .history-row:focus-within .history-actions { opacity: 1; }
  .history-action:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel); }
  .history-action.danger:hover { color: var(--lv-fg-danger); }
  .history-action:focus-visible { opacity: 1; outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  .history-row:hover, .history-row:focus-within { background: var(--lv-bg-panel-muted); }
  .history-row:hover .history-item, .history-row:focus-within .history-item { padding-right: calc((var(--control-small-size) * 2) + var(--base-size-12)); background: transparent; }
  @media (hover: none) {
    .history-actions { opacity: 1; }
    .history-row .history-item { padding-right: calc((var(--control-small-size) * 2) + var(--base-size-12)); }
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
      <div class="history-actions" aria-label=${`Quick actions for ${title}`}>
        <button class="history-action" type="button" aria-label=${`${item.pinned ? 'Unpin' : 'Pin'} ${title}`} title=${item.pinned ? 'Unpin chat' : 'Pin chat'} @click=${(event: MouseEvent) => runChatAction(event, item.pinned ? 'unpin' : 'pin', item, chatAction)}>${lucideIcon(item.pinned ? PinOff : Pin, { size: 16 })}</button>
        <button class="history-action" type="button" aria-label=${`Archive ${title}`} title="Archive chat" @click=${(event: MouseEvent) => runChatAction(event, 'archive', item, chatAction)}>${lucideIcon(Archive, { size: 16 })}</button>
      </div>
    </div>
  `
}

function runChatAction(event: MouseEvent, action: string, item: SidebarHistoryItem, chatAction: (action: string, item: SidebarHistoryItem) => void): void {
  event.stopPropagation()
  chatAction(action, item)
}
