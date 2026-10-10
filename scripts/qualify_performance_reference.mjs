import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { verifyTrustedPerformanceReference } from './performance_baseline_review.mjs'

const maximumEvidenceBytes = 20 * 1024 * 1024
const files = ['performance-report.json', 'performance-identity.json']
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex')
const fail = (message) => { throw new Error(`Performance reference evidence: ${message}`) }
const immutableImage = (image) => /^ghcr\.io\/flidai\/leapview@sha256:[0-9a-f]{64}$/.test(image ?? '')
const hash = (value) => /^[0-9a-f]{64}$/.test(value ?? '')
const object = (value) => value && typeof value === 'object' && !Array.isArray(value)

function github(path) {
  return JSON.parse(execFileSync('gh', ['api', '--paginate', '--slurp', path], { encoding: 'utf8', maxBuffer: maximumEvidenceBytes }))
}
function one(payload) {
  if (!Array.isArray(payload) || payload.length !== 1 || !object(payload[0])) fail('artifact API response must contain exactly one object')
  return payload[0]
}
function download(id) {
  return execFileSync('gh', ['api', `repos/flidai/leapview/actions/artifacts/${id}/zip`], { maxBuffer: maximumEvidenceBytes })
}
function readArchive(bytes) {
  const directory = mkdtempSync(join(tmpdir(), 'leapview-performance-reference-'))
  try {
    const path = join(directory, 'evidence.zip')
    writeFileSync(path, bytes, { mode: 0o600 })
    const entries = execFileSync('unzip', ['-Z1', path], { encoding: 'utf8', maxBuffer: 4096 }).trim().split('\n').sort()
    if (JSON.stringify(entries) !== JSON.stringify([...files].sort())) fail('archive must contain exactly the two bounded performance evidence files')
    return Object.fromEntries(files.map((name) => [name, execFileSync('unzip', ['-p', path, name], { maxBuffer: maximumEvidenceBytes })]))
  } finally { rmSync(directory, { recursive: true, force: true }) }
}

// Run/job success alone does not identify report bytes. Bind the independently
// reviewed reference to GitHub's immutable artifact digest and its actual files.
// This is evidence admission, not promotion or a performance comparison.
export function verifyPerformanceReferenceEvidence(reference, dependencies = {}) {
  const { api = github, download: getArchive = download, readArchive: decode = readArchive, candidateCommit, candidateImage } = dependencies
  if (!object(reference) || reference.schemaVersion !== 1 || !Number.isSafeInteger(reference.artifactId) || reference.artifactId <= 0 ||
      !Number.isSafeInteger(reference.runAttempt) || reference.runAttempt <= 0 ||
      !hash(reference.artifactSHA256) || !hash(reference.reportSHA256) || !immutableImage(reference.image)) fail('reference requires schemaVersion 1, artifact ID/digest, report digest and an immutable image')
  if (candidateCommit === reference.commit) fail('candidate cannot use its own source as the accepted reference')
  if (candidateImage === reference.image) fail('candidate cannot use its own image as the accepted reference')
  verifyTrustedPerformanceReference(reference, api)
  const runId = Number(reference.qualificationRun.split('/').at(-1))
  const artifact = one(api(`repos/flidai/leapview/actions/artifacts/${reference.artifactId}`))
  if (artifact.id !== reference.artifactId || artifact.expired !== false || !Number.isSafeInteger(artifact.size_in_bytes) ||
      artifact.size_in_bytes <= 0 || artifact.size_in_bytes > maximumEvidenceBytes ||
      artifact.digest !== `sha256:${reference.artifactSHA256}` || artifact.workflow_run?.id !== runId ||
      artifact.workflow_run?.head_sha !== reference.commit || artifact.name !== `performance-evidence-${runId}-${reference.runAttempt}`) fail('artifact identity, run attempt, source, digest, size or retention does not match the reference')
  const bytes = getArchive(reference.artifactId)
  if (!Buffer.isBuffer(bytes) || bytes.length !== artifact.size_in_bytes || sha256(bytes) !== reference.artifactSHA256) fail('downloaded archive digest or size does not match the upstream artifact')
  const evidence = decode(bytes)
  const reportBytes = evidence['performance-report.json']
  if (!Buffer.isBuffer(reportBytes) || sha256(reportBytes) !== reference.reportSHA256) fail('retained report digest does not match the reference')
  let report, identity
  try { report = JSON.parse(reportBytes); identity = JSON.parse(evidence['performance-identity.json']) } catch { fail('retained report or identity is malformed JSON') }
  if (!object(identity) || identity.sourceRevision !== reference.commit || identity.image !== reference.image) fail('retained identity does not match the reference source and image')
  if (!object(report) || report.schemaVersion !== 1 || report.image !== reference.image || report.result !== 'success' ||
      !object(report.assertions) || report.assertions.environment !== true || report.assertions.absoluteBudgets !== true ||
      report.assertions.errorFree !== true || !Array.isArray(report.failures) || report.failures.length !== 0) fail('retained report must be a finalized successful report for the reference image')
  return { reportBytes, reportSHA256: sha256(reportBytes), reference }
}

