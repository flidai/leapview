import type { BytePair, FrontendBundleEvidence } from './frontend_bundle_budget'

function fail(path: string, message: string): never {
  throw new Error(`frontend bundle budget: ${path}: ${message}`)
}

/**
 * Independently audit the output directory so a new shipped JS file cannot be
 * silently left outside the logical-entry graph in the evidence report.
 */
export async function verifyFrontendBundleStaticCoverage(
  evidence: FrontendBundleEvidence,
  evidencePath: string,
): Promise<void> {
  const shippedJavaScript: string[] = []
  for await (const path of new Bun.Glob('static/**/*.{js,mjs}').scan({ cwd: '.', dot: true, onlyFiles: true })) {
    shippedJavaScript.push(path.replace(/^static\//, ''))
  }
  shippedJavaScript.sort()
  const covered = new Set(evidence.aggregate.files)
  const omitted = shippedJavaScript.filter((path) => !covered.has(path))
  if (omitted.length > 0) {
    fail(`${evidencePath}.aggregate.files`, `omits shipped JavaScript ${omitted.join(', ')}; add each file to a logical entry before checking budgets`)
  }
  const unexpected = evidence.aggregate.files.filter((path) => !shippedJavaScript.includes(path))
  if (unexpected.length > 0) {
    fail(`${evidencePath}.aggregate.files`, `references non-shipped JavaScript ${unexpected.join(', ')}`)
  }
}

export async function verifyFrontendBundleFiles(
  evidence: FrontendBundleEvidence,
  evidencePath: string,
): Promise<void> {
  const bytesByFile = new Map<string, BytePair>()
  for (const assetPath of evidence.aggregate.files) {
    const file = Bun.file(`static/${assetPath}`)
    if (!(await file.exists())) fail(`${evidencePath}.aggregate.files`, `emitted file static/${assetPath} is missing`)
    const bytes = new Uint8Array(await file.arrayBuffer())
    bytesByFile.set(assetPath, { rawBytes: bytes.byteLength, gzipBytes: Bun.gzipSync(bytes).byteLength })
  }
  for (const [name, measurement] of Object.entries(evidence.entries)) {
    const actual = measurement.files.reduce((total, assetPath) => {
      const bytes = bytesByFile.get(assetPath)
      if (!bytes) fail(`${evidencePath}.entries.${name}.files`, `file ${assetPath} is not present in aggregate.files`)
      return {
        rawBytes: total.rawBytes + bytes.rawBytes,
        gzipBytes: total.gzipBytes + bytes.gzipBytes,
      }
    }, { rawBytes: 0, gzipBytes: 0 })
    if (actual.rawBytes !== measurement.rawBytes || actual.gzipBytes !== measurement.gzipBytes) {
      fail(`${evidencePath}.entries.${name}`, `byte totals do not match emitted files (evidence ${measurement.rawBytes}/${measurement.gzipBytes}, actual ${actual.rawBytes}/${actual.gzipBytes})`)
    }
  }
  const actualAggregate = evidence.aggregate.files.reduce((total, assetPath) => {
    const bytes = bytesByFile.get(assetPath)!
    return {
      rawBytes: total.rawBytes + bytes.rawBytes,
      gzipBytes: total.gzipBytes + bytes.gzipBytes,
    }
  }, { rawBytes: 0, gzipBytes: 0 })
  if (actualAggregate.rawBytes !== evidence.aggregate.rawBytes || actualAggregate.gzipBytes !== evidence.aggregate.gzipBytes) {
    fail(`${evidencePath}.aggregate`, `byte totals do not match emitted files (evidence ${evidence.aggregate.rawBytes}/${evidence.aggregate.gzipBytes}, actual ${actualAggregate.rawBytes}/${actualAggregate.gzipBytes})`)
  }
  await verifyFrontendBundleStaticCoverage(evidence, evidencePath)
}
