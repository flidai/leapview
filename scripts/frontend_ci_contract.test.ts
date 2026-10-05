import { expect, test } from 'bun:test'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { parse } from 'yaml'

const shards = ['core', 'reports', 'chat', 'data', 'site']
const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks

for (const [target, downloadFails] of [
  ['test:go:app:shards', false],
  ['test:go:app:shards', true],
  ['generate', true],
] as const) {
  test(`Go module preparation for ${target} ${downloadFails ? 'blocks compilation on failure' : 'preserves test HTTP transport'}`, () => {
    const fixture = mkdtempSync(join(tmpdir(), 'leapview-go-preparation-'))
    try {
      const bin = join(fixture, 'bin')
      const events = join(fixture, 'events.log')
      mkdirSync(bin)
      writeFileSync(join(bin, 'go'), `#!/bin/sh
printf '%s|%s\\n' "$GODEBUG" "$*" >> "$CI_GO_EVENT_LOG"
if [ "$1:$2" = mod:download ]; then
  test "$CI_MODULE_DOWNLOAD_FAIL" != 1
elif [ "$1" = run ]; then
  printf '^TestFixture$\\n'
fi
`, { mode: 0o755 })
      const result = spawnSync('task', ['--taskfile', join(process.cwd(), 'Taskfile.yml'), target], {
        cwd: fixture,
        encoding: 'utf8',
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH}`,
          GODEBUG: 'http2client=1',
          CI_GO_EVENT_LOG: events,
          CI_MODULE_DOWNLOAD_FAIL: downloadFails ? '1' : '0',
        },
      })
      const calls = readFileSync(events, 'utf8').trim().split('\n')
      expect(calls[0]).toBe('http2client=0|mod download')
      expect(calls.filter(call => call.endsWith('|mod download'))).toHaveLength(1)
      if (downloadFails) {
        expect(result.status).not.toBe(0)
        expect(calls).toHaveLength(1)
      } else {
        expect(result.status).toBe(0)
        expect(calls.filter(call => call.includes('|test ./internal/app '))).toHaveLength(4)
        expect(calls.slice(1).every(call => call.startsWith('http2client=1|'))).toBe(true)
      }
      for (const lane of ['generate', 'ci:prepare', 'test:go:app:shards', 'test:go:packages']) {
        expect(tasks[lane].cmds[0]).toEqual({ task: 'go:deps' })
        expect(tasks[lane].env?.GODEBUG).toBeUndefined()
      }
    } finally {
      rmSync(fixture, { recursive: true, force: true })
    }
  })
}

test('catalog page browser fixtures run in separate test processes', () => {
  const scripts = JSON.parse(readFileSync('package.json', 'utf8')).scripts
  const testProcesses = scripts['test:catalog-page'].split(' && ').filter((command: string) => command.startsWith('bun test '))
  for (const fixture of ['catalog-page.dom.test.ts', 'catalog-page.pins.dom.test.ts']) {
    expect(testProcesses.filter((command: string) => command.includes(fixture)))
      .toEqual([`bun test web/components/app/${fixture}`])
  }
})

test('local frontend validation runs every bounded shard without suppressing failure', () => {
  expect(tasks['ci:lane:frontend'].cmds).toEqual(shards.map((shard) => ({
    task: 'ci:lane:frontend:shard', vars: { SHARD: shard },
  })))
  expect(tasks['ci:lane:frontend:shard'].cmds).toEqual([
    'node scripts/ci_watchdog.mjs --timeout-seconds {{if eq .SHARD "reports"}}300{{else}}180{{end}} --attempts 2 -- task ci:test:frontend:{{.SHARD}}',
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
    permission_profile: '${{ steps.resolve.outputs.permission_profile }}',
  })
  expect(resolver.run).toBe('python3 scripts/demo_runtime_record.py resolve')

  const deploy = config.jobs.deploy
  expect(deploy.needs).toEqual(['publication-source', 'runtime'])
  expect(deploy.if).toBe("${{ always() && (needs.publication-source.outputs.publish == 'true' || (needs.runtime.result == 'success' && contains(fromJSON('[\"deploy\",\"upgrade\"]'), inputs.action))) }}")
  expect(deploy.env.SOURCE_REVISION).toBe('${{ needs.publication-source.outputs.revision || needs.runtime.outputs.revision }}')
  expect(deploy.env.DEMO_PERMISSION_PROFILE).toBe('${{ needs.publication-source.outputs.permission_profile || needs.runtime.outputs.permission_profile }}')
  expect(deploy.env.DEMO_DATASET).toBe("${{ vars.DEMO_DATASET || 'olist' }}")
  const steps = deploy.steps
  const setupIndex = steps.findIndex((step: any) => step.uses === './.github/actions/setup-ci')
  const generateIndex = steps.findIndex((step: any) => step.run === 'task generate')
  const publishIndex = steps.findIndex((step: any) => step.run === './scripts/deploy_demo.sh')

  expect(setupIndex).toBeGreaterThan(-1)
  expect(generateIndex).toBeGreaterThan(setupIndex)
  expect(publishIndex).toBeGreaterThan(generateIndex)
})

test('hosted demo runs the protected version-aware publication adapter against the selected source checkout', () => {
  const config = parse(readFileSync('.github/workflows/demo-deploy.yml', 'utf8'))
  const steps = config.jobs.deploy.steps
  const selectedSource = steps.findIndex((step: any) => step.name === 'Check out the qualified revision')
  const adapterCheckout = steps.findIndex((step: any) => step.name === 'Check out current publication adapter')
  const adapterInstall = steps.findIndex((step: any) => step.name === 'Install current publication adapter in selected source')
  const datasetGuard = steps.findIndex((step: any) => step.name === 'Validate pinned dataset support')
  const credentials = steps.findIndex((step: any) => step.name === 'Fetch demo deployment credentials')
  const publication = steps.findIndex((step: any) => step.id === 'publication')

  expect(selectedSource).toBeGreaterThan(-1)
  expect(adapterCheckout).toBeGreaterThan(-1)
  expect(adapterInstall).toBeGreaterThan(-1)
  expect(steps[selectedSource].with.ref).toBe('${{ env.SOURCE_REVISION }}')
  expect(steps[adapterCheckout].uses).toBe('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1')
  expect(steps[adapterCheckout].with).toEqual({
    ref: '${{ github.sha }}',
    path: '.demo-publication-adapter',
    'fetch-depth': 1,
    'persist-credentials': false,
    'sparse-checkout': 'scripts/deploy_demo.sh\nscripts/demo_client_contract.py\n',
    'sparse-checkout-cone-mode': false,
  })
  expect(adapterCheckout).toBeGreaterThan(selectedSource)
  expect(adapterInstall).toBeGreaterThan(adapterCheckout)
  expect(adapterInstall).toBeLessThan(datasetGuard)
  expect(datasetGuard).toBeLessThan(credentials)
  expect(credentials).toBeLessThan(publication)
  expect(steps[adapterInstall].run).toBe([
    'set -euo pipefail',
    'install -m 0755 .demo-publication-adapter/scripts/deploy_demo.sh scripts/deploy_demo.sh',
    'install -m 0644 .demo-publication-adapter/scripts/demo_client_contract.py scripts/demo_client_contract.py',
    'rm -rf .demo-publication-adapter',
  ].join('\n') + '\n')
})

for (const [target, command] of [['production', 'image'], ['site', 'site-image']]) {
  test(`${target} image qualification generates SQL, API and UI packages before compiling leapviewctl`, () => {
    const commands = tasks[`image:qualify:${target}`].cmds
    // The host upgrade command reaches access/module through its transition
    // intent; that module also compiles the generated login signal package.
    // Both entrypoints must work without a prior application build/generate.
    expect(commands.slice(0, 3)).toEqual([
      { task: 'db:generate' },
      { task: 'api:generate' },
      { task: 'ui-signals:generate' },
    ])
    expect(commands.at(-1)).toContain(`go run ./cmd/leapviewctl qualify ${command}`)
  })
}

test('native PostgreSQL qualification generates the complete application fixture before compilation', () => {
  const commands = tasks['test:qualification:native-postgres'].cmds
  expect(commands[0]).toEqual({ task: 'generate' })
  expect(commands.at(-1)).toContain('TestQualificationNativePostgresTopologyContainerBackedContract')
})

test('hosted demo rejects an unknown dataset before requesting deployment credentials', () => {
  const result = spawnSync('bash', ['scripts/deploy_demo.sh'], {
    env: { PATH: process.env.PATH, DEMO_DATASET: 'unknown' }, encoding: 'utf8',
  })
  expect(result.status).toBe(64)
  expect(result.stderr.trim()).toBe('DEMO_DATASET must be olist or cfo')
})


test('pinned publication rejects unsupported datasets before credentials or publisher execution', () => {
  const workflow = parse(readFileSync('.github/workflows/demo-deploy.yml', 'utf8'))
  const steps = workflow.jobs.deploy.steps
  const guardIndex = steps.findIndex((step: any) => step.name === 'Validate pinned dataset support')
  const sourceCheckout = steps.findIndex((step: any) => step.name === 'Check out the qualified revision')
  expect(guardIndex).toBeGreaterThan(0)
  expect(guardIndex).toBeLessThan(steps.findIndex((step: any) => step.name === 'Fetch demo deployment credentials'))
  expect(sourceCheckout).toBeGreaterThan(-1)
  expect(sourceCheckout).toBeLessThan(guardIndex)
  expect(steps[sourceCheckout].with.ref).toBe('${{ env.SOURCE_REVISION }}')
  const directory = mkdtempSync(join(tmpdir(), 'demo-dataset-guard-'))
  try {
    mkdirSync(join(directory, 'deploy/demo'), { recursive: true })
    const run = (dataset: string) => spawnSync('bash', ['-c', steps[guardIndex].run], {
      cwd: directory, env: { PATH: process.env.PATH, DEMO_DATASET: dataset }, encoding: 'utf8',
    })
    // Old pinned revisions have no capability manifest and only publish Olist.
    expect(run('cfo').status).toBe(64)
    expect(run('olist').status).toBe(0)
    expect(run('unknown').status).toBe(64)
    writeFileSync(join(directory, 'deploy/demo/datasets.txt'), 'olist\n')
    expect(run('cfo').status).toBe(64)
    writeFileSync(join(directory, 'deploy/demo/datasets.txt'), readFileSync('deploy/demo/datasets.txt'))
    expect(run('cfo').status).toBe(0)
    expect(run('olist').status).toBe(0)
    expect(run('unknown').status).toBe(64)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})

test('runtime deployment admits exact evidence before secrets and advances pin only after validation', () => {
  const workflow = parse(readFileSync('.github/workflows/demo-deploy.yml', 'utf8'))
  expect(workflow.on.workflow_dispatch.inputs.action.options).toEqual(['publish', 'prepare', 'deploy', 'upgrade', 'recover', 'reconcile'])
  expect(workflow.on.workflow_run).toBeUndefined()
  expect(workflow.concurrency['cancel-in-progress']).toBe(false)
  const runtime = workflow.jobs.runtime
  expect(runtime.permissions.deployments).toBe('write')
  const steps = runtime.steps
  const qualified = steps.findIndex((s: any) => s.id === 'qualified')
  const admission = steps.findIndex((s: any) => s.uses === './.github/actions/oci-admission')
  const secrets = steps.findIndex((s: any) => s.name === 'Fetch demo deployment credentials')
  const browser = steps.findIndex((s: any) => s.name === 'Set up browser validation')
  const dependencies = steps.findIndex((s: any) => s.run === 'bun install --frozen-lockfile')
  const preflight = steps.findIndex((s: any) => s.run === 'python3 scripts/demo_compose_deploy.py --preflight')
  const record = steps.findIndex((s: any) => s.id === 'record')
  const rollout = steps.findIndex((s: any) => s.id === 'rollout')
  const pin = steps.findIndex((s: any) => s.id === 'reconcile')
  expect(qualified).toBeGreaterThan(-1)
  expect(admission).toBeGreaterThan(qualified)
  expect(browser).toBeGreaterThan(admission)
  expect(dependencies).toBeGreaterThan(browser)
  expect(secrets).toBeGreaterThan(dependencies)
  expect(preflight).toBeGreaterThan(secrets)
  expect(record).toBeGreaterThan(preflight)
  expect(rollout).toBeGreaterThan(record)
  expect(pin).toBeGreaterThan(rollout)
  expect(steps[pin].if).toBe("${{ always() && steps.record.outputs.id != '' }}")
  expect(steps[pin].run).toBe('python3 scripts/demo_runtime_record.py reconcile')
  expect(steps[record].if).toBe("inputs.action != 'prepare'")
  // A failed rollout must still reconcile its verified recovery; no raw
  // workflow success flag may invent a running image.
  expect(JSON.stringify(steps)).not.toContain('demo_runtime_record.py success')
  const repair = workflow.jobs['runtime-reconcile'].steps
  const inspect = repair.findIndex((s: any) => s.id === 'inspect')
  const reconcile = repair.findIndex((s: any) => s.run === 'python3 scripts/demo_runtime_record.py reconcile')
  expect(reconcile).toBeGreaterThan(inspect)
  expect(repair[reconcile].if).toBe("${{ always() && steps.inspect.outcome != 'skipped' }}")
  expect(JSON.stringify(steps)).not.toContain('/hetzner-qualification/infrastructure')
  expect(JSON.stringify(steps)).not.toContain('secret-path":"/demo/access')
})


test('publication checks the new CFO generation without invoking another runtime mutation', () => {
  const { jobs } = parse(readFileSync('.github/workflows/demo-deploy.yml', 'utf8'))
  const steps = jobs.deploy.steps
  const publication = steps.findIndex((s: any) => s.id === 'publication')
  const policy = steps.findIndex((s: any) => s.name === 'Check out current publication verification policy')
  const verify = steps.findIndex((s: any) => s.id === 'verify-publication')
  expect(policy).toBeGreaterThan(publication)
  expect(steps[policy].with.ref).toBe('${{ github.sha }}')
  expect(verify).toBeGreaterThan(policy)
  expect(steps[verify].if).toBe("env.DEMO_DATASET == 'cfo'")
  expect(steps[verify].run).toBe('bun install --frozen-lockfile\npython3 scripts/demo_compose_deploy.py --verify-publication\n')
  expect(steps[verify].env.DEMO_PROJECT_ID).toBe('${{ vars.DEMO_PROJECT_ID }}')
  expect(JSON.stringify(steps)).not.toContain('demo_runtime_record.py start')
  expect(JSON.stringify(steps)).not.toContain('demo_runtime_record.py reconcile')
})

test('historical browser launcher requires a private proxy and exact synthetic certificate pin', () => {
  const probe = (env: Record<string, string>) => spawnSync('node', ['--input-type=module', '-e',
    `import { historicalBrowserOptions } from './internal/app/cli/composectl/testdata/historical_browser.mjs';
     process.stdout.write(JSON.stringify(historicalBrowserOptions(process.env)));`], {
    env: { PATH: process.env.PATH, ...env }, encoding: 'utf8',
  })
  const valid = {
    DEMO_CLONE_ONLY: '1',
    DEMO_BROWSER_PROXY: 'http://127.0.0.1:43210',
    DEMO_CLONE_PROXY: 'http://127.0.0.1:43210',
    DEMO_HISTORICAL_BROWSER_SPKI: Buffer.alloc(32, 1).toString('base64'),
  }
  expect(probe(valid).status).toBe(0)
  for (const change of [
    { DEMO_CLONE_ONLY: '' },
    { DEMO_CLONE_PROXY: 'http://127.0.0.1:43211' },
    { DEMO_BROWSER_PROXY: 'http://example.com:43210' },
    { DEMO_BROWSER_PROXY: 'http://127.0.0.1:99999' },
    { DEMO_HISTORICAL_BROWSER_SPKI: '' },
    { DEMO_HISTORICAL_BROWSER_SPKI: '--ignore-certificate-errors' },
    { DEMO_HISTORICAL_BROWSER_SPKI: valid.DEMO_HISTORICAL_BROWSER_SPKI + ',extra-pin' },
  ]) expect(probe({ ...valid, ...change }).status).not.toBe(0)
  const options = JSON.parse(probe(valid).stdout)
  expect(options.ignoreHTTPSErrors).toBeUndefined()
  expect(options.args).toEqual(['--ignore-certificate-errors-spki-list=' + valid.DEMO_HISTORICAL_BROWSER_SPKI])
})
