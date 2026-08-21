import { LitElement, css, html } from 'lit'

type ArticleSection = { id: string; label: string; level: number }
type ArticleSectionNode = ArticleSection & { children: ArticleSectionNode[] }

class SiteArticleToc extends LitElement {
  private sections: ArticleSection[] = []
  private activeId = ''
  private visibleSectionIDs = new Map<string, string>()
  private observer?: IntersectionObserver

  static styles = css`
    :host {
      display: block;
      position: sticky;
      top: calc(var(--site-header-height) + var(--base-size-32));
      align-self: start;
      height: calc(100svh - var(--site-header-height) - var(--base-size-32));
      overflow: auto;
      scrollbar-width: none;
    }

    :host::-webkit-scrollbar {
      display: none;
    }

    h2 {
      margin: 0 0 0 var(--base-size-12);
      color: var(--lv-fg-subtle);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-normal);
      letter-spacing: 0.03em;
      line-height: 1.2;
      text-transform: uppercase;
    }

    ul {
      padding: 0;
      list-style: none;
    }

    ul#toc {
      position: relative;
      margin: 15px 0 0;
    }

    ul ul {
      margin: var(--base-size-2) 0 var(--base-size-2) 15px;
      border-left: var(--lv-border-muted);
    }

    ul ul ul {
      display: none;
    }

    li {
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-normal);
      letter-spacing: 0.005em;
      line-height: 1;
      list-style: none;
    }

    a {
      display: inline-block;
      overflow: hidden;
      max-width: 100%;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-6) var(--base-size-12);
      color: var(--lv-fg-subtle);
      line-height: 1;
      text-decoration: none;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    a:hover,
    a:focus-visible,
    li.current > a {
      color: var(--lv-fg-default);
    }

    a:focus-visible {
      outline: var(--focus-outline);
      outline-offset: calc(-1 * var(--focus-outline-offset));
    }
  `

  connectedCallback() {
    super.connectedCallback()
    requestAnimationFrame(() => this.collectSections())
  }

  disconnectedCallback() {
    this.observer?.disconnect()
    super.disconnectedCallback()
  }

  private collectSections() {
    const article = document.querySelector<HTMLElement>('.site-docs-article')
    const headings = Array.from(article?.querySelectorAll<HTMLElement>(':scope > h2, :scope > h3, :scope > h4') ?? [])
    const used = new Set<string>()
    this.sections = headings.map((heading) => {
      let id =
        heading.id ||
        heading.textContent
          ?.trim()
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, '-')
          .replace(/^-|-$/g, '') ||
        'section'
      const base = id
      let suffix = 2
      while (used.has(id)) id = `${base}-${suffix++}`
      used.add(id)
      heading.id = id
      return {
        id,
        label: heading.textContent?.trim() ?? '',
        level: Number(heading.tagName.slice(1)),
      }
    })
    this.visibleSectionIDs = new Map<string, string>()
    const indexVisibleSections = (nodes: ArticleSectionNode[], depth = 0, visibleAncestor = ''): void => {
      for (const node of nodes) {
        const visibleID = depth <= 1 ? node.id : visibleAncestor
        this.visibleSectionIDs.set(node.id, visibleID)
        indexVisibleSections(node.children, depth + 1, visibleID)
      }
    }
    indexVisibleSections(this.sectionTree())
    this.activeId = this.visibleSectionIDs.get(this.sections[0]?.id ?? '') ?? ''
    this.observer = new IntersectionObserver(
      (entries) => {
        const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0]
        const visibleID = visible?.target.id ? this.visibleSectionIDs.get(visible.target.id) : undefined
        if (visibleID && this.activeId !== visibleID) {
          this.setActiveSection(visibleID)
        }
      },
      { rootMargin: '-18% 0px -70% 0px', threshold: 0 },
    )
    headings.forEach((heading) => this.observer?.observe(heading))
    this.requestUpdate()
  }

  private setActiveSection(id: string): void {
    this.activeId = id
    this.requestUpdate()
    void this.updateComplete.then(() => this.revealActiveSection())
  }

  private revealActiveSection(): void {
    const active = this.renderRoot.querySelector<HTMLElement>('a.active')
    if (!active || active.getClientRects().length === 0) return
    const hostBounds = this.getBoundingClientRect()
    const activeBounds = active.getBoundingClientRect()
    const revealMargin = 8
    if (activeBounds.top < hostBounds.top + revealMargin) {
      this.scrollTop -= hostBounds.top + revealMargin - activeBounds.top
    } else if (activeBounds.bottom > hostBounds.bottom - revealMargin) {
      this.scrollTop += activeBounds.bottom - hostBounds.bottom + revealMargin
    }
  }

  private sectionTree(): ArticleSectionNode[] {
    const roots: ArticleSectionNode[] = []
    const stack: ArticleSectionNode[] = []

    for (const section of this.sections) {
      const node: ArticleSectionNode = { ...section, children: [] }
      while (stack.length && stack[stack.length - 1].level >= node.level) stack.pop()
      const parent = stack[stack.length - 1]
      if (parent) parent.children.push(node)
      else roots.push(node)
      stack.push(node)
    }

    return roots
  }

  private renderSections(sections: ArticleSectionNode[]): Array<ReturnType<typeof html>> {
    return sections.map(
      (section) => html`
        <li class=${section.id === this.activeId ? 'current' : ''}>
          <a class=${section.id === this.activeId ? 'active' : ''} data-level=${section.level} href=${`#${section.id}`}>${section.label}</a>
          ${
            section.children.length
              ? html`<ul>
                  ${this.renderSections(section.children)}
                </ul>`
              : null
          }
        </li>
      `,
    )
  }

  render() {
    if (!this.sections.length) return null
    return html`<nav aria-label="In this article">
      <h2>In this article</h2>
      <ul id="toc">
        ${this.renderSections(this.sectionTree())}
      </ul>
    </nav>`
  }
}

if (!customElements.get('lv-site-article-toc')) customElements.define('lv-site-article-toc', SiteArticleToc)
