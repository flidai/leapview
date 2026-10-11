import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { verifyPerformanceReferenceEvidence } from './qualify_performance_reference.mjs'

// GitHub branch protection owns PR approval. CI validates performance evidence
// without querying reviews or requiring approval of each new commit.
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
  if (reference.runAttempt !== undefined && (!Number.isSafeInteger(reference.runAttempt) || reference.runAttempt <= 0 ||
      run.run_attempt !== reference.runAttempt)) {
    throw trustedReferenceError('reference attempt must match the latest successful run attempt; a previous attempt cannot inherit later qualification success.')
  }
  if (run.event !== 'push' || run.head_branch !== 'main' || run.path !== '.github/workflows/artifacts.yml') {
    throw trustedReferenceError(`qualification run ${runId} is not the successful main-branch .github/workflows/artifacts.yml push required for a trusted reference.`)
  }

  const jobsPath = reference.runAttempt === undefined
    ? `repos/flidai/leapview/actions/runs/${runId}/jobs?per_page=100`
    : `repos/flidai/leapview/actions/runs/${runId}/attempts/${reference.runAttempt}/jobs?per_page=100`
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

export function checkPerformanceReferenceEvidence(root = process.cwd(), dependencies = {}) {
  const reference = readPerformanceReference(root)
  if (!reference) return 'No accepted performance reference; qualification remains absolute-only.'
  verifyPerformanceReferenceEvidence(reference, dependencies)
  return `Trusted performance reference evidence verified at ${reference.commit}.`
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    console.log(checkPerformanceReferenceEvidence())
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
