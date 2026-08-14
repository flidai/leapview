import { LayoutDashboard, type IconNode } from 'lucide'
import { lucideIconNodes } from '../../generated/lucide-icon-catalog'

export function lucideIconByCanonicalName(name: string): IconNode {
  return lucideIconNodes[name] ?? LayoutDashboard
}
