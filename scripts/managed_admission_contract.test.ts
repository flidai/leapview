import { expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { parse } from 'yaml'

const workflow = (name: string) => parse(readFileSync(`.github/workflows/${name}.yml`, 'utf8'))
const expression = (value: string) => '${{ ' + value + ' }}'

for (const profile of [
  { file: 'artifacts', job: 'qualify-production-image', event: 'push', arch: 'amd64' },
  { file: 'release', job: 'image-platform', event: 'workflow_dispatch', arch: expression('matrix.arch') },
]) {
  test(`${profile.file} exports owner admission only for its authenticated main producer`, () => {
    const steps = workflow(profile.file).jobs[profile.job].steps
    const admit = steps.find((step: any) => step.id === 'admission')
    expect(admit.with['admission-bundle']).toContain(`github.event_name == '${profile.event}'`)
    expect(admit.with['admission-bundle']).toContain("github.ref == 'refs/heads/main'")
    expect(admit.with['admission-bundle']).toContain("|| ''")
    expect(admit.with['release-id']).toBeDefined()
    expect(admit.with['release-version']).toBeDefined()
    const upload = steps.find((step: any) => step.id === 'admission-bundle')
    expect(upload.if).toBe(`github.event_name == '${profile.event}' && github.ref == 'refs/heads/main'`)
    expect(upload.uses).toMatch(/^actions\/upload-artifact@[0-9a-f]{40}$/)
    expect(upload.with.name).toBe(`managed-admission-${expression('github.run_id')}-${expression('github.run_attempt')}-${profile.arch}`)
    expect(upload.with.path).toBe(expression('runner.temp') + '/managed-admission')
    expect(upload.with['if-no-files-found']).toBe('error')
    expect(upload.if).not.toContain('always()')
    expect(steps.indexOf(upload)).toBeGreaterThan(steps.indexOf(admit))
  })
}

test('canonical owner export is explicit and is not added to other image admission callers', () => {
  const release = workflow('release')
  for (const [name, job] of Object.entries(release.jobs) as [string, any][]) {
    if (name === 'image-platform') continue
    for (const step of job.steps ?? []) {
      if (step.uses === './.github/actions/oci-admission') {
        expect(step.with['admission-bundle']).toBeUndefined()
      }
    }
  }
  const action = parse(readFileSync('.github/actions/oci-admission/action.yml', 'utf8'))
  for (const input of ['admission-bundle', 'release-id', 'release-version']) {
    expect(action.inputs[input].required).toBe(false)
  }
  const command = action.runs.steps.find((step: any) => step.id === 'admit')
  expect(command.run).toContain('if [[ -n "$ADMISSION_BUNDLE" ]]')
  expect(command.run).toContain('--admission-bundle "$ADMISSION_BUNDLE"')
  expect(command.run).toContain('--release-id "$RELEASE_ID"')
  expect(command.run).toContain('--release-version "$RELEASE_VERSION"')
})

test('composite action forwards owner identity as exact arguments only when requested', () => {
  const action = parse(readFileSync('.github/actions/oci-admission/action.yml', 'utf8'))
  const command = action.runs.steps.find((step: any) => step.id === 'admit')
  const root = mkdtempSync(join(tmpdir(), 'managed admission action '))
  try {
    const bin = join(root, 'bin')
    const actionPath = join(root, '.github/actions/oci-admission')
    mkdirSync(bin)
    mkdirSync(actionPath, { recursive: true })
    const shell = spawnSync('bash', ['-c', 'command -v bash'], { encoding: 'utf8' }).stdout.trim()
    writeFileSync(join(bin, 'go'), `#!${shell}\nprintf '%s\\n' "$@" > "$ARGUMENT_LOG"\n`, { mode: 0o700 })
    for (const bundle of ['', join(root, 'owner receipt')]) {
      const log = join(root, 'arguments')
      const result = spawnSync('bash', ['-c', command.run], {
        encoding: 'utf8',
        env: {
          ...process.env, PATH: bin + ':' + process.env.PATH,
          GITHUB_ACTION_PATH: actionPath, RUNNER_TEMP: root, ARGUMENT_LOG: log,
          ADMISSION_OUTPUT: '', PLATFORM: 'linux/amd64', VULNERABILITY_REPORT: '',
          ADMISSION_BUNDLE: bundle, RELEASE_ID: 'candidate-123-1', RELEASE_VERSION: '0.3.0',
          IMAGE: 'ghcr.io/flidai/leapview@sha256:' + 'a'.repeat(64),
          OCI_REPOSITORY: 'ghcr.io/flidai/leapview', SOURCE_REVISION: 'b'.repeat(40),
          EXPECTED_WORKFLOW: 'flidai/leapview/.github/workflows/release.yml',
          POLICY: 'policy.json', MODE: 'live', EVIDENCE: '',
        },
      })
      expect(result.status).toBe(0)
      const args = readFileSync(log, 'utf8').trimEnd().split('\n')
      const index = args.indexOf('--admission-bundle')
      if (bundle) {
        expect(args.slice(index, index + 6)).toEqual([
          '--admission-bundle', bundle, '--release-id', 'candidate-123-1', '--release-version', '0.3.0',
        ])
      } else {
        expect(index).toBe(-1)
        expect(args).not.toContain('--release-id')
        expect(args).not.toContain('--release-version')
      }
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
