import { expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'
import { parse } from 'yaml'

const workflow = (name: string) => parse(readFileSync(`.github/workflows/${name}.yml`, 'utf8'))

test('historical source preflight rejects shallow history before image work', () => {
  const steps = workflow('sqlc-image-sample').jobs.sample.steps
  const checkout = steps.find((step: any) => step.uses?.startsWith('actions/checkout@'))
  const preflight = steps.find((step: any) => step.name === 'Verify historical qualification source')
  expect(checkout.with['fetch-depth']).toBe(0)
  expect(checkout.with.ref).toBe('${{ github.sha }}')
  expect(checkout.with['persist-credentials']).toBe(false)
  expect(preflight).toBeDefined()
  expect(preflight.if).toBeUndefined()
  expect(preflight['continue-on-error']).toBeUndefined()
  expect(steps.indexOf(preflight)).toBeLessThan(steps.findIndex((step: any) => step.id === 'prepare'))

  const directory = mkdtempSync(join(tmpdir(), 'sqlc-history-'))
  const repo = join(directory, 'full')
  const shallow = join(directory, 'shallow')
  const git = (...args: string[]) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim()
  const pinPath = 'internal/app/cli/composectl/qualification_historical_transition.go'
  const pin = (revision: string) => `package composectl\nconst qualificationHistoricalPredecessorRevision = "${revision}"\n`
  try {
    mkdirSync(join(repo, 'dashboards/experiments/cfo-demo'), { recursive: true })
    mkdirSync(join(repo, 'internal/app/cli/composectl'), { recursive: true })
    writeFileSync(join(repo, 'dashboards/experiments/cfo-demo/source.txt'), 'predecessor\n')
    git('init', '-q')
    git('config', 'user.name', 'Test')
    git('config', 'user.email', 'test@example.invalid')
    git('add', '.')
    git('commit', '-qm', 'predecessor')
    const predecessor = git('rev-parse', 'HEAD')
    writeFileSync(join(repo, pinPath), pin(predecessor))
    git('add', '.')
    git('commit', '-qm', 'candidate')
    execFileSync('git', ['clone', '-q', '--depth=1', `file://${repo}`, shallow])
    const run = (cwd: string) => spawnSync('bash', ['-euo', 'pipefail', '-c', preflight.run], { cwd })
    const missing = run(shallow)
    expect(missing.status).not.toBe(0)
    expect(missing.stderr.toString()).toContain('Historical qualification source is unavailable')
    execFileSync('git', ['-C', shallow, 'fetch', '-q', '--unshallow'])
    expect(run(shallow).status).toBe(0)
    expect(execFileSync('git', ['-C', shallow, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()).toBe(git('rev-parse', 'HEAD'))
    for (const content of [pin('main'), pin(predecessor) + pin(predecessor)]) {
      writeFileSync(join(repo, pinPath), content)
      expect(run(repo).status).not.toBe(0)
    }
    // The pinned commit must contain the fixture, even if the commit itself exists.
    rmSync(join(repo, 'dashboards'), { recursive: true })
    git('add', '-A')
    git('commit', '-qm', 'missing fixture')
    writeFileSync(join(repo, pinPath), pin(git('rev-parse', 'HEAD')))
    expect(run(repo).status).not.toBe(0)
  } finally {
    rmSync(directory, { recursive: true })
  }
})

test('hosted SQLC experiment is manual, bounded and preserves failed samples', () => {
  const config = workflow('sqlc-image-screen')
  expect(Object.keys(config.on)).toEqual(['workflow_dispatch'])
  expect(config.jobs.producers.strategy.matrix.mode).toEqual(['baseline', 'treatment'])
  expect(config.jobs.consumers.needs).toEqual(['producers'])
  expect(config.jobs.consumers.strategy.matrix).toEqual({ mode: ['baseline', 'treatment'], pair: [1, 2, 3] })
  for (const name of ['producers', 'consumers']) {
    expect(config.jobs[name].strategy['fail-fast']).toBe(false)
    expect(config.jobs[name].strategy['max-parallel']).toBe(2)
    expect(config.jobs[name].uses).toBe('./.github/workflows/sqlc-image-sample.yml')
  }
  expect(config.jobs.compare.if).toBe('${{ !cancelled() }}')
  expect(config.jobs.compare.needs).toEqual(['producers', 'consumers'])
  // BuildKit's exporter reads its mutable cache index even without cache-from.
  expect(config.jobs.producers['cache-mode']).toBe('write')
  expect(config.jobs.consumers['cache-mode']).toBe('read')
  const download = config.jobs.compare.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(download.with.pattern).toBe('sqlc-sample-${{ github.run_attempt }}-*')
})

test('producer cache access is exercised before the expensive measured build', () => {
  const steps = workflow('sqlc-image-sample').jobs.sample.steps
  const prepare = steps.find((step: any) => step.name === 'Prepare cache access probe')
  const probe = steps.find((step: any) => step.name === 'Verify producer cache export access')
  expect(prepare?.if).toBe("inputs.kind == 'producer'")
  expect(probe?.if).toBe("inputs.kind == 'producer'")
  expect(probe['continue-on-error']).toBeUndefined()
  expect(probe.with['cache-from']).toBeUndefined()
  expect(probe.with['cache-to']).toBe('type=gha,version=2,mode=max,scope=${{ env.SCREEN_SCOPE }}-access-probe')
  expect(probe.with.outputs).toBe('type=cacheonly')
  expect(probe.with.builder).toBe('${{ steps.builder.outputs.name }}')
  expect(steps.indexOf(probe)).toBeLessThan(steps.findIndex((step: any) => step.name === 'Start full image build clock'))
  const build = steps.find((step: any) => step.id === 'build')
  expect(build.if).toBeUndefined()
  const history = steps.find((step: any) => step.name === 'Retain exact measured build history')
  expect(history.if).toContain("steps.build.outcome != 'skipped'")
  const directory = mkdtempSync(join(tmpdir(), 'sqlc-access-probe-'))
  try {
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', prepare.run], {
      env: { ...process.env, SCREEN_DIR: directory },
    })
    expect(result.status, result.stderr.toString()).toBe(0)
    expect(readFileSync(join(directory, 'cache-probe/Dockerfile'), 'utf8')).toBe('FROM scratch\nCOPY marker /marker\n')
    expect(readFileSync(join(directory, 'cache-probe/marker'), 'utf8')).toBe('sqlc-cache-access-probe\n')
  } finally {
    rmSync(directory, { recursive: true })
  }
})

test('sample authority rejects other repositories, events, branches and revisions', () => {
  const job = workflow('sqlc-image-sample').jobs.sample
  const guard = job.steps.find((step: any) => step.name === 'Verify trusted measurement authority').run
  const env = { ...process.env, GITHUB_REPOSITORY: 'flidai/leapview', GITHUB_EVENT_NAME: 'workflow_dispatch',
    GITHUB_REF: 'refs/heads/main', GITHUB_SHA: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim() }
  expect(spawnSync('bash', ['-euo', 'pipefail', '-c', guard], { env }).status).toBe(0)
  for (const change of [{ GITHUB_REPOSITORY: 'fork/leapview' }, { GITHUB_EVENT_NAME: 'pull_request' },
    { GITHUB_REF: 'refs/heads/topic' }, { GITHUB_SHA: 'main' }, { GITHUB_SHA: '0'.repeat(40) }]) {
    expect(spawnSync('bash', ['-euo', 'pipefail', '-c', guard], { env: { ...env, ...change } }).status).not.toBe(0)
  }
})

test('producer and consumer caches are isolated from production and from one another', () => {
  const config = workflow('sqlc-image-sample')
  const job = config.jobs.sample
  expect(job['runs-on']).toBe('ubuntu-24.04')
  expect(job.env.SCREEN_SCOPE).toBe('sqlc-image-screen-${{ github.run_id }}-${{ github.run_attempt }}-${{ inputs.mode }}')
  const build = job.steps.find((step: any) => step.id === 'build')
  expect(build.with['cache-from']).toBe("${{ inputs.kind == 'consumer' && format('type=gha,version=2,scope={0}', env.SCREEN_SCOPE) || '' }}")
  expect(build.with['cache-to']).toBe("${{ inputs.kind == 'producer' && format('type=gha,version=2,mode=max,scope={0}', env.SCREEN_SCOPE) || '' }}")
  expect(build.with.push).toBe(false)
  expect(build.with.load).toBe(true)
  expect(build.with.target).toBe('runtime')
  expect(build.with.platforms).toBe('linux/amd64')
  expect(config.permissions).toEqual({ contents: 'read', packages: 'read', actions: 'read' })
  expect(JSON.stringify(config)).not.toContain('scope=production')
})

test('every sample retains generation proof and exact historical image qualification', () => {
  const steps = workflow('sqlc-image-sample').jobs.sample.steps
  const proof = steps.find((step: any) => step.name === 'Validate generation and cache evidence')
  expect(proof.run).toContain('ci_sqlc_hosted_screen.py validate')
  const qualify = steps.find((step: any) => step.name === 'Qualify the exact full image')
  expect(qualify.if).toBeUndefined()
  expect(qualify.env.LEAPVIEW_HISTORICAL_TRANSITION_REQUIRED).toBe('1')
  expect(qualify.env.LEAPVIEW_HISTORICAL_TRANSITION_FINAL_ARTIFACT).toBe('0')
  expect(qualify.run).toContain('task test:qualification:historical-transition')
  const retain = steps.find((step: any) => step.name === 'Retain sample evidence including failures')
  expect(retain.if).toBe('always()')
  expect(retain.with.name).toContain('${{ github.run_attempt }}')
  expect(retain.with.overwrite).toBeUndefined()
  expect(retain.with.path).not.toContain('/context')
  expect(retain.with.path).not.toContain('/proof')
})

test('retained build metadata excludes credential-bearing cache attributes', () => {
  const job = workflow('sqlc-image-sample').jobs.sample
  expect(job.env.DOCKER_BUILD_RECORD_UPLOAD).toBe('false')
  const step = job.steps.find((item: any) => item.name === 'Retain exact measured build history')
  const directory = mkdtempSync(join(tmpdir(), 'sqlc-metadata-'))
  const mock = `docker() {
    if [[ "$*" == *"history inspect"* ]]; then
      printf '%s\\n' '{"Ref":"record","Duration":123,"Config":{"RestRaw":{"cache-imports":"token=secret-cache-credential"}}}'
    else
      printf '%s\\n' 'build_phase=sqlc elapsed_seconds=1 exit_code=0'
    fi
  }\n`
  try {
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', mock + step.run], { env: {
      ...process.env, SCREEN_DIR: directory, BUILDER: 'builder', BUILD_OUTCOME: 'success',
      BUILD_METADATA: JSON.stringify({ 'buildx.build.ref': 'builder/node/record',
        'containerimage.digest': 'sha256:' + 'a'.repeat(64), extra: 'secret-cache-credential' }),
    } })
    expect(result.status, result.stderr.toString()).toBe(0)
    for (const name of ['build-metadata.json', 'build-history.json']) {
      expect(readFileSync(join(directory, name), 'utf8')).not.toContain('secret-cache-credential')
    }
    expect(JSON.parse(readFileSync(join(directory, 'build-history.json'), 'utf8')).Duration).toBe(123)
  } finally {
    rmSync(directory, { recursive: true })
  }
})
