import { css, html, nothing } from 'lit'
import { ChevronRight } from 'lucide'
import { lucideIcon } from './lucide-icons'

interface BreadcrumbItemBase {
  label: string
  prefix?: unknown
  className?: string
}

export type BreadcrumbItem = BreadcrumbItemBase & (
  | { current: true; href?: never }
  | { current?: false; href: string }
)

export const breadcrumbStyles = css`
  .breadcrumb {
    min-width: 0;
  }

  .breadcrumb ol {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-4);
    margin: 0;
    padding: 0;
    list-style: none;
    font: var(--lv-type-body);
  }

  .breadcrumb ol > li {
    display: inline-flex;
    min-width: 0;
    align-items: center;
  }

  .breadcrumb-separator {
    display: inline-flex;
    flex: 0 0 auto;
    align-items: center;
    color: var(--lv-fg-muted);
  }

  .breadcrumb-link,
  .breadcrumb h1 {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .breadcrumb-link {
    text-decoration: none;
  }

  .breadcrumb-link:hover,
  .breadcrumb-link:focus-visible {
    text-decoration: underline;
  }

  .breadcrumb h1 {
    margin: 0;
  }

  .breadcrumb-label {
    overflow: hidden;
    min-width: 0;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .breadcrumb-glyph {
    display: inline-grid;
    width: var(--base-size-16);
    height: var(--base-size-16);
    flex: 0 0 auto;
    place-items: center;
    border: 0;
    background: transparent;
  }
`

export function renderBreadcrumb(items: BreadcrumbItem[], label = 'Breadcrumb') {
  return html`
    <nav class="breadcrumb" aria-label=${label}>
      <ol>
        ${items.map((item, index) => html`
          ${index > 0 ? html`
            <li class="breadcrumb-separator" aria-hidden="true">
              ${lucideIcon(ChevronRight, { size: 14, strokeWidth: 1.75 })}
            </li>
          ` : nothing}
          <li class=${`breadcrumb-item ${item.className ?? ''}`} aria-current=${item.current ? 'page' : nothing}>
            ${item.current
              ? html`<h1>${item.prefix ?? nothing}<span class="breadcrumb-label">${item.label}</span></h1>`
              : html`<a class="breadcrumb-link" href=${item.href}>${item.prefix ?? nothing}<span class="breadcrumb-label">${item.label}</span></a>`}
          </li>
        `)}
      </ol>
    </nav>
  `
}
