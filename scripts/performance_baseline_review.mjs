import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

// Changes to the evidence, its producer, or its enforcement need the same
// independent review as changing a ceiling. A JSON `approved: true` is not an
// approval; GitHub's review of the current PR head is the authority.
export function requiresPerformanceReview(paths) {
  return paths.some((path) =>
    /^\.quality\/(frontend-bundle|performance-)/.test(path) ||
    /^deploy\/compose\/qualification\/(performance|browser\.mjs$|package(?:-lock)?\.json$)/.test(path) ||
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

export function checkPerformanceBaselineReview(event, repository, api = github) {
  const pull = event.pull_request
  if (!pull) throw new Error('Performance review requires a pull-request event; dispatch CI on an open PR for review evidence.')
  const [current] = api(`repos/${repository}/pulls/${pull.number}`)
  if (current.head.sha !== pull.head.sha) throw new Error('Pull-request head changed; rerun CI for the current commit.')
  const files = api(`repos/${repository}/pulls/${pull.number}/files?per_page=100`).flat()
  if (current.changed_files > files.length) throw new Error('GitHub returned an incomplete changed-file list; performance review is inconclusive.')
  const paths = files.flatMap((file) => [file.filename, file.previous_filename].filter(Boolean))
  if (!requiresPerformanceReview(paths)) return 'No performance baseline or gate changes.'
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
