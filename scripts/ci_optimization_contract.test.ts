import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { parse } from 'yaml'

const workflow = (name: string) => parse(readFileSync(`.github/workflows/${name}.yml`, 'utf8'))
const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks

test('orchestration uses explicit locked setup without application caches or installers', () => {
  const action = parse(readFileSync('.github/actions/setup-ci/action.yml', 'utf8'))
  expect(action.inputs.profile.default).toBe('validation')
  for (const name of ['prepare', 'ci-gate']) {
    const setup = workflow('ci').jobs[name].steps.find((step: any) => step.uses === './.github/actions/setup-ci')
    expect(setup.with).toEqual({ profile: 'orchestration' })
  }
  for (const step of action.runs.steps) {
    if ((step.uses?.includes('actions/cache') && step.id !== 'orchestration-cache') || ['Resolve selected compiler identity',
      'Resolve Go validation cache paths', 'Set up Node.js', 'Set up Bun',
      'Configure bounded tool caches', 'Install pinned CI tools'].includes(step.name)) {
      expect(String(step.if), String(step.name)).toContain("inputs.profile == 'validation'")
    }
  }
  const flake = readFileSync('flake.nix', 'utf8')
  expect(flake).toContain('orchestration = pkgs.mkShellNoCC')
  expect(flake).toContain('packages = toolchain.orchestrationPackages')
})

test('setup rejects unsupported orchestration contracts instead of silently extending them', () => {
  const action = parse(readFileSync('.github/actions/setup-ci/action.yml', 'utf8'))
  const command = action.runs.steps[0].run
  const env = { ...process.env, TOOLCHAIN: 'auto', CI_PROFILE: 'orchestration',
    CI_BROWSER: 'false', CI_TERRAFORM: 'false', CI_PLATFORM: 'Linux-X64' }
  expect(spawnSync('bash', ['-e', '-c', command], { env }).status).toBe(0)
  for (const change of [{ TOOLCHAIN: 'conventional' }, { CI_PLATFORM: 'macOS-ARM64' },
    { CI_BROWSER: 'true' }, { CI_TERRAFORM: 'true' }, { CI_PROFILE: 'unknown' }]) {
    expect(spawnSync('bash', ['-e', '-c', command], { env: { ...env, ...change } }).status).not.toBe(0)
  }
})

test('Electron package matrix selects Linux before allocating PR runners', () => {
  const job = workflow('electron-security-proof').jobs.packages
  // Both branches are literal JSON, selected entirely before matrix expansion.
  const matrices = [...job.strategy.matrix.matchAll(/'({"include":.*?})'/g)]
    .map((match: RegExpMatchArray) => JSON.parse(match[1]))
  expect(job.strategy.matrix).toContain("github.event_name == 'pull_request'")
  expect(matrices).toHaveLength(2)
  expect(matrices[0].include.map((entry: any) => entry.artifact)).toEqual(['linux-x64'])
  expect(matrices[1].include.map((entry: any) => entry.artifact))
    .toEqual(['linux-x64', 'macos-x64', 'macos-arm64', 'windows-x64'])
  expect(job.if).toBe("${{ github.event_name != 'pull_request' || !github.event.pull_request.draft }}")
  for (const step of job.steps) expect(step.if).toBeUndefined()
})

test('nightly diagnostic detects credentials before installing its toolchain', () => {
  const job = workflow('nightly').jobs['agent-tool-evaluation']
  expect(job['continue-on-error']).toBe(true)
  const credentials = job.steps.findIndex((step: any) => step.id === 'credentials')
  const setup = job.steps.findIndex((step: any) => step.uses === './.github/actions/setup-ci')
  expect(credentials).toBeLessThan(setup)
  expect(job.steps[setup].if).toBe("steps.credentials.outputs.available == 'true'")
  expect(job.steps.find((step: any) => step.name === 'Run all-tools headless evaluation').if)
    .toBe("steps.credentials.outputs.available == 'true'")
  expect(workflow('nightly').jobs['ci-gate'].needs).not.toContain('agent-tool-evaluation')
})

test('macOS local Docker watches Go dependencies and cancels only superseded PR work', () => {
  const config = workflow('localdocker-macos')
  expect(config.on.pull_request.paths).toContain('go.mod')
  expect(config.on.pull_request.paths).toContain('go.sum')
  expect(config.concurrency.group).toBe('localdocker-macos-${{ github.event.pull_request.number || github.ref }}')
  expect(config.concurrency['cancel-in-progress']).toBe("${{ github.event_name == 'pull_request' }}")
  expect(config.on).toHaveProperty('workflow_dispatch')
})

