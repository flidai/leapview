import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve, join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { checkPerformanceReferenceEvidence, verifyTrustedPerformanceReference } from './performance_baseline_review.mjs'

const reference = {
  commit: 'a'.repeat(40),
  qualificationRun: 'https://github.com/flidai/leapview/actions/runs/12345',
}
const successfulRun = {
  head_sha: reference.commit,
  status: 'completed',
  conclusion: 'success',
  event: 'push',
  head_branch: 'main',
  path: '.github/workflows/artifacts.yml',
}
const successfulQualification = {
  name: 'Qualify production image',
  status: 'completed',
  conclusion: 'success',
  head_sha: reference.commit,
}

test('CI evidence checking runs without a PR event or approval context', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-performance-evidence-'))
  try {
    const env = { ...process.env }
    for (const key of ['GITHUB_EVENT_PATH', 'GITHUB_REF', 'GITHUB_SHA', 'GITHUB_REPOSITORY', 'GH_TOKEN']) delete env[key]
    const result = spawnSync(process.execPath, [resolve('scripts/performance_baseline_review.mjs')], {
      cwd: root, env, encoding: 'utf8',
    })
    assert.equal(result.status, 0, result.stderr)
    assert.match(result.stdout, /No accepted performance reference/)
  } finally { rmSync(root, { recursive: true, force: true }) }
})

function referenceApi({ run = successfulRun, jobs = [successfulQualification], totalCount = jobs.length, omitTotalCount = false } = {}) {
  return (path) => {
    if (path.includes('/jobs?')) return [omitTotalCount ? { jobs } : { total_count: totalCount, jobs }]
    return [run]
  }
}

test('malformed accepted references still fail without review metadata', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-performance-evidence-'))
  try {
    mkdirSync(join(root, '.quality'))
    for (const contents of ['{invalid', '[]', 'null']) {
      writeFileSync(join(root, '.quality/performance-reference.json'), contents)
      assert.throws(() => checkPerformanceReferenceEvidence(root), /malformed/)
    }
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('a reference without immutable artifact evidence is rejected before any API query', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-performance-evidence-'))
  try {
    mkdirSync(join(root, '.quality'))
    writeFileSync(join(root, '.quality/performance-reference.json'), JSON.stringify(reference))
    let queried = false
    assert.throws(() => checkPerformanceReferenceEvidence(root, {
      api: () => { queried = true; throw Error('unexpected API query') },
    }), /artifact ID\/digest/)
    assert.equal(queried, false)
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('trusted performance reference requires the upstream run, matching commit, and successful qualification job', () => {
  const calls = []
  const api = (path) => {
    calls.push(path)
    return referenceApi()(path)
  }
  assert.match(verifyTrustedPerformanceReference(reference, api), /flidai\/leapview run 12345/)
  assert.deepEqual(calls, [
    'repos/flidai/leapview/actions/runs/12345',
    'repos/flidai/leapview/actions/runs/12345/jobs?per_page=100',
  ])
})

test('overall run success does not bless a skipped qualification job', () => {
  const skipped = { ...successfulQualification, conclusion: 'skipped' }
  assert.throws(
    () => verifyTrustedPerformanceReference(reference, referenceApi({ jobs: [skipped] })),
    /no completed successful.*Qualify production image.*skipped, failed/,
  )
})

test('trusted performance reference requires the main artifacts push and matching job SHA', () => {
  for (const [field, value] of [['event', 'workflow_dispatch'], ['head_branch', 'release'], ['path', '.github/workflows/ci.yml']]) {
    assert.throws(
      () => verifyTrustedPerformanceReference(reference, referenceApi({ run: { ...successfulRun, [field]: value } })),
      /not the successful main-branch.*artifacts\.yml push/,
      field,
    )
  }
  assert.throws(
    () => verifyTrustedPerformanceReference(reference, referenceApi({ jobs: [{ ...successfulQualification, head_sha: 'b'.repeat(40) }] })),
    /no completed successful.*at reference commit/,
  )
})

test('trusted performance reference fails closed on mismatched or incomplete GitHub data', () => {
  assert.throws(
    () => verifyTrustedPerformanceReference({ ...reference, qualificationRun: 'https://github.com/example/repo/actions/runs/12345' }, referenceApi()),
    /exact flidai\/leapview Actions run URL/,
  )
  assert.throws(
    () => verifyTrustedPerformanceReference(reference, referenceApi({ run: { ...successfulRun, head_sha: 'b'.repeat(40) } })),
    /points at .* not reference commit/,
  )
  assert.throws(
    () => verifyTrustedPerformanceReference(reference, referenceApi({ omitTotalCount: true })),
    /total_count is required/,
  )
})
