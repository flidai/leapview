import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  requiresPerformanceReview,
  hasIndependentApproval,
  checkPerformanceBaselineReview,
  verifyTrustedPerformanceReference,
} from './performance_baseline_review.mjs'

const pull = { number: 42, head: { sha: 'candidate' }, user: { login: 'author' } }
const approval = { id: 1, state: 'APPROVED', commit_id: 'candidate', user: { login: 'reviewer', type: 'User' }, author_association: 'MEMBER' }
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

function referenceApi({ run = successfulRun, jobs = [successfulQualification], totalCount = jobs.length, omitTotalCount = false } = {}) {
  return (path) => {
    if (path.includes('/jobs?')) return [omitTotalCount ? { jobs } : { total_count: totalCount, jobs }]
    return [run]
  }
}

test('changing evidence or enforcement requires review; unrelated work does not', () => {
  for (const path of ['.quality/frontend-bundle-budget.json', '.quality/performance-reference.json', 'deploy/compose/qualification/performance.mjs', 'scripts/performance_baseline_review.mjs', '.github/workflows/ci.yml', 'Taskfile.yml', 'internal/app/cli/composectl/qualification_installed.go', '.github/actions/setup-ci/action.yml']) {
    assert.equal(requiresPerformanceReview([path]), true, path)
  }
  assert.equal(requiresPerformanceReview(['web/components/shared/button.ts']), false)
})

test('build inputs, benchmark dependencies and every enforcement entry point require review', () => {
  for (const path of [
    'package.json', 'bun.lock', 'tsconfig.json', 'scripts/frontend_ci_contract.test.ts', 'scripts/frontend_bundle_options.ts',
    'scripts/build_maplibre_worker.ts', 'scripts/generate_lucide_icon_catalog.ts',
    'scripts/generate_visualization_validator.ts', 'deploy/compose/qualification/browser.mjs',
    'deploy/compose/qualification/authoring-worker.mjs',
    'deploy/compose/qualification/package.json', 'deploy/compose/qualification/package-lock.json',
    'deploy/compose/qualification/Dockerfile.authoring-client',
    '.github/actions/oci-admission/action.yml', '.github/workflows/merge-validation.yml',
    '.github/workflows/nightly.yml',
    'internal/platform/ci/planner.go', 'internal/platform/ci/pr_plan.go',
    'internal/app/tools/ciplan/main.go', 'internal/app/tools/cireport/main.go',
    'internal/app/tools/ciadapter/adapter.go',
  ]) {
    assert.equal(requiresPerformanceReview([path]), true, path)
  }
})

test('only an independent human collaborator approval of the current head counts', () => {
  assert.equal(hasIndependentApproval(pull, [approval]), true)
  for (const invalid of [
    { ...approval, commit_id: 'old' },
    { ...approval, user: { login: 'author', type: 'User' } },
    { ...approval, user: { login: 'bot', type: 'Bot' } },
    { ...approval, author_association: 'CONTRIBUTOR' },
    { ...approval, state: 'DISMISSED' },
  ]) assert.equal(hasIndependentApproval(pull, [invalid]), false)
  assert.equal(hasIndependentApproval(pull, [approval, { ...approval, id: 2, state: 'CHANGES_REQUESTED' }]), false)
  assert.equal(hasIndependentApproval(pull, [approval, { ...approval, id: 2, state: 'COMMENTED' }]), true)
})

function hiddenMemberApi(review, permission, calls = []) {
  return path => {
    calls.push(path)
    if (path.endsWith('/permission')) {
      if (permission instanceof Error) throw permission
      return permission
    }
    if (path.includes('/reviews')) return [[review]]
    if (path.includes('/files')) return [[{ filename: 'package.json' }]]
    return [pull]
  }
}

const hiddenMemberApproval = { ...approval, author_association: 'CONTRIBUTOR' }
const writerPermission = { permission: 'write', user: { login: 'reviewer', type: 'User' } }

test('Actions hidden membership uses authoritative repository write permission', () => {
  for (const permission of ['write', 'admin']) {
    const calls = []
    const api = hiddenMemberApi(hiddenMemberApproval, [{ ...writerPermission, permission }], calls)
    assert.match(checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', api, null), /independently reviewed/)
    assert.ok(calls.includes('repos/owner/repo/collaborators/reviewer/permission'))
  }
})

test('permission fallback cannot approve stale, self, bot or withdrawn reviews', () => {
  for (const review of [
    { ...hiddenMemberApproval, commit_id: 'old' },
    { ...hiddenMemberApproval, user: { login: 'author', type: 'User' } },
    { ...hiddenMemberApproval, user: { login: 'reviewer', type: 'Bot' } },
    { ...hiddenMemberApproval, state: 'DISMISSED' },
    { ...hiddenMemberApproval, state: 'CHANGES_REQUESTED' },
  ]) {
    const calls = []
    assert.throws(() => checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', hiddenMemberApi(review, [writerPermission], calls), null), /independent repository collaborator/)
    assert.equal(calls.some(path => path.endsWith('/permission')), false)
  }
})

test('permission fallback rejects nonwriters and another principal identity', () => {
  for (const permission of [
    { ...writerPermission, permission: 'read' },
    { ...writerPermission, permission: 'none' },
    { ...writerPermission, permission: 'triage' },
    { ...writerPermission, user: { login: 'other', type: 'User' } },
    { ...writerPermission, user: { login: 'reviewer', type: 'Bot' } },
    { permission: 'write' },
  ]) {
    assert.throws(() => checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', hiddenMemberApi(hiddenMemberApproval, [permission]), null), /independent repository collaborator/)
  }
})

test('permission lookup failures and ambiguous responses fail closed', () => {
  for (const permission of [new Error('API unavailable'), [], [writerPermission, writerPermission]]) {
    assert.throws(() => checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', hiddenMemberApi(hiddenMemberApproval, permission), null), /repository permission.*inconclusive/)
  }
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

test('unrelated pull requests do not query reference qualification history', () => {
  const calls = []
  const api = (path) => {
    calls.push(path)
    if (path.includes('/files')) return [[{ filename: 'web/components/shared/button.ts' }]]
    if (path.includes('/actions/runs/')) throw new Error('reference history should not be queried')
    return [{ ...pull, changed_files: 1 }]
  }
  assert.equal(checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', api, reference), 'No performance baseline or gate changes.')
  assert.deepEqual(calls, ['repos/owner/repo/pulls/42', 'repos/owner/repo/pulls/42/files?per_page=100'])
})

test('renaming a protected policy cannot evade review and missing reviews fail', () => {
  const api = (path) => path.includes('/files')
    ? [[{ filename: 'renamed.json', previous_filename: '.quality/performance-reference.json' }]]
    : path.includes('/reviews') ? [[]] : [pull]
  assert.throws(() => checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', api, null), /independent repository collaborator/)
  assert.throws(() => checkPerformanceBaselineReview({}, 'owner/repo', api), /pull-request event/)
})
