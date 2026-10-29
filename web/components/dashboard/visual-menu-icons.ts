import { CircleX, Copy, Download, Focus, ImageDown, TableProperties, Waypoints, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

export type VisualMenuIcon = 'focus' | 'show-data' | 'explore' | 'copy-data' | 'export-csv' | 'download-image' | 'clear-selection'

export function visualMenuIcon(name: VisualMenuIcon) {
  const icons: Record<VisualMenuIcon, IconNode> = {
    focus: Focus,
    'show-data': TableProperties,
    explore: Waypoints,
    'copy-data': Copy,
    'export-csv': Download,
    'download-image': ImageDown,
    'clear-selection': CircleX,
  }

  return lucideIcon(icons[name])
}
