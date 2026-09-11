import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

// Changes to the evidence, its producer, or its enforcement need the same
// independent review as changing a ceiling. A JSON `approved: true` is not an
// approval; GitHub's review of the current PR head is the authority.
export function requiresPerformanceReview(paths) {
  return paths.some((path) =>
    /^\.quality\/(frontend-bundle|performance-)/.test(path) ||
    /^deploy\/compose\/qualification\//.test(path) ||
    /^internal\/app\/cli\/composectl\/qualification/.test(path) ||
    /^internal\/(platform\/ci\/|app\/tools\/(ciplan|cireport|ciadapter)\/)/.test(path) ||
    /^scripts\/(frontend_bundle|performance_baseline|qualify_performance)/.test(path) ||
    ['Taskfile.yml', 'Dockerfile', 'package.json', 'bun.lock', 'tsconfig.json',
      'scripts/build_assets.ts', 'scripts/build_maplibre_worker.ts', 'scripts/frontend_ci_contract.test.ts',
      'scripts/generate_lucide_icon_catalog.ts', 'scripts/generate_visualization_validator.ts',
      '.github/workflows/ci.yml', '.github/workflows/artifacts.yml', '.github/workflows/release.yml',
      '.github/workflows/installed-candidate.yml', '.github/workflows/merge-validation.yml',
      '.github/workflows/nightly.yml', '.github/actions/setup-ci/action.yml',
      '.github/actions/oci-admission/action.yml'].includes(path))
}

export function hasIndependentApproval(pull, reviews) {
  const latest = new Map()
  for (const review of [...reviews].sort((a, b) => a.id - b.id)) {
    if (!review.user?.login || review.state === 'COMMENTED' || review.state === 'PENDING') continue
    latest.set(review.user.login, review)
  }
  return [...latest.values()].some((review) =>
    review.state === 'APPROVED' && review.commit_id === pull.head.sha &&
    review.user.login !== pull.user.login && review.user.type === 'User' &&
    ['OWNER', 'MEMBER', 'COLLABORATOR'].includes(review.author_association))
}

function github(path) {
  return JSON.parse(execFileSync('gh', ['api', '--paginate', '--slurp', path], { encoding: 'utf8' }))
}

function trustedReferenceError(message) {
  return new Error(`Trusted performance reference check failed: ${message}`)
}

export function readPerformanceReference(root = process.cwd()) {
  const path = resolve(root, '.quality/performance-reference.json')
  let contents
  try {
    contents = readFileSync(path, 'utf8')
  } catch (error) {
    if (error?.code === 'ENOENT') return null
    throw trustedReferenceError(`could not read ${path}: ${error.message}`)
  }
  try {
    const reference = JSON.parse(contents)
    if (!reference || typeof reference !== 'object' || Array.isArray(reference)) {
      throw new Error('the reference must be a JSON object')
    }
    return reference
  } catch (error) {
    throw trustedReferenceError(`the checked-out .quality/performance-reference.json is malformed: ${error.message}`)
  }
}

function apiPayload(api, path, description) {
  try {
    return api(path)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    throw trustedReferenceError(`GitHub API could not retrieve ${description} (${path}): ${message}`)
  }
}

function apiPages(payload, description) {
  if (payload && typeof payload === 'object' && !Array.isArray(payload)) return [payload]
  if (!Array.isArray(payload) || payload.length === 0) {
    throw trustedReferenceError(`GitHub returned no ${description} data; rerun CI with access to the upstream Actions API.`)
  }
  const pages = payload.every(Array.isArray) ? payload.flat() : payload
  if (pages.length === 0 || pages.some((page) => !page || typeof page !== 'object' || Array.isArray(page))) {
    throw trustedReferenceError(`GitHub returned malformed ${description} data; rerun CI with access to the upstream Actions API.`)
  }
  return pages
}

function oneApiObject(payload, description) {
  const pages = apiPages(payload, description)
  if (pages.length !== 1) {
    throw trustedReferenceError(`GitHub returned an ambiguous ${description} response; rerun CI with access to the upstream Actions API.`)
  }
  return pages[0]
}

function referenceRunId(reference) {
  if (!reference || typeof reference !== 'object' || Array.isArray(reference)) {
    throw trustedReferenceError('the checked-out reference is not a JSON object.')
  }
  if (!/^[0-9a-f]{40}$/.test(reference.commit ?? '')) {
    throw trustedReferenceError('the checked-out reference has no valid 40-character commit; regenerate it from a reviewed immutable reference.')
  }
  const match = /^https:\/\/github\.com\/flidai\/leapview\/actions\/runs\/([1-9][0-9]*)$/.exec(reference.qualificationRun ?? '')
  if (!match) {
    throw trustedReferenceError('qualificationRun must be an exact flidai/leapview Actions run URL; mutable or third-party references are not accepted.')
  }
  return match[1]
}

