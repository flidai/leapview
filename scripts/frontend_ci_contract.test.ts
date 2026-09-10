import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

const shards = ['core', 'reports', 'chat', 'data', 'site']
const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks

test('local frontend validation runs every bounded shard without suppressing failure', () => {
  expect(tasks['ci:lane:frontend'].cmds).toEqual(shards.map((shard) => ({
    task: 'ci:lane:frontend:shard', vars: { SHARD: shard },
  })))
  expect(tasks['ci:lane:frontend:shard'].cmds).toEqual([
    'node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:test:frontend:{{.SHARD}}',
  ])
  expect(tasks['ci:lane:frontend:local'].cmds).toEqual([{ task: 'ci:lane:frontend' }])
  expect(tasks['ci:lane:frontend'].ignore_error).toBeUndefined()
  expect(tasks['ci:lane:frontend:shard'].ignore_error).toBeUndefined()
})

test('frontend core checks prepared bundle evidence without rebuilding production assets', () => {
  const commands = tasks['ci:test:frontend:core'].cmds
  expect(commands).toContainEqual({ task: 'quality:frontend-bundle:check' })
  expect(commands).not.toContain('bun scripts/build_assets.ts')
  expect(commands).not.toContain('bun run build')
})

test('frontend preparation generates Lucide modules before building production assets', () => {
  const commands = tasks['ci:prepare:frontend'].cmds
  const lucide = commands.findIndex((command: unknown) => JSON.stringify(command) === JSON.stringify({ task: 'lucide-icons:generate' }))
  const build = commands.findIndex((command: unknown) => JSON.stringify(command) === JSON.stringify({ task: 'build' }))
  expect(lucide).toBeGreaterThanOrEqual(0)
  expect(lucide).toBeLessThan(build)
})

test('performance review bootstrap never executes an untrusted candidate guard', () => {
  const config = parse(readFileSync('.github/workflows/ci.yml', 'utf8'))
  const gate = config.jobs['ci-gate']
  expect(gate.permissions).toEqual({
    actions: 'read',
    contents: 'read',
    'pull-requests': 'read',
  })
  const review = gate.steps.find((step: any) => step.name === 'Verify independent review of changed performance governance')
  expect(review?.run).toContain('git show "$base_revision:$guard"')
  expect(review?.run).toContain('bootstrap must be established on main before this PR can pass')
  expect(review?.run).not.toContain('using candidate bootstrap guard')
})

test('main production images cannot silently skip qualification', () => {
  const config = parse(readFileSync('.github/workflows/artifacts.yml', 'utf8'))
  const qualification = config.jobs['qualify-production-image']
  expect(qualification.needs).toBe('build-production-image')
  expect(qualification.if).toBe("${{ always() && needs.build-production-image.result == 'success' }}")
})

for (const workflow of ['ci', 'merge-validation', 'nightly']) {
  test(`${workflow} requires all isolated frontend shards with the existing watchdog bound`, () => {
    const config = parse(readFileSync(`.github/workflows/${workflow}.yml`, 'utf8'))
    const job = config.jobs['frontend-validation']
    if (workflow === 'ci') {
 expect(job.strategy.matrix).toBe("${{ fromJSON(needs.prepare.outputs.frontend_matrix) }}")
 expect(job.needs).toEqual(["prepare"])
 expect(job.if).toBe("needs.prepare.outputs.frontend_validation == 'true'")
 } else { expect(job.strategy.matrix.shard).toEqual(shards) }
    expect(job.strategy['fail-fast']).toBe(false)
    expect(job['continue-on-error']).toBeUndefined()
    expect(job.steps.find((step: any) => step.name === 'Run frontend validation').run)
      .toBe('task ci:lane:frontend:shard SHARD=${{ matrix.shard }}')
    const gate = config.jobs['ci-gate']
    expect(gate.needs).toContain('frontend-validation')
    expect(gate.steps.some((step: any) => step.env?.FRONTEND_RESULT === "${{ needs.frontend-validation.result }}"))
      .toBe(true)
  })
}

for (const workflow of ['ci', 'merge-validation']) {
  test(`${workflow} retains the core-shard bundle evidence on every outcome`, () => {
    const config = parse(readFileSync(`.github/workflows/${workflow}.yml`, 'utf8'))
    const job = config.jobs['frontend-validation']
    const upload = job.steps.find((step: any) => step.name === 'Upload frontend bundle evidence')
    expect(upload).toBeDefined()
    expect(upload.if).toBe("${{ always() && matrix.shard == 'core' }}")
    expect(upload.uses).toBe('actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a')
    expect(upload.with).toEqual({
      name: 'frontend-bundle-evidence-${{ github.run_id }}-${{ github.run_attempt }}',
      path: '.tmp/frontend-bundle-evidence.json',
      'include-hidden-files': true,
      'retention-days': 90,
      'if-no-files-found': 'error',
    })
  })
}
