import { LayoutDashboard, type IconNode } from 'lucide'
import { lucideIconNodes } from '../../generated/lucide-icon-nodes'

export function lucideIconByCanonicalName(name: string): IconNode {
  return lucideIconNodes[name] ?? LayoutDashboard
}
