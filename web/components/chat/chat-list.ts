import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { MessageSquareText, MoreHorizontal, Pencil, Pin, PinOff, Plus, Search, Trash2 } from 'lucide'
import type { ChatConversationSummary } from '../../generated/signals'
import { jsonAttribute } from '../shared/json-attribute'
import { lucideIcon } from '../shared/lucide-icons'

class LeapViewChatList extends LitElement {
  @property({ converter: jsonAttribute<ChatConversationSummary[]>([]) }) conversations: ChatConversationSummary[] = []
  @property({ attribute: 'active-conversation-id' }) activeConversationId = ''
  @property({ type: Boolean, attribute: 'agent-enabled' }) agentEnabled = false
  @state() private search = ''
  @state() private openMenuID = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('keydown', this.onMenuKeydown)
    document.addEventListener('pointerdown', this.closeMenuOnOutsidePointerdown)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('keydown', this.onMenuKeydown)
    document.removeEventListener('pointerdown', this.closeMenuOnOutsidePointerdown)
    super.disconnectedCallback()
  }

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .shell {
      display: grid;
      align-content: start;
      gap: var(--base-size-16);
      width: 100%;
      max-width: var(--lv-chat-stack-width, 45rem);
      margin: 0 auto;
      padding: var(--base-size-16);
      box-sizing: border-box;
    }

    .header {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
    }

    h2 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-page-title);
      letter-spacing: 0;
    }

    .header-actions { display: flex; align-items: center; gap: var(--base-size-8); flex-wrap: wrap; }

    .toolbar {
      position: relative;
      display: block;
      min-width: 0;
    }

    .search-icon {
      position: absolute;
      top: 50%;
      left: var(--base-size-12);
      display: grid;
      width: var(--base-size-16);
      height: var(--base-size-16);
      place-items: center;
      color: var(--lv-fg-muted);
      transform: translateY(-50%);
      pointer-events: none;
    }

    .search {
      width: 100%;
      min-width: 0;
      height: var(--control-large-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-12) 0 var(--base-size-36);
      font: var(--lv-type-body);
    }

    .search::placeholder {
      color: var(--lv-fg-muted);
      opacity: 1;
    }

    .search:focus-visible {
      border-color: var(--borderColor-accent-emphasis, var(--lv-line-accent, var(--lv-accent)));
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent, var(--lv-accent)));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .new-chat-link {
      display: inline-flex;
      min-height: var(--lv-button-height, var(--control-medium-size));
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-border-rest, var(--lv-line-default));
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      background: var(--lv-button-bg-rest, var(--lv-bg-panel));
      color: var(--lv-button-fg-rest, var(--lv-fg-default));
      padding: 0 var(--lv-button-padding-inline-spacious, var(--control-medium-paddingInline-spacious, var(--base-size-16)));
      text-decoration: none;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      box-shadow: var(--lv-button-shadow-resting, none);
      gap: var(--base-size-8);
      cursor: pointer;
    }

    .new-chat-link[disabled] {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      box-shadow: none;
      opacity: 0.72;
    }

    .new-chat-link:hover,
    .new-chat-link:focus-visible {
      border-color: var(--lv-button-border-hover, var(--lv-line-default));
      background: var(--lv-button-bg-hover, var(--lv-bg-control-hover));
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent, var(--lv-accent)));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .new-chat-link.danger:hover,
    .new-chat-link.danger:focus-visible {
      color: var(--lv-fg-danger);
    }

    .table-wrap {
      min-width: 0;
      overflow-x: auto;
      padding: var(--base-size-4);
      margin: calc(-1 * var(--base-size-4));
    }

    table {
      width: 100%;
      min-width: 280px;
      border-collapse: separate;
      border-spacing: 0;
      table-layout: auto;
    }

    thead th {
      width: 0;
      height: 0;
      overflow: hidden;
      padding: 0;
      border: 0;
      line-height: 0;
    }

    tbody tr {
      position: relative;
      color: var(--lv-fg-default);
      cursor: pointer;
    }

    td {
      height: var(--control-medium-size);
      border-bottom: var(--lv-border-muted);
      background-clip: padding-box;
      padding: var(--lv-space-control) var(--base-size-12);
      vertical-align: middle;
    }

    tbody tr:first-child td {
      border-top: 0;
    }

    tbody tr:last-child td {
      border-bottom: 0;
    }

    tbody tr:hover td,
    tbody tr:focus-within td,
    tbody tr[data-active='true'] td {
      border-bottom-color: transparent;
      background: var(--lv-bg-hover);
    }

    tbody tr:hover td:first-child,
    tbody tr:focus-within td:first-child,
    tbody tr[data-active='true'] td:first-child {
      border-top-left-radius: var(--lv-radius-default);
      border-bottom-left-radius: var(--lv-radius-default);
    }

    tbody tr:hover td:last-child,
    tbody tr:focus-within td:last-child,
    tbody tr[data-active='true'] td:last-child {
      border-top-right-radius: var(--lv-radius-default);
      border-bottom-right-radius: var(--lv-radius-default);
    }

    .primary-cell {
      position: relative;
      overflow: hidden;
    }

    .primary-link {
      position: absolute;
      z-index: 1;
      inset: 0;
      border-radius: var(--lv-radius-default);
      outline: 0;
    }

    .primary-link:focus-visible {
      box-shadow: inset 0 0 0 var(--lv-border-width-focus) var(--lv-accent);
    }

    .row-content {
      display: flex;
      min-height: var(--control-medium-size);
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-16);
    }

    .row-status {
      display: grid;
      min-width: 98px;
      flex: 0 0 auto;
      align-items: center;
      justify-items: end;
      margin-left: auto;
    }

    .row-status > * {
      grid-area: 1 / 1;
    }

    .quick-actions {
      position: relative;
      z-index: 3;
      display: flex;
      flex: 0 0 auto;
      align-items: center;
      gap: var(--base-size-4);
      visibility: hidden;
      opacity: 0;
    }

    tbody tr:hover .quick-actions,
    tbody tr:focus-within .quick-actions {
      visibility: visible;
      opacity: 1;
    }

    tbody tr:hover .date,
    tbody tr:focus-within .date {
      visibility: hidden;
      opacity: 0;
    }

    .quick-action {
      display: inline-flex;
      width: 30px;
      height: 30px;
      align-items: center;
      justify-content: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      cursor: pointer;
    }

    .quick-action:hover,
    .quick-action:focus-visible {
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .quick-action.danger:hover,
    .quick-action.danger:focus-visible {
      color: var(--lv-fg-danger);
    }

    .options-menu { position: relative; z-index: 3; flex: 0 0 auto; }
    .options-menu > summary { display: inline-flex; width: 30px; height: 30px; align-items: center; justify-content: center; border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); cursor: pointer; list-style: none; }
    .options-menu > summary::-webkit-details-marker { display: none; }
    .options-menu > summary:hover, .options-menu > summary:focus-visible { background: var(--lv-bg-panel); color: var(--lv-fg-default); outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .options-panel { position: absolute; top: calc(100% + 4px); right: 0; display: grid; min-width: 180px; padding: 4px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
    .chat-action { display: grid; width: 100%; min-height: 32px; grid-template-columns: 20px minmax(0, 1fr); align-items: center; gap: 8px; padding: 6px 8px; border: 0; border-radius: var(--lv-radius-small); background: transparent; color: var(--lv-fg-default); cursor: pointer; text-align: left; font: var(--lv-type-body-compact); }
    .chat-action:hover { background: var(--lv-bg-panel-muted); }
    .chat-action.danger:hover { color: var(--lv-fg-danger); }
    .chat-action.unavailable { cursor: not-allowed; color: var(--lv-fg-muted); opacity: .6; }

    .title {
      overflow: hidden;
      min-width: 0;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .date {
      flex: 0 0 auto;
      margin-left: auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
      transition: opacity var(--duration-fast) var(--ease-lv);
    }

    svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    .empty {
      display: grid;
      min-height: min(26rem, calc(100svh - 9rem));
      place-content: center;
      justify-items: center;
      gap: var(--base-size-8);
      padding: var(--base-size-32) var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      text-align: center;
    }

    .empty-icon {
      display: grid;
      width: var(--base-size-40);
      height: var(--base-size-40);
      place-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
    }

    .empty-icon svg {
      width: var(--base-size-20);
      height: var(--base-size-20);
    }

    .empty-title {
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
    }

    .empty-detail {
      max-width: 28rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-secondary);
    }

    @media (max-width: 640px) {
      .shell {
        gap: var(--base-size-12);
        padding: var(--base-size-16);
      }

      .header {
        display: grid;
      }

      h2 {
        font: var(--lv-type-page-title);
      }

      .new-chat-link {
        width: 100%;
      }

      .quick-actions {
        visibility: visible;
        opacity: 1;
      }

      .date {
        display: none;
      }

    }

    @media (hover: none) {
      .quick-actions { visibility: visible; opacity: 1; }
      .date { display: none; }
    }
  `

  render() {
    const conversations = Array.isArray(this.conversations) ? this.conversations : []
    const query = this.search.trim().toLocaleLowerCase()
    const visible = query
      ? conversations.filter((conversation) => conversationTitle(conversation).toLocaleLowerCase().includes(query))
      : conversations
    const empty = query ? 'No matching chats' : 'No chats yet'

    return html`
      <section class="shell" aria-label="Chat history">
        <div class="header">
          <h2>Chats</h2>
          <div class="header-actions">
            <button class="new-chat-link danger" type="button" @click=${() => this.runBulkAction('delete_active')}>${lucideIcon(Trash2)}<span>Delete all chats</span></button>
            ${this.agentEnabled
              ? html`<a class="new-chat-link" href="/chats/new">${lucideIcon(Plus)}<span>New chat</span></a>`
              : html`<button class="new-chat-link" type="button" disabled title="Agent is not configured">${lucideIcon(Plus)}<span>New chat</span></button>`}
          </div>
        </div>
        ${conversations.length > 0 ? html`<label class="toolbar">
          <span class="search-icon" aria-hidden="true">${lucideIcon(Search)}</span>
          <input
            class="search"
            type="search"
            aria-label="Search chats"
            placeholder="Search chats..."
            autocomplete="off"
            spellcheck="false"
            .value=${this.search}
            @input=${this.onSearchInput}
          >
        </label>` : null}
        ${visible.length === 0 ? html`
          <div class="empty" role="status">
            <span class="empty-icon" aria-hidden="true">${lucideIcon(MessageSquareText)}</span>
            <strong class="empty-title">${empty}</strong>
            ${!query && !this.agentEnabled ? html`<span class="empty-detail">Agent is not configured.</span>` : null}
          </div>
        ` : html`
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th scope="col">Conversation</th>
                </tr>
              </thead>
              <tbody>
                ${visible.map((conversation) => this.renderRow(conversation))}
              </tbody>
            </table>
          </div>
        `}
      </section>
    `
  }

  private renderRow(conversation: ChatConversationSummary) {
    const title = conversationTitle(conversation)
    const href = `/chats/${encodeURIComponent(conversation.id)}`
    return html`
      <tr data-active=${String(conversation.id === this.activeConversationId)}>
        <td class="primary-cell">
          <a class="primary-link" href=${href} data-primary="true" aria-label=${title}></a>
          <div class="row-content">
            <span class="title">${title}</span>
            <div class="row-status">
              <time class="date" datetime=${conversation.updatedAt}>${conversation.updatedAt ? shortDate(conversation.updatedAt) : ''}</time>
              <div class="quick-actions" aria-label=${`Quick actions for ${title}`}>
                <button class="quick-action" type="button" aria-label=${`${conversation.pinned ? 'Unpin' : 'Pin'} ${title}`} title=${conversation.pinned ? 'Unpin chat' : 'Pin chat'} @click=${(event: MouseEvent) => this.runChatAction(event, conversation.pinned ? 'unpin' : 'pin', conversation, href)}>${lucideIcon(conversation.pinned ? PinOff : Pin, { size: 16 })}</button>
                <button class="quick-action danger" type="button" aria-label=${`Delete ${title}`} title="Delete chat" @click=${(event: MouseEvent) => this.runChatAction(event, 'delete', conversation, href)}>${lucideIcon(Trash2, { size: 16 })}</button>
              </div>
            </div>
            <details class="options-menu" ?open=${this.openMenuID === conversation.id} @toggle=${(event: Event) => this.onMenuToggle(event, conversation.id)}>
              <summary class="options-button" aria-label=${`More actions for ${title}`} title="More actions">${lucideIcon(MoreHorizontal)}</summary>
              <div class="options-panel" role="menu" aria-label=${`Actions for ${title}`}>
                <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => this.runChatAction(event, 'select', conversation, href)}><span aria-hidden="true"></span><span>Select</span></button>
                <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => this.runChatAction(event, conversation.pinned ? 'unpin' : 'pin', conversation, href)}>${lucideIcon(conversation.pinned ? PinOff : Pin, { size: 16 })}<span>${conversation.pinned ? 'Unpin chat' : 'Pin chat'}</span></button>
                <button class="chat-action" type="button" role="menuitem" @click=${(event: MouseEvent) => this.runChatAction(event, 'rename', conversation, href)}>${lucideIcon(Pencil, { size: 16 })}<span>Rename</span></button>
                <button class="chat-action unavailable" type="button" role="menuitem" disabled title="Chat projects are not supported yet"><span aria-hidden="true"></span><span>Add to project</span></button>
                <button class="chat-action danger" type="button" role="menuitem" @click=${(event: MouseEvent) => this.runChatAction(event, 'delete', conversation, href)}>${lucideIcon(Trash2, { size: 16 })}<span>Delete chat</span></button>
              </div>
            </details>
          </div>
        </td>
      </tr>
    `
  }

  private onSearchInput = (event: Event): void => {
    this.search = (event.target as HTMLInputElement).value
  }

  private onMenuToggle(event: Event, conversationID: string): void {
    const menu = event.currentTarget as HTMLDetailsElement
    if (menu.open) this.openMenuID = conversationID
    else if (this.openMenuID === conversationID) this.openMenuID = ''
  }

  private closeMenuOnOutsidePointerdown = (event: PointerEvent): void => {
    if (!this.openMenuID) return
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.options-menu[open]')
    if (menu && !event.composedPath().includes(menu)) {
      menu.open = false
      this.openMenuID = ''
    }
  }

  private onMenuKeydown = (event: KeyboardEvent): void => {
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.options-menu[open]')
    if (!menu) return
    if (event.key === 'Escape') {
      event.preventDefault()
      menu.open = false
      this.openMenuID = ''
      menu.querySelector<HTMLElement>('summary')?.focus()
      return
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || !event.composedPath().includes(menu)) return
    const items = Array.from(menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'))
    if (!items.length) return
    event.preventDefault()
    const current = items.indexOf(event.composedPath().find((target): target is HTMLButtonElement => target instanceof HTMLButtonElement) as HTMLButtonElement)
    const index = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? items.length - 1
        : event.key === 'ArrowDown'
          ? current < 0 ? 0 : (current + 1) % items.length
          : current < 0 ? items.length - 1 : (current - 1 + items.length) % items.length
    items[index].focus()
  }

  private runChatAction(event: MouseEvent, action: string, conversation: ChatConversationSummary, href: string): void {
    event.stopPropagation()
    const menu = (event.currentTarget as HTMLElement).closest('details')
    if (menu instanceof HTMLDetailsElement) menu.open = false
    this.openMenuID = ''
    this.dispatchEvent(new CustomEvent('lv-chat-action', {
      bubbles: true,
      composed: true,
      detail: { action, conversationId: conversation.id, title: conversation.title, href },
    }))
  }

  private runBulkAction(action: 'delete_active'): void {
    this.dispatchEvent(new CustomEvent('lv-chat-action', {
      bubbles: true,
      composed: true,
      detail: { action, conversationId: '' },
    }))
  }
}

function conversationTitle(conversation: ChatConversationSummary): string {
  return conversation.title || 'Conversation'
}

function shortDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(date)
}

if (!customElements.get('lv-chat-list')) customElements.define('lv-chat-list', LeapViewChatList)
