import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { parse } from 'yaml'

const action = parse(readFileSync('.github/actions/setup-ci/action.yml', 'utf8'))
const steps = action.runs.steps

test('hosted Linux CI configures the Docker Hub cache before container work', () => {
  const development = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const helper = 'scripts/configure_ci_docker_mirror.py'
  const dockerAction = parse(readFileSync('.github/actions/setup-docker/action.yml', 'utf8'))
  expect(dockerAction.runs.steps).toHaveLength(2)
  const [mask, configure] = dockerAction.runs.steps
  expect(mask).toMatchObject({ if: configure.if, shell: 'bash', run: 'python3 scripts/mask_ci_docker_credentials.py' })
  expect(mask['continue-on-error']).toBeUndefined()
  expect(configure.if).toBe("runner.os == 'Linux' && runner.environment == 'github-hosted'")
  expect(configure.run).toBe(`sudo --preserve-env=GITHUB_ACTIONS,RUNNER_OS,RUNNER_ENVIRONMENT python3 ${helper}` + " ${{ inputs.postgres == 'true' && '--postgres' || '' }}")
  expect(configure['continue-on-error']).toBeUndefined()
  for (const setup of [steps, development.jobs.development.steps, development.jobs.image.steps]) {
    const index = setup.findIndex((step: any) => step.uses === './.github/actions/setup-docker')
    expect(index).toBeGreaterThanOrEqual(0)
    expect(setup.filter((step: any) => step.uses === './.github/actions/setup-docker')).toHaveLength(1)
    expect(setup.some((step: any) => step.run?.includes(helper))).toBe(false)
    if (setup === steps) {
      expect(setup[index].if).toBe("inputs.profile == 'validation' && runner.os == 'Linux' && runner.environment == 'github-hosted'")
    } else {
      expect(setup[index].if).toBeUndefined()
    }
    expect(setup[index]['continue-on-error']).toBeUndefined()
    const containerWork = setup.findIndex((step: any) => /Install.*Nix|ci:prepare|nix:smoke|#leapview-image/.test(step.name ?? step.run ?? ''))
    expect(index).toBeLessThan(containerWork)
  }
  for (const file of [helper, 'scripts/mask_ci_docker_credentials.py', 'scripts/tests/test_ci_docker_credentials.py', 'scripts/tests/test_ci_docker_mirror.py', '.github/actions/setup-docker/action.yml', '.github/docker/buildkitd.toml']) {
    expect(development.on.pull_request.paths.some((pattern: string) => new Bun.Glob(pattern).match(file))).toBe(true)
  }
  const result = spawnSync('python3', ['-B', '-m', 'unittest', 'discover', '-s', 'scripts/tests', '-p', 'test_ci_docker_*.py'], { encoding: 'utf8' })
  if (result.status !== 0) throw new Error(result.stdout + result.stderr)
  expect(result.status).toBe(0)
})

test('application CI preloads the pinned PostgreSQL image before starting independent shards', () => {
  expect(action.inputs.postgres.default).toBe('false')
  const docker = steps.find((step: any) => step.uses === './.github/actions/setup-docker')
  expect(docker.with.postgres).toBe('${{ inputs.postgres }}')
  for (const file of ['.github/workflows/ci.yml', '.github/workflows/merge-validation.yml']) {
    const workflow = parse(readFileSync(file, 'utf8'))
    const application = workflow.jobs['go-application-validation'].steps
    const setup = application.findIndex((step: any) => step.uses === './.github/actions/setup-ci')
    const run = application.findIndex((step: any) => step.run === 'task ci:lane:go:application')
    expect(setup).toBeGreaterThanOrEqual(0)
    expect(setup).toBeLessThan(run)
    expect(application[setup].with.postgres).toBe('true')
    expect(application[setup]['continue-on-error']).toBeUndefined()
    expect(workflow.jobs['frontend-validation'].steps.find((step: any) => step.uses === './.github/actions/setup-ci').with.postgres).toBeUndefined()
  }
})

test('standalone Electron Linux proof configures the guarded mirror immediately after checkout', () => {
  const proof = parse(readFileSync('.github/workflows/electron-security-proof.yml', 'utf8'))
  const setup = proof.jobs.linux.steps
  const checkout = setup.findIndex((step: any) => step.uses?.startsWith('actions/checkout@'))
  const mirror = setup.findIndex((step: any) => step.uses === './.github/actions/setup-docker')
  expect(mirror).toBe(checkout + 1)
  expect(setup.filter((step: any) => step.uses === './.github/actions/setup-docker')).toHaveLength(1)
  expect(setup.some((step: any) => step.run?.includes('scripts/configure_ci_docker_mirror.py'))).toBe(false)
  expect(setup[mirror]['continue-on-error']).toBeUndefined()
  expect(mirror).toBeLessThan(setup.findIndex((step: any) => step.run?.includes('docker build')))
})

test('managed scaffold Ruby bootstrap keeps the exact official-image digest on the public mirror', () => {
  const scaffold = parse(readFileSync('.github/workflows/managed-scaffold.yml', 'utf8'))
  expect(scaffold.jobs['application-template'].container.image).toBe(
    'public.ecr.aws/docker/library/ruby:3.4@sha256:c4428c90c4e80ee5848c31912969e73c9f48ac45ec45dfb5f76d1a821358771e')
})

test('active recovery qualification configures its separate BuildKit resolver before building', () => {
  const ci = parse(readFileSync('.github/workflows/ci.yml', 'utf8'))
  expect(ci.jobs['host-recovery-validation'].uses).toBe('./.github/workflows/demo-upgrade-qualification.yml')
  const recovery = parse(readFileSync('.github/workflows/demo-upgrade-qualification.yml', 'utf8'))
  const setup = recovery.jobs['historical-transition'].steps
  const toolchain = setup.findIndex((step: any) => step.uses === './.github/actions/setup-ci')
  const builder = setup.findIndex((step: any) => step.uses?.startsWith('docker/setup-buildx-action@'))
  const build = setup.findIndex((step: any) => step.uses?.startsWith('docker/build-push-action@'))
  expect(toolchain).toBeLessThan(builder)
  expect(builder).toBeLessThan(build)
  expect(setup[builder].if).toBe('${{ !inputs.final_artifact }}')
  expect(setup[builder]['continue-on-error']).toBeUndefined()
  expect(setup[builder].with?.['buildkitd-config']).toBe('.github/docker/buildkitd.toml')
  expect(setup[builder].with?.['buildkitd-config-inline']).toBeUndefined()
  expect(readFileSync('.github/docker/buildkitd.toml', 'utf8').trim()).toBe(
    '[registry."docker.io"]\n  mirrors = ["mirror.gcr.io"]')
  expect(setup[build].with.file).toBe('Dockerfile')
})
