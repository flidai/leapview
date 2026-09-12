import { css, html } from 'lit'
import { Archive, Pin, PinOff, Trash2 } from 'lucide'
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
  .chat-actions { position: absolute; right: 4px; display: flex; gap: 2px; opacity: 0; pointer-events: none; background: var(--lv-bg-panel-muted); border-radius: var(--lv-radius-default); }
  .chat-action { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-muted); cursor: pointer; }
  .history-row:hover, .history-row:focus-within { background: var(--lv-bg-panel-muted); }
  .history-row:hover .history-item, .history-row:focus-within .history-item { padding-right: 96px; background: transparent; }
  .history-row:hover .chat-actions, .history-row:focus-within .chat-actions { opacity: 1; pointer-events: auto; }
  .chat-action:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel); }
  .chat-action.danger:hover { color: var(--lv-fg-danger); }
  .chat-action:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
  @media (hover: none) {
    .chat-actions { opacity: 1; pointer-events: auto; background: var(--lv-bg-panel); }
    .history-row .history-item { padding-right: 96px; }
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
    <section class="history" aria-label=${history.label || 'Chats'}>
      <strong class="history-label">${history.label || 'Chats'}</strong>
      <div class="history-list">
        ${items.length === 0 ? html`<span class="history-empty">${history.emptyText || 'No chats yet.'}</span>` : null}
        ${items.map((item) => renderSidebarChatHistoryItem(item, followInternalLink, chatAction))}
      </div>
    </section>
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
      <div class="chat-actions" role="group" aria-label=${`Actions for ${title}`}>
        <button class="chat-action" type="button" title=${item.pinned ? 'Unpin chat' : 'Pin chat'} aria-label=${item.pinned ? 'Unpin chat' : 'Pin chat'} @click=${() => chatAction(item.pinned ? 'unpin' : 'pin', item)}>${lucideIcon(item.pinned ? PinOff : Pin, { size: 16 })}</button>
        <button class="chat-action" type="button" title="Archive chat" aria-label="Archive chat" @click=${() => chatAction('archive', item)}>${lucideIcon(Archive, { size: 16 })}</button>
        <button class="chat-action danger" type="button" title="Delete chat" aria-label="Delete chat" @click=${() => chatAction('delete', item)}>${lucideIcon(Trash2, { size: 16 })}</button>
      </div>
    </div>
  `
}
