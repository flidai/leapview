import { test } from 'node:test'
import assert from 'node:assert/strict'
import { requiresPerformanceReview, hasIndependentApproval, checkPerformanceBaselineReview } from './performance_baseline_review.mjs'

const pull = { number: 42, head: { sha: 'candidate' }, user: { login: 'author' } }
const approval = { id: 1, state: 'APPROVED', commit_id: 'candidate', user: { login: 'reviewer', type: 'User' }, author_association: 'MEMBER' }

test('changing evidence or enforcement requires review; unrelated work does not', () => {
  for (const path of ['.quality/frontend-bundle-budget.json', '.quality/performance-reference.json', 'deploy/compose/qualification/performance.mjs', 'scripts/performance_baseline_review.mjs', '.github/workflows/ci.yml', 'Taskfile.yml', 'internal/app/cli/composectl/qualification_installed.go', '.github/actions/setup-ci/action.yml']) {
    assert.equal(requiresPerformanceReview([path]), true, path)
  }
  assert.equal(requiresPerformanceReview(['web/components/shared/button.ts']), false)
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

test('renaming a protected policy cannot evade review and missing reviews fail', () => {
  const api = (path) => path.includes('/files')
    ? [[{ filename: 'renamed.json', previous_filename: '.quality/performance-reference.json' }]]
    : path.includes('/reviews') ? [[]] : [pull]
  assert.throws(() => checkPerformanceBaselineReview({ pull_request: pull }, 'owner/repo', api), /independent repository collaborator/)
  assert.throws(() => checkPerformanceBaselineReview({}, 'owner/repo', api), /pull-request event/)
})
