export function hasMixedSpatialPrecision(summary: string): boolean {
  const match = /visible features:\s*(\d+) raw points?,\s*(\d+) aggregate cells?/.exec(summary)
  if (!match) return false
  return Number(match[1]) > 0 && Number(match[2]) > 0
}
