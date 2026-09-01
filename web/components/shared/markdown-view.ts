import { LitElement, css, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js'
import DOMPurify from 'dompurify'
import MarkdownIt from 'markdown-it'

const markdown = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: false,
})
markdown.linkify.set({ fuzzyLink: true })

class MarkdownView extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: Boolean, reflect: true }) compact = false
  @property({ type: String }) emptyText = ''

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      line-height: var(--base-text-lineHeight-relaxed);
      overflow-wrap: anywhere;
      --lv-markdown-block-gap: var(--lv-chat-markdown-block-gap);
      --lv-markdown-list-indent: var(--lv-chat-markdown-list-indent);
      --lv-markdown-list-item-gap: var(--lv-chat-markdown-list-item-gap);
      --lv-markdown-code-radius: var(--lv-chat-code-radius);
      --lv-markdown-code-padding-block: var(--lv-chat-code-padding-block);
      --lv-markdown-code-padding-inline: var(--lv-chat-code-padding-inline);
      --lv-markdown-code-font-scale: var(--lv-chat-code-font-scale);
      --lv-markdown-pre-padding-block: var(--lv-chat-pre-padding-block);
      --lv-markdown-pre-padding-inline: var(--lv-chat-pre-padding-inline);
      --lv-markdown-quote-border-width: var(--lv-chat-quote-border-width);
      --lv-markdown-quote-padding-inline: var(--lv-chat-bubble-padding-block);
      --lv-markdown-link-underline-thickness: var(--lv-chat-link-underline-thickness);
      --lv-markdown-link-underline-offset: var(--lv-chat-link-underline-offset);
    }

    :host([compact]) {
      font: var(--lv-type-caption);
      line-height: var(--base-text-lineHeight-snug);
      --lv-markdown-block-gap: var(--base-size-12);
      --lv-markdown-list-indent: var(--base-size-16);
      --lv-markdown-list-item-gap: var(--base-size-4);
      --lv-markdown-code-radius: var(--lv-radius-default);
      --lv-markdown-code-padding-block: 0.1rem;
      --lv-markdown-code-padding-inline: 0.25rem;
      --lv-markdown-code-font-scale: 1;
      --lv-markdown-pre-padding-block: var(--base-size-12);
      --lv-markdown-pre-padding-inline: var(--base-size-12);
      --lv-markdown-quote-border-width: 3px;
      --lv-markdown-quote-padding-inline: var(--base-size-12);
      --lv-markdown-link-underline-thickness: var(--lv-border-width);
      --lv-markdown-link-underline-offset: 0.15em;
    }

    .markdown,
    .empty {
      min-width: 0;
    }

    .empty {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .markdown > * {
      margin-block: 0 var(--lv-markdown-block-gap);
    }

    .markdown > :last-child {
      margin-bottom: 0;
    }

    .markdown :is(h1, h2, h3, h4, h5, h6) {
      margin-block: var(--base-size-16) var(--base-size-8);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
    }

    .markdown > :is(h1, h2, h3, h4, h5, h6):first-child {
      margin-top: 0;
    }

    .markdown h1 {
      font: var(--lv-type-page-title);
    }

    .markdown h2 {
      font: var(--lv-type-section-title);
    }

    .markdown h3 {
      font: var(--lv-type-body);
    }

    .markdown :is(h4, h5, h6) {
      font: var(--lv-type-body);
    }

    .markdown :is(p, li, blockquote, td, th) {
      line-height: var(--base-text-lineHeight-normal);
    }

    .markdown ul,
    .markdown ol {
      padding-left: var(--lv-markdown-list-indent);
    }

    .markdown :is(ul, ol) :is(ul, ol) {
      margin-block: var(--base-size-4) 0;
    }

    .markdown li + li {
      margin-top: var(--lv-markdown-list-item-gap);
    }

    .markdown li > p {
      margin-block: 0 var(--base-size-8);
    }

    .markdown :is(strong, b) {
      font-weight: var(--base-text-weight-semibold);
    }

    .markdown :is(em, i) {
      font-style: italic;
    }

    .markdown s {
      text-decoration-line: line-through;
    }

    .markdown code {
      border-radius: var(--lv-markdown-code-radius);
      background: var(--lv-bg-control);
      padding: var(--lv-markdown-code-padding-block) var(--lv-markdown-code-padding-inline);
      font-family: var(--fontStack-monospace);
      font-size: var(--lv-markdown-code-font-scale);
    }

    .markdown pre {
      max-width: 100%;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: var(--lv-markdown-pre-padding-block) var(--lv-markdown-pre-padding-inline);
      line-height: var(--base-text-lineHeight-normal);
    }

    .markdown pre code {
      border-radius: 0;
      background: transparent;
      padding: 0;
      font: var(--lv-type-caption);
    }

    .markdown blockquote {
      border-left: var(--lv-markdown-quote-border-width) solid var(--lv-line-muted);
      padding-left: var(--lv-markdown-quote-padding-inline);
      color: var(--lv-fg-muted);
    }

    .markdown blockquote > :last-child {
      margin-bottom: 0;
    }

    .markdown a {
      color: var(--lv-fg-accent);
      text-decoration-thickness: var(--lv-markdown-link-underline-thickness);
      text-underline-offset: var(--lv-markdown-link-underline-offset);
    }

    .markdown hr {
      height: var(--lv-border-width);
      border: 0;
      background: var(--lv-line-muted);
    }

    .markdown table {
      display: block;
      max-width: 100%;
      overflow: auto;
      border-spacing: 0;
      border-collapse: collapse;
      font-size: inherit;
    }

    .markdown th,
    .markdown td {
      border: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      text-align: left;
      vertical-align: top;
    }

    .markdown th {
      background: var(--lv-bg-panel-muted);
      font-weight: var(--base-text-weight-semibold);
    }

    .markdown img {
      display: block;
      max-width: 100%;
      height: auto;
      border-radius: var(--lv-radius-default);
    }
  `

  render() {
    const value = this.value.trim()
    if (!value) {
      return this.emptyText ? html`<p class="empty">${this.emptyText}</p>` : nothing
    }
    return html`<div class="markdown">${unsafeHTML(renderMarkdownHTML(value))}</div>`
  }
}

export function renderMarkdownHTML(value: string): string {
  return DOMPurify.sanitize(markdown.render(value), {
    USE_PROFILES: { html: true },
  })
}

if (!customElements.get('lv-markdown-view')) customElements.define('lv-markdown-view', MarkdownView)
