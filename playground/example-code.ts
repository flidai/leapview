import { LitElement } from 'lit'

const literal = (value: string) => value.replace(/\\/g, '\\\\').replace(/`/g, '\\`').replace(/\$\{/g, '\\${')
const text = (value: string) => literal(value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'))
const attribute = (value: string) => text(value).replace(/"/g, '&quot;')
const voidTags = new Set(['input', 'br', 'hr', 'img', 'wbr'])

/** Capture public Lit inputs and light DOM. Optional element overrides supply current values from public APIs. */
export function previewCode(host: LitElement, imports: string[], styles: string[] = [], inputOverrides?: ReadonlyMap<Element, Readonly<Record<string, unknown>>>): string {
  const preview = host.renderRoot.querySelector('[part~=preview]')
  if (!preview) return '// This example has no declarative preview. See Usage & events.'
  const serialize = (node: Node): string => {
    if (node.nodeType === Node.TEXT_NODE) return text(node.textContent || '')
    if (!(node instanceof Element)) return ''
    const bindings: string[] = []
    const publicAttributes = new Set<string>()
    if (node instanceof LitElement) {
      const ctor = node.constructor as typeof LitElement
      for (const [name, options] of ctor.elementProperties) {
        if (typeof name !== 'string' || options.state) continue
        const overrides = inputOverrides?.get(node)
        const value = overrides && Object.hasOwn(overrides, name) ? overrides[name] : (node as unknown as Record<string, unknown>)[name]
        if (value === undefined || typeof value === 'function' || value instanceof Node) continue
        try {
          const json = JSON.stringify(value)
          if (json !== undefined) bindings.push(`.${name}=\${${json}}`)
        } catch { continue }
        publicAttributes.add(typeof options.attribute === 'string' ? options.attribute : name.toLowerCase())
      }
    }
    const attrs = Array.from(node.attributes)
      .filter(attr => !publicAttributes.has(attr.name) && !attr.name.startsWith('on') && !attr.name.startsWith('data-'))
      .map(attr => `${attr.name}="${attribute(attr.value)}"`)
    if (node instanceof HTMLInputElement || node instanceof HTMLTextAreaElement || node instanceof HTMLSelectElement) {
      bindings.push(`.value=\${${JSON.stringify(node.value)}}`)
      if (node instanceof HTMLInputElement && ['checkbox', 'radio'].includes(node.type)) bindings.push(`.checked=\${${node.checked}}`)
    }
    const tag = node.localName
    const opening = `<${tag}${[...attrs, ...bindings].length ? ' ' + [...attrs, ...bindings].join(' ') : ''}>`
    return opening + (voidTags.has(tag) ? '' : (node instanceof LitElement && node.renderRoot === node ? '' : Array.from(node.childNodes).map(serialize).join('')) + `</${tag}>`)
  }
  return [
    "import { html } from 'lit'", ...imports,
    '', '// Load static/app.css and static/theme.js in the document.',
    ...(styles.length ? ['// In the owning Lit component: static styles = [' + styles.join(', ') + ']'] : []),
    '// Wire the public events listed in Usage & events to your application state.',
    'html`' + Array.from(preview.childNodes).map(serialize).join('').trim() + '`',
  ].join('\n')
}
