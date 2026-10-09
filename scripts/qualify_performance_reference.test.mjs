import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { verifyPerformanceReferenceEvidence, proposePerformanceReference } from './qualify_performance_reference.mjs'

const sha = (bytes) => createHash('sha256').update(bytes).digest('hex')
const image = `ghcr.io/flidai/leapview@sha256:${'b'.repeat(64)}`
const archive = Buffer.from('a fixed archive containing the two bounded reports')
const report = Buffer.from(JSON.stringify({ schemaVersion: 1, image, result: 'success', assertions: { environment: true, absoluteBudgets: true, errorFree: true }, failures: [] }))
const identity = Buffer.from(JSON.stringify({ sourceRevision: 'a'.repeat(40), image }))
const reference = { schemaVersion: 1, commit: 'a'.repeat(40), image, qualificationRun: 'https://github.com/flidai/leapview/actions/runs/12345', runAttempt: 1, artifactId: 42, artifactSHA256: sha(archive), reportSHA256: sha(report) }
const run = { head_sha: reference.commit, run_attempt: 1, status: 'completed', conclusion: 'success', event: 'push', head_branch: 'main', path: '.github/workflows/artifacts.yml' }
const artifact = { id: 42, name: 'performance-evidence-12345-1', expired: false, size_in_bytes: archive.length, digest: `sha256:${sha(archive)}`, workflow_run: { id: 12345, head_sha: reference.commit } }
function dependencies(overrides = {}) {
  return {
    api: (path) => path.includes('/artifacts/') ? [overrides.artifact ?? artifact] : path.includes('/jobs?') ? [{ total_count: 1, jobs: [{ name: 'Qualify production image', status: 'completed', conclusion: 'success', head_sha: reference.commit }] }] : [overrides.run ?? run],
    download: () => overrides.archive ?? archive,
    readArchive: () => ({ 'performance-report.json': overrides.report ?? report, 'performance-identity.json': overrides.identity ?? identity }),
  }
}
test('admission binds a successful upstream run to retained archive, report, source and immutable image', () => {
  const admitted = verifyPerformanceReferenceEvidence(reference, dependencies())
  assert.equal(admitted.reportSHA256, reference.reportSHA256)
  assert.deepEqual(admitted.reportBytes, report)
})
test('substituted archive or report bytes fail even when the upstream job passed', () => {
  assert.throws(() => verifyPerformanceReferenceEvidence(reference, dependencies({ archive: Buffer.from('replacement') })), /archive digest/)
  assert.throws(() => verifyPerformanceReferenceEvidence(reference, dependencies({ report: Buffer.from('{}') })), /report digest/)
})
test('expired, wrong-run and incorrectly sourced artifacts cannot establish a reference', () => {
  for (const changed of [ { expired: true }, { workflow_run: { id: 678, head_sha: reference.commit } }, { workflow_run: { id: 12345, head_sha: 'c'.repeat(40) } }, { digest: `sha256:${'f'.repeat(64)}` } ]) {
    assert.throws(() => verifyPerformanceReferenceEvidence(reference, dependencies({ artifact: { ...artifact, ...changed } })), /artifact/)
  }
})
test('identity and finalized report must identify the same immutable image and source', () => {
  for (const changed of [{ image: 'mutable:tag' }, { sourceRevision: 'c'.repeat(40) }]) {
    assert.throws(() => verifyPerformanceReferenceEvidence(reference, dependencies({ identity: Buffer.from(JSON.stringify({ sourceRevision: reference.commit, image, ...changed })) })), /identity/)
  }
  const failed = Buffer.from(JSON.stringify({ ...JSON.parse(report), result: 'failure' }))
  assert.throws(() => verifyPerformanceReferenceEvidence({ ...reference, reportSHA256: sha(failed) }, dependencies({ report: failed })), /finalized successful/)
})
test('candidate self-reference and incomplete reference identity fail before downloading evidence', () => {
  assert.throws(() => verifyPerformanceReferenceEvidence(reference, { ...dependencies(), candidateCommit: reference.commit }), /own source/)
  assert.throws(() => verifyPerformanceReferenceEvidence(reference, { ...dependencies(), candidateImage: image }), /own image/)
  for (const field of ['schemaVersion', 'runAttempt', 'artifactId', 'artifactSHA256', 'reportSHA256', 'image']) {
    const invalid = { ...reference }; delete invalid[field]
    assert.throws(() => verifyPerformanceReferenceEvidence(invalid, dependencies()), /reference/)
  }
})
test('a failed earlier-attempt artifact cannot inherit a later successful retry', () => {
  const latestRun = { ...run, run_attempt: 2 }
  assert.throws(() => verifyPerformanceReferenceEvidence(reference, dependencies({ run: latestRun })), /previous attempt cannot inherit/)
  assert.throws(() => verifyPerformanceReferenceEvidence({ ...reference, runAttempt: 2 }, dependencies({ run: latestRun })), /artifact identity, run attempt/)
  const currentArtifact = { ...artifact, name: 'performance-evidence-12345-2' }
  assert.deepEqual(verifyPerformanceReferenceEvidence({ ...reference, runAttempt: 2 }, dependencies({ run: latestRun, artifact: currentArtifact })).reportBytes, report)
})
test('a retry starting between API calls cannot inherit the previous successful snapshot', () => {
  let snapshots = 0
  const deps = dependencies({ artifact: { ...artifact, name: 'performance-evidence-12345-2' } })
  const otherAPI = deps.api
  deps.api = path => path === 'repos/flidai/leapview/actions/runs/12345'
    ? [{ ...run, run_attempt: ++snapshots === 1 ? 1 : 2, status: snapshots === 1 ? 'completed' : 'in_progress', conclusion: snapshots === 1 ? 'success' : null }]
    : otherAPI(path)
  assert.throws(() => verifyPerformanceReferenceEvidence({ ...reference, runAttempt: 2 }, deps), /attempt/)
  assert.equal(snapshots, 1)
})
test('new references query qualification jobs from their exact admitted attempt', () => {
  const deps = dependencies()
  const otherAPI = deps.api
  const calls = []
  deps.api = path => { calls.push(path); return otherAPI(path) }
  verifyPerformanceReferenceEvidence(reference, deps)
  assert.equal(calls.filter(path => path === 'repos/flidai/leapview/actions/runs/12345').length, 1)
  assert.ok(calls.includes('repos/flidai/leapview/actions/runs/12345/attempts/1/jobs?per_page=100'))
  assert.ok(!calls.includes('repos/flidai/leapview/actions/runs/12345/jobs?per_page=100'))
})
test('the real archive decoder accepts only the two bounded files and rejects traversal or duplicates', () => {
  function zip(names) {
    return execFileSync('python3', ['-c', 'import io,json,sys,zipfile; b=io.BytesIO(); z=zipfile.ZipFile(b,"w"); [(z.writestr(n,bytes.fromhex(v))) for n,v in json.loads(sys.argv[1])]; z.close(); sys.stdout.buffer.write(b.getvalue())', JSON.stringify(names)], { maxBuffer: 1024 * 1024 })
  }
  for (const extra of [null, ['../performance-report.json', '00'], ['performance-report.json', report.toString('hex')]]) {
    const bytes = zip([['performance-report.json', report.toString('hex')], ['performance-identity.json', identity.toString('hex')], ...(extra ? [extra] : [])])
    const current = { ...reference, artifactSHA256: sha(bytes) }
    const deps = dependencies({ archive: bytes, artifact: { ...artifact, size_in_bytes: bytes.length, digest: `sha256:${sha(bytes)}` } })
    delete deps.readArchive
    if (extra) assert.throws(() => verifyPerformanceReferenceEvidence(current, deps), /exactly the two bounded/)
    else assert.deepEqual(verifyPerformanceReferenceEvidence(current, deps).reportBytes, report)
  }
})
test('proposal binds actual upstream bytes without inventing acceptance', () => {
  assert.deepEqual(proposePerformanceReference(42, dependencies()), reference)
})
