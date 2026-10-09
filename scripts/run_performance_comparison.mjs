import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { verifyPerformanceReferenceEvidence } from './qualify_performance_reference.mjs'

const referenceFile = '.quality/performance-reference.json'
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex')
const fail = (message) => { throw new Error(message) }
const imagePattern = /^ghcr\.io\/flidai\/leapview@sha256:[0-9a-f]{64}$/
const execute = (command, args, env) => execFileSync(command, args, { env, stdio: 'inherit', timeout: 125 * 60 * 1000 })
const readReport = (path) => {
  if (statSync(path).size > 20 * 1024 * 1024) fail('qualification report exceeds bounded evidence size')
  return JSON.parse(readFileSync(path, 'utf8'))
}

export function requireSuccessfulReport(report, image, baselinePath) {
  if (report?.schemaVersion !== 1 || report.image !== image || report.result !== 'success' ||
      report.assertions?.environment !== true || report.assertions.absoluteBudgets !== true ||
      report.assertions.errorFree !== true || !Array.isArray(report.failures) || report.failures.length !== 0 ||
      (baselinePath && (report.assertions.comparisonTolerance !== true || report.comparison?.baseline !== baselinePath ||
        !Array.isArray(report.comparison.failures) || report.comparison.failures.length !== 0))) fail('qualification did not produce a successful report with the required comparison assertion')
}

// Reference identity comes only from the independently reviewed repository
// reference and its retained upstream bytes. The old report is admission
// evidence; the reference image is measured again on this runner before the
// candidate, with the same current controller, fixture and policy.
export function runProductionPerformanceComparison({ candidateCommit, candidateImage, evidenceDirectory }, dependencies = {}) {
  const env = { ...process.env }
  if (!/^[0-9a-f]{40}$/.test(candidateCommit ?? '') || !imagePattern.test(candidateImage ?? '')) fail('exact candidate commit and immutable production image are required')
  if (env.QUALIFICATION_PERFORMANCE_BASELINE) fail('ambient baseline paths cannot replace the reviewed reference')
  const source = dependencies.source ?? (() => execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim())
  const clean = dependencies.clean ?? (() => execFileSync('git', ['status', '--porcelain', '--untracked-files=no'], { encoding: 'utf8' }).trim() === '')
  const verifySource = () => {
    if (source() !== candidateCommit || !clean()) fail('controller checkout must match the exact clean candidate source')
  }
  verifySource()
  const exists = dependencies.exists ?? existsSync
  const read = dependencies.read ?? readFileSync
  const report = dependencies.report ?? readReport
  const run = dependencies.execute ?? execute
  const retain = dependencies.retain ?? ((path, value) => writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { flag: 'wx', mode: 0o600 }))
  const mkdir = dependencies.mkdir ?? ((path) => mkdirSync(path, { recursive: true, mode: 0o700 }))
  const candidateDirectory = resolve(evidenceDirectory)
  const qualification = (image, directory, baseline) => {
    verifySource()
    const childEnv = { ...env, LEAPVIEWCTL_ROOT: resolve('deploy/compose') }
    if (baseline) childEnv.QUALIFICATION_PERFORMANCE_BASELINE = baseline
    run('go', ['run', '-tags=duckdb_arrow', './cmd/leapviewctl', 'qualify', 'image', '--image', image,
      '--require-immutable', '--evidence-dir', directory], childEnv)
    verifySource()
  }
  if (!exists(referenceFile)) {
    qualification(candidateImage, candidateDirectory)
    requireSuccessfulReport(report(join(candidateDirectory, 'performance-report.json')), candidateImage, false)
    retain(join(candidateDirectory, 'comparison-mode.json'), { schemaVersion: 1, mode: 'absolute-only-bootstrap', candidateCommit, candidateImage, comparison: false })
    return 'absolute-only-bootstrap'
  }
  const reference = JSON.parse(read(referenceFile, 'utf8'))
  const admitted = (dependencies.verify ?? verifyPerformanceReferenceEvidence)(reference, { candidateCommit, candidateImage })
  const root = resolve(candidateDirectory + '-comparison')
  const referenceDirectory = join(root, 'reference')
  mkdir(root)
  retain(join(root, 'protocol.json'), { schemaVersion: 1, kind: 'serial-production-image-comparison',
    validatorCommit: candidateCommit, candidateImage, reference: admitted.reference,
    admittedHistoricalReportSHA256: admitted.reportSHA256,
    fixturePolicySHA256: hash(read('deploy/compose/qualification/performance-policy.json')),
    order: ['reference', 'candidate'],
    limitation: 'one serial qualification pair; no optimization adoption, variance calibration or user-p95 claim' })
  qualification(reference.image, referenceDirectory)
  const baselinePath = join(referenceDirectory, 'performance-report.json')
  requireSuccessfulReport(report(baselinePath), reference.image, false)
  qualification(candidateImage, candidateDirectory, baselinePath)
  requireSuccessfulReport(report(join(candidateDirectory, 'performance-report.json')), candidateImage, baselinePath)
  retain(join(candidateDirectory, 'comparison-mode.json'), { schemaVersion: 1, mode: 'serial-reference-comparison',
    candidateCommit, candidateImage, reference: admitted.reference, baselineReportSHA256: hash(read(baselinePath)), comparison: true })
  return 'serial-reference-comparison'
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [candidateCommit, candidateImage, evidenceDirectory, ...extra] = process.argv.slice(2)
    if (!evidenceDirectory || extra.length) fail('usage: run_performance_comparison.mjs CANDIDATE_SHA IMMUTABLE_IMAGE EVIDENCE_DIRECTORY')
    console.log(runProductionPerformanceComparison({ candidateCommit, candidateImage, evidenceDirectory }))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