export function proposePerformanceReference(artifactId, dependencies = {}) {
  const api = dependencies.api ?? github
  const artifact = one(api(`repos/flidai/leapview/actions/artifacts/${artifactId}`))
  const bytes = (dependencies.download ?? download)(artifactId)
  const evidence = (dependencies.readArchive ?? readArchive)(bytes)
  const report = JSON.parse(evidence['performance-report.json'])
  const artifactAttempt = /^performance-evidence-[1-9][0-9]*-([1-9][0-9]*)$/.exec(artifact.name ?? '')
  const proposal = { schemaVersion: 1, commit: artifact.workflow_run?.head_sha, image: report.image,
    qualificationRun: `https://github.com/flidai/leapview/actions/runs/${artifact.workflow_run?.id}`,
    runAttempt: Number(artifactAttempt?.[1]),
    artifactId, artifactSHA256: sha256(bytes), reportSHA256: sha256(evidence['performance-report.json']) }
  verifyPerformanceReferenceEvidence(proposal, { ...dependencies, api, download: () => bytes, readArchive: () => evidence })
  return proposal
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [mode, input, output, candidateCommit, candidateImage] = process.argv.slice(2)
    if (mode === 'propose') {
      if (!output || resolve(output) === resolve('.quality/performance-reference.json')) fail('proposal output must be separate from the accepted reference')
      const proposal = proposePerformanceReference(Number(input))
      writeFileSync(output, `${JSON.stringify(proposal, null, 2)}\n`, { flag: 'wx', mode: 0o600 })
      console.log('Proposal retained. Acceptance requires an independently reviewed PR; this command cannot promote a reference.')
    } else if (mode === 'check') {
      if (!output || !/^[0-9a-f]{40}$/.test(candidateCommit ?? '') || !immutableImage(candidateImage)) fail('check requires reference, output directory, candidate commit and immutable candidate image')
      const admitted = verifyPerformanceReferenceEvidence(JSON.parse(readFileSync(input)), { candidateCommit, candidateImage })
      mkdirSync(output, { recursive: true, mode: 0o700 })
      writeFileSync(join(output, 'performance-report.json'), admitted.reportBytes, { flag: 'wx', mode: 0o600 })
      writeFileSync(join(output, 'reference-admission.json'), `${JSON.stringify({ schemaVersion: 1, reference: admitted.reference, candidateCommit, candidateImage }, null, 2)}\n`, { flag: 'wx', mode: 0o600 })
      console.log('Retained reference bytes admitted; no performance comparison or promotion has been performed.')
    } else fail('usage: propose <artifact-id> <proposal.json> OR check <reference.json> <output-dir> <candidate-commit> <candidate-image>')
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
