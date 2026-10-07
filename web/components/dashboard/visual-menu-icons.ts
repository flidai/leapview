import { CircleX, Copy, Download, Focus, TableProperties, Waypoints, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

export type VisualMenuIcon = 'focus' | 'show-data' | 'explore' | 'copy-data' | 'export-csv' | 'clear-selection'

export function visualMenuIcon(name: VisualMenuIcon) {
  const icons: Record<VisualMenuIcon, IconNode> = {
    focus: Focus,
    'show-data': TableProperties,
    explore: Waypoints,
    'copy-data': Copy,
    'export-csv': Download,
    'clear-selection': CircleX,
  }

  return lucideIcon(icons[name])
}
