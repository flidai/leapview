import {
  BookOpen,
  Boxes,
  Cable,
  ChartColumn,
  Component,
  LayoutDashboard,
  ListFilter,
  PanelTop,
  Plug,
  Ruler,
  Sigma,
  SquareDashedMousePointer,
  Table2,
  TableProperties,
  Waypoints,
  Workflow,
  type IconNode,
} from 'lucide'

export type AssetPresentation = {
  icon: IconNode
  iconName: string
  token: string
}

const defaultPresentation: AssetPresentation = {
  icon: Component,
  iconName: 'asset',
  token: 'default',
}

/**
 * Canonical browser presentation for project resources. Resource aliases used by
 * agent references are normalized here so lists, detail pages, and chat all use
 * the same glyph and asset color token.
 */
export function assetPresentation(type: string): AssetPresentation {
  switch (type.trim().toLocaleLowerCase()) {
    case 'catalog':
      return { icon: BookOpen, iconName: 'catalog', token: 'catalog' }
    case 'project':
      return { icon: Boxes, iconName: 'project', token: 'catalog' }
    case 'connection':
      return { icon: Plug, iconName: 'connection', token: 'connection' }
    case 'dashboard':
      return { icon: LayoutDashboard, iconName: 'dashboard', token: 'dashboard' }
    case 'field':
    case 'dimension':
      return { icon: Ruler, iconName: 'field', token: 'dimension' }
    case 'filter':
      return { icon: ListFilter, iconName: 'filter', token: 'filter' }
    case 'metric':
      return { icon: Sigma, iconName: 'metric', token: 'metric' }
    case 'model':
      return { icon: TableProperties, iconName: 'model', token: 'model' }
    case 'dataset':
      return { icon: Table2, iconName: 'dataset', token: 'dataset' }
    case 'page':
      return { icon: PanelTop, iconName: 'page', token: 'page' }
    case 'page_item':
      return { icon: Component, iconName: 'page-item', token: 'page' }
    case 'relationship':
      return { icon: Workflow, iconName: 'relationship', token: 'dimension' }
    case 'semantic_model':
      return { icon: Waypoints, iconName: 'semantic-model', token: 'semantic-model' }
    case 'source':
      return { icon: Cable, iconName: 'source', token: 'source' }
    case 'table':
      return { icon: Table2, iconName: 'table', token: 'table' }
    case 'visual':
      return { icon: ChartColumn, iconName: 'visual', token: 'visual' }
    case 'visual_element':
      return { icon: SquareDashedMousePointer, iconName: 'visual-element', token: 'visual' }
    case 'pipeline':
    case 'refresh_pipeline':
      return { icon: Workflow, iconName: 'pipeline', token: 'default' }
    default:
      return defaultPresentation
  }
}

export function assetAccentColor(type: string): string | undefined {
  const token = assetPresentation(type).token
  return token === 'default' ? undefined : `var(--lv-asset-${token}-accent, var(--lv-fg-muted))`
}
