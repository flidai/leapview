import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'

type ScopeAssignments = {
  readonly slots: Map<string, number>
  nextSlot: number
}

/**
 * Owns automatic category-to-palette assignments for a dashboard page.
 * Assignments store palette positions rather than resolved colors so they
 * survive both data filtering and theme changes.
 */
export class CategoryColorRegistry {
  readonly #scopes = new Map<string, ScopeAssignments>()

  register(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, values: readonly unknown[]): void {
    const scope = categoryColorScope(envelope, ref)
    const assignments = this.#scopes.get(scope) ?? { slots: new Map<string, number>(), nextSlot: 0 }
    this.#scopes.set(scope, assignments)
    // Query completion order must not decide a dashboard's category colors.
    // Register each batch in canonical identity order so sibling visuals that
    // expose the same category domain converge on the same palette slots.
    const identities = [...new Set(values.map(categoryIdentity))].sort()
    for (const identity of identities) {
      if (assignments.slots.has(identity)) continue
      assignments.slots.set(identity, assignments.nextSlot++)
    }
  }

  color(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, value: unknown, context: RendererContext): string {
    this.register(envelope, ref, [value])
    const palette = context.colors.data
    if (palette.length === 0) return context.colors.accent
    const slot = this.#scopes.get(categoryColorScope(envelope, ref))?.slots.get(categoryIdentity(value)) ?? 0
    return palette[slot % palette.length]!
  }
}

const pageRegistries = new WeakMap<object, CategoryColorRegistry>()

export function categoryColorRegistryFor(container: HTMLElement): CategoryColorRegistry {
  const owner = dashboardPageFor(container) ?? container.ownerDocument ?? container
  let registry = pageRegistries.get(owner)
  if (!registry) {
    registry = new CategoryColorRegistry()
    pageRegistries.set(owner, registry)
  }
  return registry
}

function dashboardPageFor(container: HTMLElement): Element | undefined {
  let current: Node | undefined = container
  while (current) {
    const page = (current as Element).closest?.('lv-dashboard-page')
    if (page) return page
    const root: Node | undefined = current.getRootNode?.()
    const shadowHost: Element | undefined = root && 'host' in root ? (root as ShadowRoot).host : undefined
    current = shadowHost && shadowHost !== current ? shadowHost : undefined
  }
  return undefined
}

function categoryColorScope(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): string {
  const sourceRef = envelope.spec.datasets
    .find((dataset) => dataset.id === ref.dataset)?.fields
    .find((definition) => definition.id === ref.field)?.sourceRef?.trim()
  return sourceRef ? `source:${sourceRef}` : `visual:${envelope.visualID}:${ref.dataset}:${ref.field}`
}

export function categoryIdentity(value: unknown): string {
  if (value === null) return 'null:'
  if (value === undefined) return 'undefined:'
  if (typeof value === 'number') {
    if (Number.isNaN(value)) return 'number:NaN'
    if (Object.is(value, -0)) return 'number:-0'
  }
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return `${typeof value}:${String(value)}`
  }
  return `json:${JSON.stringify(value)}`
}
