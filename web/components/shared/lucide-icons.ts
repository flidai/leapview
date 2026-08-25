import { svg } from 'lit'
import { ifDefined } from 'lit/directives/if-defined.js'
import { unsafeSVG } from 'lit/directives/unsafe-svg.js'
import type { IconNode, SVGProps } from 'lucide'

type LucideIconOptions = {
  className?: string
  color?: string
  size?: number | string
  strokeWidth?: number | string
}

export function lucideIcon(iconNode: IconNode, options: LucideIconOptions = {}) {
  const size = String(options.size ?? 16)
  const strokeWidth = String(options.strokeWidth ?? 2)

  return svg`
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width=${size}
      height=${size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width=${strokeWidth}
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      data-lucide="icon"
      class=${ifDefined(options.className)}
      style=${ifDefined(options.color ? `color:${options.color}` : undefined)}
    >
      ${unsafeSVG(iconNodeToSvg(iconNode))}
    </svg>
  `
}

function iconNodeToSvg(iconNode: IconNode): string {
  return iconNode.map(([tag, attrs]) => `<${tag}${attrsToString(attrs)}></${tag}>`).join('')
}

function attrsToString(attrs: SVGProps): string {
  return Object.entries(attrs)
    .filter(([, value]) => value !== undefined)
    .map(([key, value]) => ` ${key}="${escapeAttr(String(value))}"`)
    .join('')
}

function escapeAttr(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
