import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

export function validateReference(value) {
  if (!value || value.schemaVersion !== 1 || value.workload !== 'olist-evaluation' ||
      value.architecture !== 'amd64' || !/^[0-9a-f]{40}$/.test(value.commit ?? '') ||
      !/^ghcr\.io\/flidai\/leapview@sha256:[0-9a-f]{64}$/.test(value.image ?? '') ||
      !/^https:\/\/github\.com\/flidai\/leapview\/actions\/runs\/[1-9][0-9]*$/.test(value.qualificationRun ?? '')) {
    throw new Error('Missing or malformed reviewed performance reference; a mutable tag or candidate-generated baseline is not accepted.')
  }
  return value
}

export function validateReferenceReport(report, reference) {
  if (report.result !== 'success' || report.commit !== reference.commit ||
      report.architecture !== reference.architecture || report.comparison?.mode !== 'bootstrap' ||
      report.assertions?.evidenceIdentity !== true || report.assertions?.absoluteBudgets !== true ||
      report.assertions?.errorFree !== true || !report.image?.endsWith(reference.image.split('@')[1])) {
    throw new Error('Reference measurement failed or its runtime identity disagrees with the reviewed immutable reference.')
  }
}

export function validateCandidateReport(report, candidate, reference) {
  // The image qualifier rehosts the same digest in its disposable registry.
  if (report.result !== 'success' || report.comparison?.mode !== 'compare' ||
      report.comparison?.baselineCommit !== reference.commit ||
      report.assertions?.evidenceIdentity !== true || report.assertions?.comparisonTolerance !== true ||
      report.assertions?.absoluteBudgets !== true || report.assertions?.errorFree !== true ||
      !report.image?.endsWith(`@${candidate.split('@')[1]}`)) {
    throw new Error('Candidate qualification did not produce successful compatible relative performance evidence.')
  }
}

function run(command, arguments_, environment = {}) {
  return new Promise((accept, reject) => {
    const child = spawn(command, arguments_, { stdio: 'inherit', env: { ...process.env, ...environment } })
    child.once('error', reject)
    child.once('exit', (code, signal) => code === 0 ? accept() : reject(new Error(`${command} failed (${signal ?? code}); retained performance evidence must be investigated.`)))
  })
}

export async function qualifyPair(candidate, options = {}) {
  const root = resolve(options.root ?? '.')
  const runsRoot = resolve(root, '.tmp/qualification/performance-pair')
  await mkdir(runsRoot, { recursive: true })
  const evidenceRoot = await mkdtemp(resolve(runsRoot, 'run-'))
  const referenceDir = resolve(evidenceRoot, 'reference')
  const candidateDir = resolve(evidenceRoot, 'candidate')
  const baselinePath = resolve(referenceDir, 'performance-report.json')
  const executable = resolve(evidenceRoot, 'leapviewctl')
  console.log(`Performance pair evidence: ${evidenceRoot}`)
  const identityPath = resolve(evidenceRoot, 'pair-identity.json')
  const identity = { schemaVersion: 1, startedAt: new Date().toISOString(), candidate: candidate ?? null, result: 'inconclusive',
    method: 'Sequential fixed-reference and candidate measurement on the same runner, each with independent disposable services.' }
  const saveIdentity = () => writeFile(identityPath, JSON.stringify(identity, null, 2) + '\n')
  await saveIdentity()
  try {
    if (!/^ghcr\.io\/flidai\/leapview@sha256:[0-9a-f]{64}$/.test(candidate ?? '')) {
      throw new Error('Usage: node scripts/qualify_performance_pair.mjs <immutable GHCR candidate image>')
    }
    const reference = validateReference(JSON.parse(await readFile(resolve(root, '.quality/performance-reference.json'), 'utf8')))
    identity.reference = reference
    await saveIdentity()
    if (process.platform !== 'linux' || process.arch !== 'x64') throw new Error('This reference is qualified only on Linux amd64; qualify and review a separate architecture reference.')
    // This executable orchestrates the test; the measured image supplies its own
    // mandatory revision identity. Disable Go's worktree-sensitive auto stamping.
    await run('go', ['build', '-buildvcs=false', '-o', executable, './cmd/leapviewctl'])
    const environment = { LEAPVIEWCTL_ROOT: resolve(root, 'deploy/compose'), QUALIFICATION_PERFORMANCE_BASELINE: '' }
    // Both runs use the same harness, toolchain and hardware. The reference image
    // is fixed by a reviewed file, never selected from candidate measurements.
    await run(executable, ['qualify', 'image', '--image', reference.image, '--require-immutable', '--evidence-dir', referenceDir], {
      ...environment, QUALIFICATION_PERFORMANCE_MODE: 'bootstrap',
    })
    const referenceReport = JSON.parse(await readFile(baselinePath, 'utf8'))
    validateReferenceReport(referenceReport, reference)
    await run(executable, ['qualify', 'image', '--image', candidate, '--require-immutable', '--evidence-dir', candidateDir], {
      ...environment, QUALIFICATION_PERFORMANCE_MODE: 'compare', QUALIFICATION_PERFORMANCE_BASELINE: baselinePath,
    })
    const candidateReport = JSON.parse(await readFile(resolve(candidateDir, 'performance-report.json'), 'utf8'))
    validateCandidateReport(candidateReport, candidate, reference)
    identity.result = 'success'
  } catch (error) {
    identity.result = 'failure'
    identity.error = error.message
    throw error
  } finally {
    identity.finishedAt = new Date().toISOString()
    await saveIdentity()
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  qualifyPair(process.argv[2]).catch((error) => { console.error(error.message); process.exitCode = 1 })
}