test('dbt reference installs the same requirements as Azure qualification', () => {
  const reference = workflow('dbt-warehouse-boundary-reference').jobs
  const installs = Object.values(reference).flatMap((job: any) => job.steps ?? [])
    .filter((step: any) => step.run?.includes('pip install')) as any[]
  expect(installs).toHaveLength(1)
  expect(installs[0].run).toContain('--requirement examples/dbt-warehouse-boundary/dbt/requirements.txt')
})

test('APIGen generation cache keys include the TypeSpec library and package inputs', () => {
  const inputs = [
    'pkg/apigen/typespec/lib/**/*.tsp',
    'pkg/apigen/typespec/package.json',
    'pkg/apigen/typespec/package-lock.json',
  ]
  const generators = [
    'api:generate',
    'agent-contracts:generate',
    'data-resource-contracts:generate',
    'desktop-discovery:generate',
    'ui-signals:generate',
    'visualization-ir:generate',
    'dashboard-contracts:generate',
    'pipeline-contracts:generate',
  ]

  for (const name of generators) {
    expect(tasks[name].sources).toEqual(expect.arrayContaining(inputs))
  }
})

test('hosted docs retain regeneration and Go checks while the site shard owns browser tests', () => {
  const docs = tasks['ci:test:docs'].cmds
  expect(docs[0]).toContain('-- task --force docs:generate')
  expect(docs).toContainEqual({ task: 'site:observation:test' })
  expect(docs).toContainEqual({ task: 'docs:check' })
  expect(docs).toContain('go test ./cmd/leapview-site ./docs ./site ./internal/app/site/...')
  expect(docs.join('\n')).not.toMatch(/test:site|test:docs-diagrams|site:build/)
  expect(tasks['ci:test:docs-site'].cmds).toEqual([
    { task: 'ci:prepare' }, { task: 'ci:test:docs' }, { task: 'ci:test:frontend:site' },
  ])
  expect(tasks['ci:test:frontend:site'].cmds).toEqual([
    'bun run test:docs-diagrams', 'bun run test:site:prepared',
  ])
  const job = workflow('ci').jobs['docs-validation']
  expect(job.steps.find((step: any) => step.name === 'Validate documentation and embedded site contracts').run)
    .toBe('task ci:test:docs')
  expect(job.steps.some((step: any) => step.run === 'task generated:check')).toBe(true)
})

test('image dependency installation precedes source copies and site generation runs once', () => {
  for (const name of ['Dockerfile', 'Dockerfile.site']) {
    const file = readFileSync(name, 'utf8')
    const web = file.slice(file.indexOf(' AS web\n'), file.indexOf(' AS build\n'))
    expect(web.indexOf('RUN bun install --frozen-lockfile --no-cache')).toBeLessThan(web.indexOf('COPY scripts'))
    expect(web.indexOf('COPY package.json bun.lock tsconfig.json')).toBeLessThan(web.indexOf('RUN bun install'))
    // schema export reaches application code; do not narrow generator inputs.
    expect(file).toContain('COPY . .\n')
  }
  const site = readFileSync('Dockerfile.site', 'utf8')
  expect(site.match(/go run -tags=duckdb_arrow \.\/internal\/app\/tools\/visualdocgen/g)).toHaveLength(1)
})

test('historical transition reads production layers but builds and verifies the exact candidate', () => {
  const steps = workflow('demo-upgrade-qualification').jobs['historical-transition'].steps
  const setup = steps.findIndex((step: any) => step.uses?.startsWith('docker/setup-buildx-action@'))
  const identity = steps.findIndex((step: any) => step.id === 'local-identity')
  const build = steps.findIndex((step: any) => step.uses?.startsWith('docker/build-push-action@'))
  const verify = steps.findIndex((step: any) => step.name === 'Verify the local candidate revision')
  const qualify = steps.findIndex((step: any) => step.name === 'Run the required historical transition fixture')
  expect(setup).toBeGreaterThan(-1)
  expect(identity).toBeGreaterThan(setup)
  expect(build).toBeGreaterThan(identity)
  expect(verify).toBeGreaterThan(build)
  expect(qualify).toBeGreaterThan(verify)
  expect(steps[build].with).toMatchObject({ context: '.', file: 'Dockerfile',
    platforms: 'linux/amd64', load: true, push: false, 'cache-from': 'type=gha,scope=production-amd64' })
  expect(steps[build].with['cache-to']).toBeUndefined()
  expect(steps[build].with['build-args']).toContain('BUILD_REVISION=${{ inputs.candidate_revision || github.sha }}')
  expect(steps[identity].run).toContain('test "$(git rev-parse HEAD)" = "${SOURCE_REVISION}"')
  expect(steps[verify].run).toContain('.revision == $revision and .dirty == false')
  for (const index of [setup, identity, build, verify]) expect(steps[index].if).toBe('${{ !inputs.final_artifact }}')
})
