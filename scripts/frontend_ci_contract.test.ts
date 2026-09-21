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

test('hosted demo generates build-only packages before publishing', () => {
  const config = parse(readFileSync('.github/workflows/demo-deploy.yml', 'utf8'))
  const publicationSource = config.jobs['publication-source']
  const resolver = publicationSource.steps.find((step: any) => step.id === 'resolve')
  expect(publicationSource.outputs).toEqual({
    publish: '${{ steps.resolve.outputs.publish }}',
    revision: '${{ steps.resolve.outputs.revision }}',
  })
  expect(resolver.run).toContain('git show HEAD^:scripts/rollout_demo_runtime.py')

  const deploy = config.jobs.deploy
  expect(deploy.needs).toBe('publication-source')
  expect(deploy.if).toBe("needs.publication-source.outputs.publish == 'true'")
  expect(deploy.env.SOURCE_REVISION).toBe('${{ needs.publication-source.outputs.revision }}')
  const steps = deploy.steps
  const setupIndex = steps.findIndex((step: any) => step.uses === './.github/actions/setup-ci')
  const generateIndex = steps.findIndex((step: any) => step.run === 'task generate')
  const publishIndex = steps.findIndex((step: any) => step.run === './scripts/deploy_demo.sh')

  expect(setupIndex).toBeGreaterThan(-1)
  expect(generateIndex).toBeGreaterThan(setupIndex)
  expect(publishIndex).toBeGreaterThan(generateIndex)
})

test('production image qualification generates SQL packages before compiling the qualifier', () => {
  const commands = tasks['image:qualify:production'].cmds
  expect(commands.slice(0, 2)).toEqual([
    { task: 'db:generate' },
    { task: 'api:generate' },
  ])
  expect(commands.at(-1)).toContain('go run ./cmd/leapviewctl qualify image')
})
