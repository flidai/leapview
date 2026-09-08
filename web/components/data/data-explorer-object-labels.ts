import { Database, Eye, Server, Table2, type IconNode } from 'lucide'

export function iconForLayer(layer: string): IconNode {
  switch (layer) {
    case 'source':
      return Server
    case 'semantic_view':
      return Eye
    case 'model':
      return Table2
    default:
      return Database
  }
}

export function layerLabel(layer: string): string {
  switch (layer) {
    case 'source':
      return 'Source'
    case 'model':
      return 'Model'
    case 'semantic_view':
      return 'Semantic view'
    default:
      return label(layer)
  }
}

export function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}
