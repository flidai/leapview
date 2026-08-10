import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { MoreHorizontal, Search } from 'lucide'
import type { ChatConversationSummary } from '../../generated/signals'
import { jsonAttribute } from '../shared/json-attribute'
import { lucideIcon } from '../shared/lucide-icons'

class LeapViewChatList extends LitElement {
  @property({ converter: jsonAttribute<ChatConversationSummary[]>([]) }) conversations: ChatConversationSummary[] = []
  @property({ attribute: 'active-conversation-id' }) activeConversationId = ''
  @state() private search = ''

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
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
      letter-spacing: 0;
    }

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
      font-weight: var(--base-text-weight-semibold);
      box-shadow: var(--lv-button-shadow-resting, none);
    }

    .new-chat-link:hover,
    .new-chat-link:focus-visible {
      border-color: var(--lv-button-border-hover, var(--lv-line-default));
      background: var(--lv-button-bg-hover, var(--lv-bg-control-hover));
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent, var(--lv-accent)));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
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
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
      transition: opacity var(--duration-fast) var(--ease-lv);
    }

    .row-actions {
      position: absolute;
      z-index: 2;
      inset-block: 0;
      right: 0;
      display: flex;
      align-items: center;
      padding-right: var(--base-size-8);
      opacity: 0;
      pointer-events: none;
      transition: opacity var(--duration-fast) var(--ease-lv);
    }

    tbody tr:hover .date,
    tbody tr:focus-within .date {
      opacity: 0;
    }

    tbody tr:hover .row-actions,
    tbody tr:focus-within .row-actions {
      opacity: 1;
      pointer-events: auto;
    }

    .options-button {
      display: grid;
      width: var(--lv-button-height, var(--control-medium-size));
      height: var(--lv-button-height, var(--control-medium-size));
      place-items: center;
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
      color: var(--lv-button-invisible-icon-rest, var(--lv-fg-muted));
      cursor: pointer;
      padding: 0;
    }

    .options-button:hover,
    .options-button:focus-visible {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-muted)));
      background: var(--lv-button-invisible-bg-hover, var(--lv-bg-hover));
      color: var(--lv-fg-default);
      outline: 0;
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
      padding: var(--base-size-16) 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
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

      tbody tr:hover .date,
      tbody tr:focus-within .date {
        opacity: 1;
      }
    }
  `

  render() {
    const conversations = Array.isArray(this.conversations) ? this.conversations : []
    const query = this.search.trim().toLocaleLowerCase()
    const visible = query
      ? conversations.filter((conversation) => conversationTitle(conversation).toLocaleLowerCase().includes(query))
      : conversations
    const empty = query ? 'No matching chats.' : 'No chats yet.'

    return html`
      <section class="shell" aria-label="Chat history">
        <div class="header">
          <h2>Chats</h2>
          <a class="new-chat-link" href="/chats/new">New chat</a>
        </div>
        <label class="toolbar">
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
        </label>
        ${visible.length === 0 ? html`<span class="empty">${empty}</span>` : html`
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
    const href = `/chats/${conversation.id}`
    return html`
      <tr data-active=${String(conversation.id === this.activeConversationId)}>
        <td class="primary-cell">
          <a class="primary-link" href=${href} data-primary="true" aria-label=${title}></a>
          <div class="row-content">
            <span class="title">${title}</span>
            <time class="date" datetime=${conversation.updatedAt}>${conversation.updatedAt ? shortDate(conversation.updatedAt) : ''}</time>
          </div>
          <div class="row-actions">
            <button class="options-button" type="button" aria-label=${`More options for ${title}`} @click=${(event: Event) => this.emitOptions(event, conversation)}>
              ${lucideIcon(MoreHorizontal)}
            </button>
          </div>
        </td>
      </tr>
    `
  }

  private onSearchInput = (event: Event): void => {
    this.search = (event.target as HTMLInputElement).value
  }

  private emitOptions(event: Event, conversation: ChatConversationSummary): void {
    event.preventDefault()
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-chat-list-options', {
      detail: { conversationId: conversation.id },
      bubbles: true,
      composed: true,
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
