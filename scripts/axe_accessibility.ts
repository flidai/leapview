export type AxeViolation = {
  id: string
  impact: string | null
  help: string
  helpUrl: string
  nodes: Array<{
    target: unknown[]
    failureSummary?: string
  }>
}

const blockingImpacts = new Set(['serious', 'critical'])

export function blockingAxeViolations<T extends AxeViolation>(violations: readonly T[]): T[] {
  return violations.filter((violation) => violation.impact !== null && blockingImpacts.has(violation.impact))
}

export function formatAxeViolations(route: { label: string; path: string }, violations: readonly AxeViolation[]): string {
  const findings = violations.flatMap((violation) => violation.nodes.map((node) => [
    `- [${violation.impact ?? 'unknown'}] ${violation.id}: ${violation.help}`,
    `  Element: ${formatTarget(node.target)}`,
    `  Remediation: ${normalizeGuidance(node.failureSummary) || violation.help}`,
    `  Guidance: ${violation.helpUrl}`,
  ].join('\n')))

  return [
    `WCAG accessibility gate failed for ${route.label} (${route.path}).`,
    `${violations.length} serious/critical violation${violations.length === 1 ? '' : 's'} affected ${findings.length} element${findings.length === 1 ? '' : 's'}:`,
    ...findings,
  ].join('\n')
}

function formatTarget(target: unknown[]): string {
  if (target.length === 0) return '<unknown selector>'
  return target.map(formatTargetPart).join(' >>> ')
}

function formatTargetPart(value: unknown): string {
  if (Array.isArray(value)) return value.map(formatTargetPart).join(' >>> ')
  return String(value)
}

function normalizeGuidance(value: string | undefined): string {
  return (value ?? '').replace(/\s+/g, ' ').trim()
}
