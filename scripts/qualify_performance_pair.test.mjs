import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { qualifyPair, validateReference, validateReferenceReport, validateCandidateReport } from './qualify_performance_pair.mjs'

const reference = JSON.parse(readFileSync('.quality/performance-reference.json', 'utf8'))
test('reference requires an immutable image and attributable main qualification', () => {
  assert.equal(validateReference(reference), reference)
  for (const invalid of [{}, { ...reference, image: 'ghcr.io/flidai/leapview:main' }, { ...reference, commit: '' }, { ...reference, qualificationRun: '' }]) {
    assert.throws(() => validateReference(invalid), /reviewed performance reference/)
  }
})

test('malformed reference retains an explicit failing run identity before any process runs', async () => {
  const root = await mkdtemp(join(tmpdir(), 'performance-pair-test-'))
  try {
    await mkdir(join(root, '.quality'))
    await writeFile(join(root, '.quality/performance-reference.json'), '{malformed')
    await assert.rejects(qualifyPair(reference.image, { root }), SyntaxError)
    const runs = join(root, '.tmp/qualification/performance-pair')
    const entries = await readdir(runs)
    assert.equal(entries.length, 1)
    const identity = JSON.parse(await readFile(join(runs, entries[0], 'pair-identity.json'), 'utf8'))
    assert.equal(identity.result, 'failure')
    assert.equal(identity.candidate, reference.image)
    assert.ok(identity.error)
    assert.ok(identity.finishedAt)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

test('candidate cannot masquerade as the reference and failed reference runs cannot be baselines', () => {
  const report = { result: 'success', commit: reference.commit, image: reference.image, architecture: 'amd64', comparison: { mode: 'bootstrap' }, assertions: { evidenceIdentity: true, absoluteBudgets: true, errorFree: true } }
  assert.doesNotThrow(() => validateReferenceReport(report, reference))
  for (const invalid of [{ ...report, commit: 'candidate' }, { ...report, result: 'failure' }, { ...report, image: 'other' }, { ...report, assertions: {} }]) {
    assert.throws(() => validateReferenceReport(invalid, reference), /Reference measurement failed/)
  }
})

test('candidate requires relative evidence and the exact digest even after registry rehosting', () => {
  const report = { result: 'success', image: `127.0.0.1:1234/leapview@${reference.image.split('@')[1]}`,
    comparison: { mode: 'compare', baselineCommit: reference.commit },
    assertions: { evidenceIdentity: true, absoluteBudgets: true, comparisonTolerance: true, errorFree: true } }
  assert.doesNotThrow(() => validateCandidateReport(report, reference.image, reference))
  for (const invalid of [{ ...report, assertions: {} }, { ...report, image: `${report.image}0` },
    { ...report, comparison: { mode: 'bootstrap', baselineCommit: reference.commit } }, { ...report, result: 'failure' }]) {
    assert.throws(() => validateCandidateReport(invalid, reference.image, reference), /relative performance evidence/)
  }
})
