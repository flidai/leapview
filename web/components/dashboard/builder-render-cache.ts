import type { DashboardBuilderDatasetSignal, DashboardBuilderPageSignal, DashboardBuilderSignal, DashboardBuilderVisualTypeSignal } from '../../generated/signals'
import { buildSemanticCatalog, type BuilderCatalogField } from './builder-field-catalog'

// Create once per Lit update. Nested signal patches can mutate objects in place,
// so these reference-based caches must never survive into the next update.
export class BuilderRenderCache {
  private readonly catalogs = new WeakMap<DashboardBuilderDatasetSignal[], BuilderCatalogField[]>()
  private readonly visualTypes = new WeakMap<DashboardBuilderSignal, Map<string, DashboardBuilderVisualTypeSignal>>()
  private readonly mobileOrders = new WeakMap<DashboardBuilderPageSignal, Map<string, number>>()

  semanticCatalog(datasets: DashboardBuilderDatasetSignal[]): BuilderCatalogField[] {
    const cached = this.catalogs.get(datasets)
    if (cached) return cached
    const catalog = buildSemanticCatalog(datasets)
    this.catalogs.set(datasets, catalog)
    return catalog
  }

  visualCatalogEntry(type: string, builder: DashboardBuilderSignal | null): DashboardBuilderVisualTypeSignal | undefined {
    if (!builder) return undefined
    let entries = this.visualTypes.get(builder)
    if (!entries) {
      entries = new Map()
      for (const entry of builder.visualCatalog ?? []) {
        if (!entries.has(entry.type)) entries.set(entry.type, entry)
      }
      this.visualTypes.set(builder, entries)
    }
    return entries.get(type)
  }

  mobileComponentOrder(componentID: string, page: DashboardBuilderPageSignal): number {
    let order = this.mobileOrders.get(page)
    if (!order) {
      const components = [...page.visuals, ...(page.filterComponents ?? [])]
        .sort((left, right) => left.placement.row - right.placement.row || left.placement.col - right.placement.col || left.id.localeCompare(right.id))
      const indices = new Map<string, number>()
      components.forEach((item, index) => { if (!indices.has(item.id)) indices.set(item.id, index) })
      order = indices
      this.mobileOrders.set(page, order)
    }
    return order.get(componentID) ?? -1
  }
}