export function verifyTrustedPerformanceReference(reference, api = github) {
  const runId = referenceRunId(reference)
  const runPath = `repos/flidai/leapview/actions/runs/${runId}`
  const run = oneApiObject(apiPayload(api, runPath, 'the qualification run'), 'qualification run')
  if (typeof run.head_sha !== 'string' || typeof run.status !== 'string' || typeof run.conclusion !== 'string' ||
      typeof run.event !== 'string' || typeof run.head_branch !== 'string' || typeof run.path !== 'string') {
    throw trustedReferenceError(`the qualification run response is incomplete for run ${runId}; head_sha, status, conclusion, event, head_branch, and path are required.`)
  }
  if (run.head_sha !== reference.commit) {
    throw trustedReferenceError(`qualification run ${runId} points at ${run.head_sha}, not reference commit ${reference.commit}.`)
  }
  if (run.status !== 'completed' || run.conclusion !== 'success') {
    throw trustedReferenceError(`qualification run ${runId} is ${run.status}/${run.conclusion}; a completed successful run at the reference commit is required.`)
  }
  if (run.event !== 'push' || run.head_branch !== 'main' || run.path !== '.github/workflows/artifacts.yml') {
    throw trustedReferenceError(`qualification run ${runId} is not the successful main-branch .github/workflows/artifacts.yml push required for a trusted reference.`)
  }

  const jobsPath = `repos/flidai/leapview/actions/runs/${runId}/jobs?per_page=100`
  const pages = apiPages(apiPayload(api, jobsPath, 'qualification jobs'), 'qualification jobs')
  const totalCount = pages[0].total_count
  if (!Number.isInteger(totalCount) || totalCount < 0) {
    throw trustedReferenceError(`the qualification jobs response is incomplete for run ${runId}; total_count is required.`)
  }
  if (pages.some((page) => page.total_count !== totalCount || !Array.isArray(page.jobs))) {
    throw trustedReferenceError(`the qualification jobs response is incomplete for run ${runId}; every page must include the same total_count and a jobs array.`)
  }
  const jobs = pages.flatMap((page) => page.jobs)
  if (jobs.length !== totalCount || jobs.some((job) => !job || typeof job !== 'object' || Array.isArray(job) ||
      typeof job.name !== 'string' || typeof job.status !== 'string' || typeof job.conclusion !== 'string' || typeof job.head_sha !== 'string')) {
    throw trustedReferenceError(`the qualification jobs response is incomplete for run ${runId}; all ${totalCount} jobs must be returned with name, status, conclusion, and head_sha.`)
  }
  if (!jobs.some((job) => job.name === 'Qualify production image' &&
      job.status === 'completed' && job.conclusion === 'success' && job.head_sha === reference.commit)) {
    throw trustedReferenceError(`qualification run ${runId} has no completed successful “Qualify production image” job at reference commit ${reference.commit}; skipped, failed, or differently sourced jobs cannot establish a trusted reference.`)
  }
  return `Trusted performance reference verified from flidai/leapview run ${runId} at ${reference.commit}.`
}

export function checkPerformanceBaselineReview(event, repository, api = github, reference) {
  const pull = event.pull_request
  if (!pull) throw new Error('Performance review requires a pull-request event; dispatch CI on an open PR for review evidence.')
  const [current] = api(`repos/${repository}/pulls/${pull.number}`)
  if (current.head.sha !== pull.head.sha) throw new Error('Pull-request head changed; rerun CI for the current commit.')
  const files = api(`repos/${repository}/pulls/${pull.number}/files?per_page=100`).flat()
  if (current.changed_files > files.length) throw new Error('GitHub returned an incomplete changed-file list; performance review is inconclusive.')
  const paths = files.flatMap((file) => [file.filename, file.previous_filename].filter(Boolean))
  if (!requiresPerformanceReview(paths)) return 'No performance baseline or gate changes.'
  if (reference === undefined) reference = readPerformanceReference()
  if (reference) verifyTrustedPerformanceReference(reference, api)
  const reviews = api(`repos/${repository}/pulls/${pull.number}/reviews?per_page=100`).flat()
  if (!hasIndependentApproval(current, reviews)) {
    throw new Error(`Performance governance changed: an independent repository collaborator must approve PR #${pull.number} at ${pull.head.sha}. Include calibration and regression evidence in the PR, then rerun the failed CI gate job. Editing approval fields or approving an older commit does not satisfy this gate.`)
  }
  return `Performance governance independently reviewed at ${pull.head.sha}.`
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'))
    if (!event.pull_request && process.env.GITHUB_REF === 'refs/heads/main') {
      console.log('Main branch validation; baseline review is enforced on pull requests before delivery.')
      process.exit(0)
    }
    if (!event.pull_request) {
      const pulls = github(`repos/${process.env.GITHUB_REPOSITORY}/commits/${process.env.GITHUB_SHA}/pulls?per_page=100`).flat()
        .filter((pull) => pull.state === 'open' && pull.base.ref === 'main' && pull.head.sha === process.env.GITHUB_SHA)
      if (pulls.length !== 1) throw new Error('Manual CI requires exactly one open PR to main at this commit for performance review.')
      event.pull_request = pulls[0]
    }
    console.log(checkPerformanceBaselineReview(event, process.env.GITHUB_REPOSITORY))
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
