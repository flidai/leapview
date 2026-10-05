import { expect, test } from 'bun:test'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse } from 'yaml'

test('protected controller and Compose adversarial checks run in local and hosted CI', () => {
  const command = 'python3 -m unittest discover -s scripts/tests -p test_nix_cli_publication.py'
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain(command)
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  expect(workflow.jobs.image.steps.some((step: any) => step.run?.includes(command))).toBe(true)
  expect(workflow.jobs.image.steps.some((step: any) =>
    step.run?.includes(`env LEAPVIEW_TEST_NIX_CLI_RUNTIME=1 ${command}`))).toBe(true)
  for (const script of ['package_compose_bundle', 'nix_compose_qualification']) {
    const testCommand = `python3 -m unittest discover -s scripts/tests -p test_${script}.py`
    expect(readFileSync('Taskfile.yml', 'utf8')).toContain(testCommand)
    expect(workflow.jobs.image.steps.some((step: any) => step.run?.includes(testCommand))).toBe(true)
    expect(workflow.on.pull_request.paths).toContain(`scripts/${script}.py`)
    expect(workflow.on.pull_request.paths).toContain(`scripts/tests/test_${script}.py`)
  }
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain('bun test scripts/nix_compose_candidate_contract.test.ts')
  expect(workflow.on.pull_request.paths).toContain('scripts/nix_compose_candidate_contract.test.ts')
  expect(workflow.on.pull_request.paths).toContain('.github/workflows/nix-compose-candidate.yml')
})

const action = parse(readFileSync('.github/actions/setup-ci/action.yml', 'utf8'))
const steps = action.runs.steps
const locked = "inputs.toolchain == 'auto' && runner.os == 'Linux' && runner.arch == 'X64'"
const conventional = "inputs.toolchain == 'conventional' || runner.os != 'Linux' || runner.arch != 'X64'"

test('orchestration archive manifest checks run in the CI contract lane', () => {
  const result = spawnSync('python3', ['-m', 'unittest', 'discover', '-s', 'scripts/tests', '-p', 'test_orchestration_cache.py'], { encoding: 'utf8' })
  if (result.status !== 0) throw new Error(result.stdout + result.stderr)
  expect(result.status).toBe(0)
})

test('orchestration cache authorizes only the exact main producer', () => {
  const workflow = parse(readFileSync('.github/workflows/orchestration-cache.yml', 'utf8'))
  const identity = workflow.jobs.resolve.steps.find((step: any) => step.id === 'identity')
  const root = mkdtempSync(join(tmpdir(), 'nix-cache-authority-'))
  try {
    writeFileSync(join(root, 'git'), '#!/bin/sh\nprintf "%s\\n" "$FIXTURE_HEAD"\n', { mode: 0o755 })
    const head = 'a'.repeat(40)
    for (const fixture of [
      { operation: 'produce', ref: 'refs/heads/main', source: '', sha: head, ok: true },
      { operation: 'produce', ref: 'refs/heads/experiment', source: '', sha: head, ok: false },
      { operation: 'produce', ref: 'refs/heads/main', source: head, sha: head, ok: false },
      { operation: 'produce', ref: 'refs/heads/main', source: '', sha: 'b'.repeat(40), ok: false },
      { operation: 'measure', ref: 'refs/heads/experiment', source: head, sha: 'b'.repeat(40), ok: true },
      { operation: 'measure', ref: 'refs/heads/experiment', source: 'b'.repeat(40), sha: head, ok: false },
      { operation: 'unknown', ref: 'refs/heads/main', source: '', sha: head, ok: false },
    ]) {
      const result = spawnSync('bash', ['-c', identity.run], { encoding: 'utf8', env: {
        ...process.env, PATH: `${root}:${process.env.PATH}`, FIXTURE_HEAD: head,
        OPERATION: fixture.operation, SOURCE_REVISION: fixture.source,
        GITHUB_REF: fixture.ref, GITHUB_SHA: fixture.sha, DEFAULT_BRANCH: 'main',
        INPUT_ID: 'c'.repeat(64), GITHUB_OUTPUT: join(root, 'outputs'),
      } })
      expect(result.status === 0).toBe(fixture.ok)
    }
    const saves = Object.entries(workflow.jobs).flatMap(([job, value]: [string, any]) =>
      (value.steps ?? []).filter((step: any) => step.uses?.startsWith('actions/cache/save@')).map(() => job))
    expect(saves).toEqual(['producer'])
    expect(workflow['cache-mode']).toBe('read')
    expect(workflow.jobs.producer['cache-mode']).toBe('write-only')
    expect(Object.entries(workflow.jobs).filter(([job, value]: [string, any]) =>
      job !== 'producer' && value['cache-mode'] !== undefined)).toEqual([])
    expect(workflow.jobs.producer.needs).toBe('resolve')
    expect(workflow.jobs.measure.strategy.matrix.sample).toEqual(['1', '2', '3'])
    expect(workflow.jobs.measure.strategy.matrix.treatment).toEqual(['baseline', 'restore'])
    expect(workflow.jobs.measure.steps.find((step: any) => step.id === 'restore').with['restore-keys']).toBeUndefined()
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('production orchestration restores only exact keys and retains complete realization', () => {
  const workflow = parse(readFileSync('.github/workflows/orchestration-cache.yml', 'utf8'))
  const restore = steps.find((step: any) => step.id === 'orchestration-cache')
  const realize = steps.find((step: any) => step.name === 'Import orchestration archive and realize the complete locked environment')
  const identity = workflow.jobs.resolve.steps.find((step: any) => step.id === 'identity')
  expect(restore.if).toBe("inputs.profile == 'orchestration'")
  expect(restore.uses).toContain('actions/cache/restore@')
  expect(restore['continue-on-error']).toBe(true)
  expect(restore.with['restore-keys']).toBeUndefined()
  expect(restore.with.key).toBe('nix-orchestration-v1-Linux-X64-2.31.2-' + identity.env.INPUT_ID)
  expect(realize.env.CACHE_INPUT_ID).toBe(identity.env.INPUT_ID)
  expect(realize.run).toBe('python3 scripts/measure_orchestration_cache.py restore')
  const keyFiles = [...identity.env.INPUT_ID.matchAll(/'([^']+)'/g)].map((match: any) => match[1])
  expect(workflow.on.push.branches).toEqual(['main'])
  expect(workflow.on.push.paths).toEqual(keyFiles)
  const lookup = workflow.jobs.resolve.steps.find((step: any) => step.id === 'lookup')
  expect(lookup.with['lookup-only']).toBe(true)
  expect(workflow.jobs.producer.if).toContain("needs.resolve.outputs.cache_hit != 'true'")
  expect(workflow.jobs.producer.permissions.actions).toBe('write')
  expect(Object.entries(workflow.jobs).filter(([job, value]: [string, any]) =>
    job !== 'producer' && value.permissions?.actions === 'write')).toEqual([])
  const ci = parse(readFileSync('.github/workflows/ci.yml', 'utf8'))
  for (const job of ['prepare', 'ci-gate']) expect(ci.jobs[job]['cache-mode']).toBe('read')
})

test('Nix application binaries retain function symbols for exact vulnerability coverage', () => {
  const recipe = readFileSync('nix/application.nix', 'utf8')
  const flags = recipe.match(/^    flags="([^"]+)"/m)![1].split(/\s+/)
  expect(flags).toContain('-w')
  expect(flags).not.toContain('-s')
  expect(recipe).toContain('dontStrip = true;')
})

test('Nix application startup and healthcheck execute the scanned absolute path', () => {
  const recipe = readFileSync('nix/image.nix', 'utf8')
  expect(recipe).toMatch(/Entrypoint = \[ "\/usr\/local\/bin\/leapview" \];/)
  expect(recipe).toMatch(/Healthcheck = \{\s+Test = \[\s+"CMD"\s+"\/usr\/local\/bin\/leapview"\s+"healthcheck"/)
  const compose = parse(readFileSync('deploy/compose/compose.yaml', 'utf8'))
  expect(compose.services.leapview.healthcheck.test)
    .toEqual(['CMD', '/usr/local/bin/leapview', 'healthcheck'])
})

test('static controller scans bind both exact archives without executing them or granting authority', () => {
  const recipe = readFileSync('nix/deployment-cli.nix', 'utf8')
  const flags = recipe.match(/^    flags="([^"]+)"/m)![1].split(/\s+/)
  expect(flags).toContain('-w')
  expect(flags).not.toContain('-s')
  expect(recipe).toContain('dontFixup = true;')
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const job = workflow.jobs['cli-security']
  expect(job.needs).toBe('cli-build')
  expect(job['runs-on']).toBe('ubuntu-24.04') // Cross-architecture inspection needs no candidate execution.
  expect(job.strategy['fail-fast']).toBe(false)
  expect(job.strategy.matrix.arch).toEqual(['amd64', 'arm64'])
  expect(job.permissions).toBeUndefined()
  expect(job.environment).toBeUndefined()
  expect(workflow.permissions).toEqual({ contents: 'read' })
  const download = job.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(download.with['artifact-ids']).toBe('${{ needs.cli-build.outputs.artifact_id }}')
  const scan = job.steps.find((step: any) => step.env?.ARCH)
  expect(scan.run).toContain('scripts/nix_archive_go_evidence.py "$archive" --kind cli-archive')
  expect(scan.run).toContain('--go-evidence .tmp/nix-cli-go-evidence/go')
  expect(scan.run).toContain('for operation in --output --verify; do')
  expect(scan.run).toContain('--source-revision "$(git rev-parse HEAD)"')
  expect(scan.run).not.toContain('tar -')
  expect(scan.run).not.toContain('docker run')
  expect(scan.if).toBeUndefined()
  const retention = job.steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(retention.if).toBe('always()')
  expect(retention.with.path).toBe('.tmp/nix-cli-go-evidence/')
  for (const file of ['scripts/nix_archive_go_evidence.py', 'scripts/tests/test_nix_cli_go_evidence.py',
    'internal/app/tools/securitydependencies/**']) expect(workflow.on.pull_request.paths).toContain(file)
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain('-p test_nix_cli_go_evidence.py')
})

test('static controller qualification preserves the host baseline and exact artifact transfer', () => {
  const { jobs } = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const build = jobs['cli-build']
  const qualify = jobs['cli-compatibility']
  expect(build.outputs.artifact_id).toBe('${{ steps.upload.outputs.artifact-id }}')
  expect(qualify.needs).toBe('cli-build')
  expect(qualify.strategy['fail-fast']).toBe(false)
  expect(qualify.strategy.matrix.include.map((row: any) => [row.arch, row.runner]))
    .toEqual([['amd64', 'ubuntu-24.04'], ['arm64', 'ubuntu-24.04-arm']])
  const download = qualify.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(download.with['artifact-ids']).toBe('${{ needs.cli-build.outputs.artifact_id }}')
  expect(download.with['merge-multiple']).toBe(true)
  const probe = qualify.steps.find((step: any) => step.env?.ARCH)
  expect(probe.run).toContain('scripts/nix_cli_publication.py probe-hosts')
  expect(probe.run).toContain('--archive-identity "cli-candidates/$ARCH/archive-identity.json"')
  expect(probe.run).toContain('--source-revision "$(git rev-parse HEAD)"')
  expect(probe.run).toContain('--evidence-dir .tmp/nix-cli-evidence')
  expect(probe.run).not.toContain('tar -')
  expect(probe.run).not.toContain('docker run')
  expect(qualify.name).toBe('Linux host controller matrix (${{ matrix.arch }})')
  const retention = qualify.steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(retention.with['retention-days']).toBe(14)
  expect(retention.with['if-no-files-found']).toBe('warn')
})

test('Nix development retains clean-source Compose controllers in a separate candidate artifact', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const build = workflow.jobs['cli-build']
  const buildStep = build.steps.find((step: any) => step.name === 'Build both controller architectures without undeclared network access')
  for (const output of ['.#leapviewctl-compose-linux-amd64', '.#leapviewctl-compose-linux-arm64']) {
    expect(buildStep.run).toContain(`nix build --no-update-lock-file ${output}`)
  }
  expect(buildStep.run).toContain('result-compose-cli-amd64')
  expect(buildStep.run).toContain('result-compose-cli-$arch')
  for (const file of ['leapviewctl-linux-$arch.tar.gz', 'archive-identity.json', 'controller-build-identity.json', 'static-compatibility.json']) {
    expect(buildStep.run).toContain(file)
  }
  expect(buildStep.run).toContain('result-compose-cli-$arch/bin/leapviewctl compose-candidates/$arch/bin/')

  const uploads = build.steps.filter((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(uploads).toHaveLength(2)
  expect(uploads[0].id).toBe('upload')
  expect(uploads[0].with.name).toBe('nix-cli-candidates-${{ github.run_id }}-${{ github.run_attempt }}')
  expect(uploads[0].with.path).toBe('cli-candidates/')
  expect(uploads[1].name).toBe('Retain separate Compose controller candidates and static reports')
  expect(uploads[1].with.name).toBe('nix-compose-cli-candidates-${{ github.run_id }}-${{ github.run_attempt }}')
  expect(uploads[1].with.path).toBe('compose-candidates/')
  expect(uploads[1].with['retention-days']).toBe(14)
  expect(uploads[1].with['if-no-files-found']).toBe('error')
  expect(build.outputs.artifact_id).toBe('${{ steps.upload.outputs.artifact-id }}')

  for (const jobName of ['cli-security', 'cli-compatibility']) {
    const consumer = workflow.jobs[jobName]
    const download = consumer.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
    expect(download.with['artifact-ids']).toBe('${{ needs.cli-build.outputs.artifact_id }}')
    expect(download.with.name).toBeUndefined()
  }
})

test('controller fixtures preserve the client baseline and advertised bootstrap hosts', () => {
  const result = spawnSync('python3', ['-c',
    'import json,sys; sys.path.insert(0,"scripts"); import nix_cli_publication as p; print(json.dumps(p.HOST_FIXTURES))'],
  { encoding: 'utf8' })
  if (result.status !== 0) throw new Error(result.stdout + result.stderr)
  const fixtures = JSON.parse(result.stdout)
  const baseline = readFileSync('deploy/compose/qualification/Dockerfile.authoring-client', 'utf8')
    .match(/^FROM (debian:bookworm-slim@sha256:[a-f0-9]{64})$/m)![1]
  expect(fixtures[0]).toMatchObject({ id: 'debian12', image: baseline, osID: 'debian', versionID: '12' })
  const bootstrap = readFileSync('deploy/host/bootstrap-linux.sh', 'utf8')
  const advertised = Array.from(bootstrap.matchAll(/^\s+(ubuntu|debian):([\d.]+)\)/gm), match => `${match[1]}:${match[2]}`)
  expect(fixtures.slice(1).map((fixture: any) => `${fixture.osID}:${fixture.versionID}`)).toEqual(advertised)
  expect(fixtures).toHaveLength(3)
  for (const fixture of fixtures) expect(fixture.image).toMatch(/@sha256:[a-f0-9]{64}$/)
})

test('Linux validation selects locked tools and excludes duplicate installers', () => {
  expect(action.inputs.toolchain.default).toBe('auto')
  const install = steps.find((step: any) => step.name === 'Install locked Nix')
  expect(install.if).toBe(locked)
  expect(install.uses).toMatch(/@[a-f0-9]{40}$/)
  expect(install.with.extra_nix_config).toContain('sandbox = true')
  const environment = steps.find((step: any) => step.name === 'Export locked compiler and browser environment')
  expect(environment.if).toBe(`inputs.profile == 'validation' && ${locked}`)
  expect(environment.env).toEqual({
    CI_PROFILE: '${{ inputs.profile }}',
    NIX_SHELL: "${{ inputs.profile == 'orchestration' && 'orchestration' || 'default' }}",
  })
  expect(environment.run).toBe('nix develop --no-update-lock-file ".#${NIX_SHELL}" -c python3 scripts/export_nix_ci_environment.py --profile "$CI_PROFILE"')
  for (const name of ['Set up Go', 'Set up Node.js', 'Set up Bun', 'Install pinned CI tools']) {
    expect(steps.find((step: any) => step.name === name).if)
      .toBe(name === 'Set up Go' ? conventional : `inputs.profile == 'validation' && (${conventional})`)
  }
  expect(steps.find((step: any) => step.name === 'Configure bounded tool caches').run)
    .not.toContain('PLAYWRIGHT_BROWSERS_PATH=')
})

test('locked browser validation cannot fall back to runner browser installs', () => {
  const smoke = steps.find((step: any) => step.name === 'Verify locked Chromium and compiler pairing')
  expect(smoke.if).toBe(`inputs.profile == 'validation' && (inputs.browser == 'true' && ${locked})`)
  expect(smoke.run).toBe('task nix:smoke')
  for (const name of ['Restore Playwright browser cache', 'Install cached Chromium and system dependencies']) {
    expect(steps.find((step: any) => step.name === name).if)
      .toBe(`inputs.profile == 'validation' && (inputs.browser == 'true' && (${conventional}))`)
  }
})

test('all PR, merge and nightly validation jobs use the shared toolchain', () => {
  for (const workflow of ['ci', 'merge-validation', 'nightly']) {
    const config = parse(readFileSync(`.github/workflows/${workflow}.yml`, 'utf8'))
    for (const [name, job] of Object.entries(config.jobs) as [string, any][]) {
      if (!job.steps?.some((step: any) => /\b(go run|task ci:|task generated:check)\b/.test(step.run ?? ''))) continue
      const setup = job.steps.filter((step: any) => step.uses === './.github/actions/setup-ci')
      expect(setup.length, `${workflow}/${name}`).toBe(1)
      expect(setup[0].with?.toolchain).not.toBe('conventional')
    }
  }
})

test('published native binaries retain the conventional builder until artifact qualification', () => {
  const release = parse(readFileSync('.github/workflows/release.yml', 'utf8'))
  for (const job of Object.values(release.jobs) as any[]) {
    for (const step of job.steps ?? []) {
      if (step.uses === './.github/actions/setup-ci') expect(step.with.toolchain).toBe('conventional')
    }
  }
})

test('release image aliases are published only after qualification', () => {
  const release = parse(readFileSync('.github/workflows/release.yml', 'utf8'))
  const metadata = release.jobs.image.steps.find((step: any) => step.name === 'Compute image metadata')
  const candidateTag = 'type=raw,value=candidate-${{ github.run_id }}-${{ github.run_attempt }}'
  const nativeImage = release.jobs['image-platform'].steps.find((step: any) => step.name === 'Build and publish native image')
  expect(release.on.workflow_dispatch?.inputs?.image_tag).toBeUndefined()
  expect(nativeImage.with.tags).toBe('${{ env.IMAGE_NAME }}:candidate-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.arch }}')
  expect(metadata.with.tags.trim().split(/\r?\n/)).toEqual([candidateTag])

  const publish = release.jobs.publish
  expect(publish.if).toBe("github.event_name == 'push'")
  expect(publish.needs).toEqual(['image', 'authoring-cli', 'qualify', 'minio-conformance', 'plan-gc-conformance'])
  expect(publish.concurrency).toEqual({
    group: 'release-image-promotion',
    'cancel-in-progress': false,
    queue: 'max',
  })
  const aliases = publish.steps.find((step: any) => step.name === 'Publish qualified image tags')
  expect(aliases.env.IMAGE_DIGEST).toBe('${{ needs.image.outputs.image_digest }}')
  expect(aliases.run).toContain('scripts/release_image_promotion.py')
  expect(aliases.run).toContain('--candidate-reference "$IMAGE_REFERENCE"')
  expect(publish.steps.find((step: any) => step.name === 'Verify immutable release version tag')).toBeDefined()
  expect(publish.steps.findIndex((step: any) => step.name === 'Publish qualified image tags'))
    .toBeLessThan(publish.steps.findIndex((step: any) => step.name === 'Publish GitHub release'))
})

test('cache fallbacks retain the toolchain and locked compiler input identity', () => {
  const cache = steps.find((step: any) => step.name === 'Restore candidate Go validation cache')
  expect(cache.uses).toContain('actions/cache/restore@')
  for (const field of ['key', 'restore-keys']) {
    expect(cache.with[field]).toContain('${{ inputs.toolchain }}')
    expect(cache.with[field]).toContain("${{ hashFiles('flake.lock', 'nix/toolchain.nix') }}")
    expect(cache.with[field]).toContain('${{ steps.toolchain.outputs.go-version }}')
  }
})

test('fresh full and nightly qualification provision the deployment Terraform dependency', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const development = workflow.jobs.development.steps
  const terraform = development.find((step: any) => step.uses?.startsWith('hashicorp/setup-terraform@'))
  const canonical = steps.find((step: any) => step.name === 'Set up Terraform')
  expect(terraform).toBeDefined()
  expect(terraform.uses).toBe(canonical.uses)
  expect(terraform.with).toEqual(canonical.with)
  expect(terraform.if).toBe("inputs.contract == 'full' || inputs.contract == 'nightly'")
  const contractIndex = development.findIndex((step: any) => step.env?.CONTRACT)
  expect(development.indexOf(terraform)).toBeLessThan(contractIndex)
})

test('Nix candidate collection follows enforcement and retains all bound evidence', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const image = workflow.jobs.image.steps
  const enforcement = image.findIndex((step: any) => step.run?.includes('check_nix_runtime_security.py result-image'))
  const collection = image.findIndex((step: any) => step.run?.includes('nix_candidate_manifest.py result-image'))
  expect(enforcement).toBeGreaterThan(-1)
  expect(collection).toBeGreaterThan(enforcement)
  expect(image[collection].if).toBeUndefined() // The default success gate rejects incomplete scans.
  expect(image[collection].run).toContain('--kind application-image')
  expect(image[collection].run).toContain('--verify .tmp/nix-runtime-security/candidate-manifest.json')
  const binding = image.findIndex((step: any) => step.run?.includes('scripts/nix_oci_content.py'))
  expect(binding).toBeGreaterThan(collection)
  expect(image[binding].if).toBeUndefined()
  expect(image[binding].run).toContain('scripts/nix_oci_content.py export')
  expect(image[binding].run).toContain('skopeo copy --preserve-digests oci:.tmp/nix-exported-oci:candidate')
  expect(image[binding].run).toContain('open(".tmp/nix-exported-oci/index.json")')
  expect(image[binding].run).toContain('--platform linux/amd64 --kind application-image')
  expect(image[binding].run).toContain('--candidate result-image .tmp/nix-runtime-security/candidate-manifest.json .tmp/nix-runtime-security')
  expect(image[binding].run).toContain('--verify .tmp/nix-runtime-security/oci-content-binding.json')
  const retention = image.find((step: any) => step.with?.name?.startsWith('nix-runtime-security-'))
  expect(retention.if).toBe('always()')
  for (const name of ['candidate-manifest', 'oci-content-binding', 'summary', 'sbom.syft', 'sbom.spdx', 'runtime.syft',
    'runtime.grype', 'runtime.assessed.grype', 'controls.synthetic.syft', 'controls.grype',
    'assessments.vex', 'syft-config', 'grype-config']) {
    expect(retention.with.path).toContain(`.tmp/nix-runtime-security/${name}.json`)
  }
  expect(workflow.permissions).toEqual({ contents: 'read' })
})

test('protected Nix candidates isolate build, qualification and signing authority', () => {
  const config = parse(readFileSync('.github/workflows/nix-candidate.yml', 'utf8'))
  expect(Object.keys(config.on)).toEqual(['workflow_dispatch'])
  expect(config.permissions).toEqual({ contents: 'read' })
  expect(config.concurrency['cancel-in-progress']).toBe(false)
  for (const job of Object.values(config.jobs) as any[]) {
    expect(job.if).toContain("github.repository == 'flidai/leapview'")
    expect(job.if).toContain("github.ref == 'refs/heads/main'")
    for (const step of job.steps) {
      if (step.uses?.startsWith('actions/checkout@')) expect(step.with['persist-credentials']).toBe(false)
      if (step.uses) expect(step.uses).toMatch(/@[a-f0-9]{40}$/)
    }
  }
  const { authorize, build, qualify, publish } = config.jobs
  expect(build.permissions).toEqual({ contents: 'read' })
  expect(qualify.permissions).toEqual({ contents: 'read', actions: 'read' })
  expect(build.needs).toBe('authorize')
  expect(qualify.needs).toEqual(['authorize', 'build'])
  expect(publish.needs).toEqual(['authorize', 'qualify'])
  expect(publish.permissions['id-token']).toBe('write')
  expect(publish.permissions.packages).toBe('write')
  expect(publish.environment).toBe(authorize.environment)
  for (const job of [qualify, publish]) {
    const protectedCheckout = job.steps.find((s: any) => s.with?.path === 'protected')
    expect(protectedCheckout.with.ref).toBe('${{ github.sha }}')
    expect(job.steps.find((s: any) => s.with?.path === 'source').with.ref).toBe('${{ inputs.source_revision }}')
    const commands = job.steps.map((s: any) => s.run ?? '').join('\n')
    expect(commands).not.toMatch(/\b(?:python3|bash|nix develop|nix build) source\//)
    expect(commands).not.toContain('cd source')
  }
  const scanner = qualify.steps.find((s: any) => s.run?.includes('check_nix_runtime_security.py'))
  expect(scanner['working-directory']).toBe('protected')
  expect(scanner.run).toContain('../candidate/image.tar')
  expect(qualify.steps.find((s: any) => s.run?.includes('check_nix_image.sh')).run)
    .toContain('protected/scripts/check_nix_image.sh candidate/image.tar trusted-app')
  expect(publish.steps.find((s: any) => s.uses?.startsWith('actions/download-artifact@')).with['artifact-ids'])
    .toBe('${{ steps.artifacts.outputs.qualified_id }}')
  for (const job of [build, qualify, publish]) {
    expect(job.strategy.matrix.include.map((entry: any) => [entry.arch, entry.runner]))
      .toEqual([['amd64', 'ubuntu-24.04'], ['arm64', 'ubuntu-24.04-arm']])
    expect(job.outputs).toBeUndefined()
  }
})

test('protected producer preserves current-head authorization and signs the bound SPDX', () => {
  const { jobs } = parse(readFileSync('.github/workflows/nix-candidate.yml', 'utf8'))
  for (const job of [jobs.authorize, jobs.publish]) {
    const guard = job.steps.find((s: any) => s.run?.includes('/pulls'))
    expect(guard.run).toContain('.base.ref == "main" and .state == "open" and .head.sha == $revision')
    expect(guard.run).toContain('length == 1')
    expect(guard.run).toContain('^[0-9a-f]{40}$')
  }
  const steps = jobs.publish.steps
  const publication = steps.findIndex((s: any) => s.id === 'publish')
  const signatures = steps.filter((s: any) => s.uses?.startsWith('actions/attest@'))
  expect(signatures.length).toBe(2)
  for (const signature of signatures) {
    expect(steps.indexOf(signature)).toBeGreaterThan(publication)
    expect(signature.with['subject-digest']).toBe('${{ steps.publish.outputs.digest }}')
    expect(signature.with['subject-name']).toBe('ghcr.io/flidai/leapview')
    expect(signature.with['push-to-registry']).toBe(true)
    expect(signature.with['create-storage-record']).toBe(false)
  }
  expect(signatures[1].with['predicate-type']).toBe('https://spdx.dev/Document/v2.3')
  expect(signatures[1].with['predicate-path']).toBe('candidate/runtime/sbom.spdx.json')
  const verification = steps.find((s: any) => s.run?.includes('verify-signed'))
  expect(steps.indexOf(verification)).toBeGreaterThan(steps.indexOf(signatures[1]))
  expect(verification.run).toContain('--signer-revision "$GITHUB_SHA"')
  const qualifier = readFileSync('scripts/check_nix_image.sh', 'utf8')
  expect(qualifier).not.toContain('nix build')
  expect(qualifier).toContain('test "$#" = 2')
  expect(qualifier).toContain('^leapview-nix:[0-9a-f]{12}$')
  expect(qualifier).toContain('read -r image_platform image extra <<< "$identity"')
  const tasks = parse(readFileSync('Taskfile.yml', 'utf8'))
  expect(tasks.tasks['nix:qualify'].cmds.slice(0, 2)).toEqual([{ task: 'nix:build' }, { task: 'nix:image' }])
})

test('protected producer scans Go binaries only in qualification and reverifies them before publication', () => {
  const { jobs } = parse(readFileSync('.github/workflows/nix-candidate.yml', 'utf8'))
  const steps = jobs.qualify.steps
  const runtime = steps.findIndex((step: any) => step.run?.includes('check_nix_runtime_security.py'))
  const go = steps.findIndex((step: any) => step.run?.includes('nix_archive_go_evidence.py'))
  const binding = steps.findIndex((step: any) => step.run?.includes('nix_candidate_publication.py record'))
  expect(go).toBeGreaterThan(runtime)
  expect(binding).toBeGreaterThan(go)
  expect(steps[go].run).toContain('protected/scripts/nix_archive_go_evidence.py candidate/image.tar')
  expect(steps[go].run).toContain('--evidence-dir candidate/runtime/go')
  for (const job of [jobs.qualify, jobs.publish]) {
    const compile = job.steps.find((step: any) => step.run?.includes('go build'))
    expect(compile['working-directory']).toBe('protected')
    expect(compile.run).toContain('./internal/app/tools/securitydependencies')
    for (const step of job.steps.filter((step: any) => step.run?.includes('nix_candidate_publication.py'))) {
      expect(step.run).toContain('--binary-verifier "$RUNNER_TEMP/go-binary-verifier"')
    }
  }
  const commands = jobs.publish.steps.map((step: any) => step.run ?? '').join('\n')
  expect(commands).not.toContain('nix_archive_go_evidence.py')
  expect(commands).not.toContain('govulncheck')
  expect(commands).not.toContain('nix build')
})

test('published qualification verifies the final signed digest in a separate read-only job', () => {
  const { jobs } = parse(readFileSync('.github/workflows/nix-candidate.yml', 'utf8'))
  const final = jobs['qualify-published']
  expect(final.needs).toEqual(['qualify', 'publish'])
  expect(final.permissions).toEqual({ contents: 'read', packages: 'read', actions: 'read' })
  expect(final.environment).toBeUndefined()
  expect(jobs.publish.outputs).toBeUndefined()
  const downloads = final.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads.map((step: any) => step.with['artifact-ids']))
    .toEqual(['${{ steps.artifacts.outputs.qualified_id }}', '${{ steps.artifacts.outputs.binding_id }}'])
  const verify = final.steps.findIndex((step: any) => step.run?.includes('nix_candidate_publication.py verify-signed'))
  const run = final.steps.findIndex((step: any) => step.run?.includes('check_nix_registry_image.sh'))
  const bind = final.steps.findIndex((step: any) => step.run?.includes('bind-qualified'))
  expect(verify).toBeGreaterThan(-1)
  expect(run).toBeGreaterThan(verify)
  expect(bind).toBeGreaterThan(run)
  for (const index of [verify, run, bind]) {
    expect(final.steps[index].env.IMAGE).toBe('${{ steps.image.outputs.image }}')
    expect(final.steps[index].if).toBeUndefined()
  }
  expect(final.steps[bind].run).toContain('--signed-evidence bindings/nix-signed.json')
  expect(final.steps[bind].run).toContain('--qualification-report candidate/final/image-qualification-report.json')
  const retain = final.steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(retain.if).toBe('always()')
  expect(retain.with.path).toBe('candidate/final/')
})

test('registry image qualification retains the immutable reference and stops on pull failure for native Linux', () => {
  const root = mkdtempSync(join(tmpdir(), 'nix-published-image-'))
  const script = join(root, 'scripts', 'check_nix_registry_image.sh')
  const calls = join(root, 'calls')
  const image = `ghcr.io/flidai/leapview@sha256:${'a'.repeat(64)}`
  try {
    mkdirSync(join(root, 'scripts'))
    mkdirSync(join(root, 'bin'))
    mkdirSync(join(root, 'application', 'bin'), { recursive: true })
    copyFileSync(resolve('scripts/check_nix_registry_image.sh'), script)
    writeFileSync(join(root, 'bin', 'uname'), '#!/bin/sh\nprintf "%s\\n" "$RUNNER_MACHINE"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'docker'), `#!/bin/sh
printf 'docker %s\\n' "$*" >> "$CALLS"
case "$1 $2" in
  'pull --platform') exit "$PULL_STATUS" ;;
  'image inspect') printf '%s\\n' "$INSPECTED_PLATFORM" ;;
  'run --platform') exit 0 ;;
  *) exit 1 ;;
esac
`, { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'cc'), '#!/bin/sh\nprintf "cc %s\\n" "$*" >> "$CALLS"\ntouch "$3"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'patchelf'), '#!/bin/sh\nprintf "patchelf %s\\n" "$*" >> "$CALLS"\n', { mode: 0o755 })
    writeFileSync(join(root, 'application', 'bin', 'leapviewctl'), '#!/bin/sh\nprintf "cli %s\\n" "$*" >> "$CALLS"\n', { mode: 0o755 })
    const invoke = (fixture: { image: string, machine: string, inspectedPlatform: string, pull: string }) => {
      rmSync(calls, { force: true })
      const run = spawnSync('bash', [script, fixture.image, join(root, 'application')], {
        env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}`, CALLS: calls,
          PULL_STATUS: fixture.pull, RUNNER_MACHINE: fixture.machine,
          INSPECTED_PLATFORM: fixture.inspectedPlatform },
      })
      let commands: string[] = []
      try { commands = readFileSync(calls, 'utf8').trim().split('\n') } catch { /* invalid input stops before Docker */ }
      return { run, commands }
    }
    for (const fixture of [
      { machine: 'x86_64', arch: 'amd64', interpreter: '/lib64/ld-linux-x86-64.so.2' },
      { machine: 'aarch64', arch: 'arm64', interpreter: '/lib/ld-linux-aarch64.so.1' },
    ]) {
      const pull = invoke({ image: 'ghcr.io/flidai/leapview:latest', machine: fixture.machine,
        inspectedPlatform: `linux/${fixture.arch}`, pull: '0' })
      expect(pull.run.status).toBe(1)
      expect(pull.commands).toEqual([])

      const failedPull = invoke({ image, machine: fixture.machine,
        inspectedPlatform: `linux/${fixture.arch}`, pull: '17' })
      expect(failedPull.run.status).toBe(17)
      expect(failedPull.commands).toEqual([`docker pull --platform linux/${fixture.arch} ${image}`])

      const foreign = invoke({ image, machine: fixture.machine,
        inspectedPlatform: `linux/${fixture.arch === 'arm64' ? 'amd64' : 'arm64'}`, pull: '0' })
      expect(foreign.run.status).toBe(1)
      expect(foreign.commands).toEqual([
        `docker pull --platform linux/${fixture.arch} ${image}`,
        `docker image inspect ${image} --format {{.Os}}/{{.Architecture}}`,
      ])
      expect(foreign.commands.join('\n')).not.toMatch(/\b(?:cc|patchelf|run|cli)\b/)

      const qualified = invoke({ image, machine: fixture.machine,
        inspectedPlatform: `linux/${fixture.arch}`, pull: '0' })
      expect(qualified.run.status).toBe(0)
      expect(qualified.commands).toEqual([
        `docker pull --platform linux/${fixture.arch} ${image}`,
        `docker image inspect ${image} --format {{.Os}}/{{.Architecture}}`,
        'cc scripts/testdata/nix/strfmon_probe.c -o ' + join(root, '.tmp/nix-published-qualification/strfmon_probe'),
        `patchelf --no-sort --set-interpreter ${fixture.interpreter} --remove-rpath ${join(root, '.tmp/nix-published-qualification/strfmon_probe')}`,
        `docker run --platform linux/${fixture.arch} --rm --network none --read-only --cap-drop ALL --volume ${join(root, '.tmp/nix-published-qualification/strfmon_probe')}:/tmp/strfmon_probe:ro --entrypoint /tmp/strfmon_probe ${image}`,
        `cli qualify image --image ${image} --require-immutable --evidence-dir ${join(root, '.tmp/nix-published-qualification/evidence')}`,
      ])
      expect(qualified.commands.join('\n')).not.toContain('docker push')
      expect(qualified.commands.join('\n')).not.toContain('docker tag')
    }
    const unsupported = invoke({ image, machine: 'riscv64', inspectedPlatform: 'linux/amd64', pull: '0' })
    expect(unsupported.run.status).toBe(1)
    expect(unsupported.commands).toEqual([])
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('protected Nix controller candidates keep build, qualification, signing, and verification isolated', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-cli-candidate.yml', 'utf8'))
  expect(Object.keys(workflow.on)).toEqual(['workflow_dispatch'])
  expect(workflow.on.workflow_dispatch.inputs.source_revision.required).toBe(true)
  expect(workflow.permissions).toEqual({ contents: 'read' })
  expect(workflow.concurrency['cancel-in-progress']).toBe(false)
  for (const [name, job] of Object.entries(workflow.jobs) as [string, any][]) {
    expect(job.if, name).toContain("github.repository == 'flidai/leapview'")
    expect(job.if, name).toContain("github.ref == 'refs/heads/main'")
    for (const step of job.steps) {
      if (step.uses?.startsWith('actions/checkout@')) expect(step.with['persist-credentials'], name).toBe(false)
      if (step.uses) expect(step.uses, name).toMatch(/@[a-f0-9]{40}$/)
    }
  }

  const { authorize, build, 'qualify-amd64': amd64, 'qualify-arm64': arm64, publish, 'verify-signed': verify } = workflow.jobs
  expect(authorize.environment).toBe('leapview-ephemeral-qualification')
  expect(authorize.permissions).toEqual({ contents: 'read', 'pull-requests': 'read' })
  const authorization = authorize.steps.find((step: any) => step.run?.includes('/pulls'))
  expect(authorization.run).toContain('^[0-9a-f]{40}$')
  expect(authorization.run).toContain('.base.ref == "main" and .state == "open" and .head.sha == $revision')
  expect(authorization.run).toContain('length == 1')

  expect(build.needs).toBe('authorize')
  expect(build.outputs.artifact_id).toBe('${{ steps.archive.outputs.artifact-id }}')
  expect(build.permissions).toEqual({ contents: 'read' })
  expect(build.environment).toBeUndefined()
  const buildStep = build.steps.find((step: any) => step.name === 'Build both controller archives without signing credentials')
  for (const target of ['.#leapviewctl-linux-amd64', '.#leapviewctl-linux-arm64']) expect(buildStep.run).toContain(target)
  for (const file of ['leapviewctl-linux-$arch.tar.gz', 'archive-identity.json', 'static-compatibility.json']) {
    expect(buildStep.run).toContain(file)
  }
  const buildArtifact = build.steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(buildArtifact.with.path).toBe('candidate/')
  expect(buildArtifact.with['retention-days']).toBe(14)

  for (const [arch, job, runner] of [['amd64', amd64, 'ubuntu-24.04'], ['arm64', arm64, 'ubuntu-24.04-arm']] as [string, any, string][]) {
    expect(job.needs).toEqual(['authorize', 'build'])
    expect(job['runs-on']).toBe(runner)
    expect(job.permissions).toEqual({ contents: 'read' })
    expect(job.environment).toBeUndefined()
    expect(job.outputs.artifact_id).toBe('${{ steps.qualified.outputs.artifact-id }}')
    const protectedCheckout = job.steps.find((step: any) => step.with?.path === 'protected')
    const sourceCheckout = job.steps.find((step: any) => step.with?.path === 'source')
    expect(protectedCheckout.with.ref).toBe('${{ github.sha }}')
    expect(sourceCheckout.with.ref).toBe('${{ inputs.source_revision }}')
    const download = job.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
    expect(download.with['artifact-ids']).toBe('${{ needs.build.outputs.artifact_id }}')
    const qualifyIndex = job.steps.findIndex((step: any) => step.run?.includes('nix_cli_publication.py qualify'))
    const verifyIndex = job.steps.findIndex((step: any) => step.run?.includes('nix_cli_publication.py verify'))
    const uploadIndex = job.steps.findIndex((step: any) => step.id === 'qualified')
    expect(qualifyIndex).toBeGreaterThan(-1)
    expect(verifyIndex).toBeGreaterThan(qualifyIndex)
    expect(uploadIndex).toBeGreaterThan(verifyIndex)
    const qualification = job.steps[qualifyIndex].run
    expect(qualification).toContain(`candidate/${arch}/leapviewctl-linux-${arch}.tar.gz`)
    expect(qualification).toContain(`--source-root source --source-revision "$SOURCE_REVISION"`)
    expect(qualification).toContain(`--evidence-dir qualified/${arch}/evidence`)
    expect(job.steps[verifyIndex].run).toContain(`qualified/${arch}/archive-identity.json`)
    const commands = job.steps.map((step: any) => step.run ?? '').join('\n')
    expect(commands).not.toMatch(/\b(?:python3|bash|nix develop|nix build) source\//)
    expect(commands).not.toContain('cd source')
    expect(commands).not.toContain('nix build')
    const diagnostics = job.steps.find((step: any) => step.name === `Retain ${arch} diagnostics`)
    expect(diagnostics.if).toBe('always()')
    expect(diagnostics.with['retention-days']).toBe(14)
  }

  expect(publish.needs).toEqual(['authorize', 'qualify-amd64', 'qualify-arm64'])
  expect(publish.outputs.artifact_id).toBe('${{ steps.signed.outputs.artifact-id }}')
  expect(publish.environment).toBe(authorize.environment)
  expect(publish.permissions).toEqual({
    contents: 'read', 'pull-requests': 'read', attestations: 'write', 'id-token': 'write',
  })
  const protectedCheckout = publish.steps.find((step: any) => step.with?.path === 'protected')
  const sourceCheckout = publish.steps.find((step: any) => step.with?.path === 'source')
  expect(protectedCheckout.with.ref).toBe('${{ github.sha }}')
  expect(sourceCheckout.with.ref).toBe('${{ inputs.source_revision }}')
  const downloads = publish.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads.map((step: any) => step.with['artifact-ids'])).toEqual([
    '${{ needs.qualify-amd64.outputs.artifact_id }}',
    '${{ needs.qualify-arm64.outputs.artifact_id }}',
  ])
  const publisherGuard = publish.steps.findIndex((step: any) => step.run?.includes('/pulls'))
  const attestations = publish.steps.filter((step: any) => step.uses?.startsWith('actions/attest@'))
  expect(publisherGuard).toBeGreaterThan(-1)
  expect(attestations).toHaveLength(4)
  expect(publish.steps.indexOf(attestations[0])).toBe(publisherGuard + 1)
  for (const [index, arch] of ['amd64', 'arm64'].entries()) {
    const provenance = attestations[index * 2]
    const spdx = attestations[index * 2 + 1]
    const archive = `qualified/${arch}/leapviewctl-linux-${arch}.tar.gz`
    expect(provenance.with['subject-path']).toBe(archive)
    expect(provenance.with['predicate-path']).toBeUndefined()
    expect(spdx.with['subject-path']).toBe(archive)
    expect(spdx.with['predicate-type']).toBe('https://spdx.dev/Document/v2.3')
    expect(spdx.with['predicate-path']).toBe(`qualified/${arch}/evidence/sbom.spdx.json`)
  }
  expect(publish.permissions.packages).toBeUndefined()
  const signedArtifact = publish.steps.find((step: any) => step.id === 'signed')
  expect(signedArtifact.with.path).toContain('qualified/amd64/')
  expect(signedArtifact.with.path).toContain('qualified/arm64/')
  expect(signedArtifact.with['retention-days']).toBe(14)

  expect(verify.needs).toEqual(['authorize', 'publish'])
  expect(verify.permissions).toEqual({ contents: 'read', attestations: 'read' })
  expect(verify.environment).toBeUndefined()
  expect(verify.permissions['id-token']).toBeUndefined()
  expect(Object.values(verify.permissions).some((value: any) => value === 'write')).toBe(false)
  const verifyDownloads = verify.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(verifyDownloads).toHaveLength(1)
  expect(verifyDownloads[0].with['artifact-ids']).toBe('${{ needs.publish.outputs.artifact_id }}')
  const signedChecks = verify.steps.filter((step: any) => step.run?.includes('nix_cli_publication.py verify-signed'))
  expect(signedChecks).toHaveLength(2)
  for (const [index, arch] of ['amd64', 'arm64'].entries()) {
    expect(signedChecks[index].run).toContain(`qualified/${arch}/leapviewctl-linux-${arch}.tar.gz`)
    expect(signedChecks[index].run).toContain(`qualified/${arch}/evidence`)
    expect(signedChecks[index].run).toContain('--signer-revision "$GITHUB_SHA"')
  }
  const signedRetention = verify.steps.find((step: any) => step.name === 'Retain independently verified signed evidence')
  expect(signedRetention.if).toBe('always()')
  expect(signedRetention.with['retention-days']).toBe(14)

  const development = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  for (const path of ['scripts/nix_cli_publication.py', 'scripts/tests/test_nix_cli_publication.py',
    '.github/workflows/nix-cli-candidate.yml']) expect(development.on.pull_request.paths).toContain(path)
})

test('Nix development evaluates the native ARM runtime-security shell without building ARM packages', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const steps = workflow.jobs.development.steps
  const flakeCheck = steps.findIndex((step: any) => step.name === 'Check the pinned toolchain and native compiler')
  const armShell = steps.findIndex((step: any) => step.name === 'Check the native ARM runtime-security shell')
  const format = steps.findIndex((step: any) => step.name === 'Check formatting')
  expect(steps[armShell].run)
    .toBe('test "$(nix eval --raw --no-update-lock-file .#devShells.aarch64-linux.runtime-security.system)" = aarch64-linux')
  expect(armShell).toBe(flakeCheck + 1)
  expect(armShell).toBeLessThan(format)
})

test('image qualification rejects fixture tags and checks native platform before the Docker image ID probe', () => {
  const root = mkdtempSync(join(tmpdir(), 'nix-image-import-'))
  const script = join(root, 'scripts', 'check_nix_image.sh')
  const application = join(root, 'application')
  const archive = join(root, 'image.tar')
  const calls = join(root, 'docker-calls')
  const imageID = `sha256:${'b'.repeat(64)}`
  const registryDigest = `sha256:${'c'.repeat(64)}`
  const registryReference = `127.0.0.1:5000/leapview@${registryDigest}`
  try {
    mkdirSync(join(root, 'scripts'))
    mkdirSync(join(root, 'bin'))
    mkdirSync(join(application, 'bin'), { recursive: true })
    mkdirSync(join(application, 'share', 'leapview', 'deploy', 'compose'), { recursive: true })
    copyFileSync(resolve('scripts/check_nix_image.sh'), script)
    writeFileSync(join(root, 'bin', 'uname'), '#!/bin/sh\nprintf "%s\\n" "$RUNNER_MACHINE"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'docker'), `#!/bin/sh
printf '%s\\n' "$*" >> '${calls}'
case "$1" in
  load) exit 0 ;;
  image)
    if [ "$2" = inspect ] && [ "$5" = '{{json .RepoDigests}}' ]; then
      printf '["%s"]\\n' "$REGISTRY_REFERENCE"
    elif [ "$2" = inspect ]; then
      printf '%s %s\\n' "$INSPECTED_PLATFORM" '${imageID}'
    else exit 0
    fi ;;
  run)
    if [ "$2" = --platform ]; then exit 0; fi
    printf '%s\\n' registry-container ;;
  port) printf '%s\\n' '127.0.0.1:5000' ;;
  tag|push|rm) exit 0 ;;
  *) exit 1 ;;
esac
`, { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'cc'), '#!/bin/sh\nprintf "cc %s\\n" "$*" >> "' + calls + '"\ntouch "$3"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'patchelf'), '#!/bin/sh\nprintf "patchelf %s\\n" "$*" >> "' + calls + '"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'curl'), '#!/bin/sh\nprintf "curl %s\\n" "$*" >> "' + calls + '"\nexit 0\n', { mode: 0o755 })
    writeFileSync(join(application, 'bin', 'leapviewctl'), '#!/bin/sh\nprintf "cli %s\\n" "$*" >> "$CALLS"\n', { mode: 0o755 })
    writeFileSync(join(root, 'manifest.json'), JSON.stringify([{ Config: `${'a'.repeat(64)}.json`, RepoTags: ['leapview-nix:abcdef123456'] }]))
    expect(spawnSync('tar', ['-cf', archive, '-C', root, 'manifest.json']).status).toBe(0)
    const invoke = (machine: string, inspectedPlatform: string) => {
      rmSync(calls, { force: true })
      const run = spawnSync('bash', [script, archive, application], {
        env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}`,
          RUNNER_MACHINE: machine, INSPECTED_PLATFORM: inspectedPlatform, REGISTRY_REFERENCE: registryReference,
          CALLS: calls },
      })
      let commands: string[] = []
      try { commands = readFileSync(calls, 'utf8').trim().split('\n') } catch { /* qualification stops before Docker */ }
      return { run, commands }
    }
    for (const fixture of [
      { machine: 'x86_64', arch: 'amd64', interpreter: '/lib64/ld-linux-x86-64.so.2' },
      { machine: 'aarch64', arch: 'arm64', interpreter: '/lib/ld-linux-aarch64.so.1' },
    ]) {
      const foreign = invoke(fixture.machine, `linux/${fixture.arch === 'arm64' ? 'amd64' : 'arm64'}`)
      expect(foreign.run.status).toBe(1)
      expect(foreign.commands).toEqual([
        'load --input ' + archive,
        `image inspect leapview-nix:abcdef123456 --format {{.Os}}/{{.Architecture}} {{.Id}}`,
      ])
      expect(foreign.commands.join('\n')).not.toMatch(/\b(?:cc|patchelf|run)\b/)

      const native = invoke(fixture.machine, `linux/${fixture.arch}`)
      expect(native.run.status).toBe(0)
      expect(native.commands[0]).toBe('load --input ' + archive)
      expect(native.commands[1]).toBe(
        'image inspect leapview-nix:abcdef123456 --format {{.Os}}/{{.Architecture}} {{.Id}}')
      expect(native.commands[2]).toBe(
        `cc scripts/testdata/nix/strfmon_probe.c -o ${join(root, '.tmp/nix-image-qualification/strfmon_probe')}`)
      expect(native.commands[3]).toBe(
        `patchelf --no-sort --set-interpreter ${fixture.interpreter} --remove-rpath ${join(root, '.tmp/nix-image-qualification/strfmon_probe')}`)
      expect(native.commands[4]).toBe(
        `run --platform linux/${fixture.arch} --rm --network none --read-only --cap-drop ALL --volume ${join(root, '.tmp/nix-image-qualification/strfmon_probe')}:/tmp/strfmon_probe:ro --entrypoint /tmp/strfmon_probe ${imageID}`)
      expect(native.commands[4]).not.toContain(`sha256:${'a'.repeat(64)}`)
      expect(native.commands).toContain(`tag ${imageID} 127.0.0.1:5000/leapview:nix`)
      expect(native.commands).toContain('push 127.0.0.1:5000/leapview:nix')
      expect(native.commands).toContain(`image inspect 127.0.0.1:5000/leapview:nix --format {{json .RepoDigests}}`)
      expect(native.commands).toContain(
        `cli qualify image --image 127.0.0.1:5000/leapview@${registryDigest} --require-immutable --evidence-dir ${join(root, '.tmp/nix-image-qualification/evidence')}`)
    }
    const invalidManifest = JSON.stringify([{ Config: `${'a'.repeat(64)}.json`, RepoTags: ['postgres:18'] }])
    writeFileSync(join(root, 'manifest.json'), invalidManifest)
    expect(spawnSync('tar', ['-cf', archive, '-C', root, 'manifest.json']).status).toBe(0)
    const invalid = invoke('x86_64', 'linux/amd64')
    expect(invalid.run.status).toBe(1)
    expect(invalid.commands).toEqual([])

    writeFileSync(join(root, 'manifest.json'), JSON.stringify([{ Config: `${'a'.repeat(64)}.json`, RepoTags: ['leapview-nix:abcdef123456'] }]))
    expect(spawnSync('tar', ['-cf', archive, '-C', root, 'manifest.json']).status).toBe(0)
    const unsupported = invoke('riscv64', 'linux/amd64')
    expect(unsupported.run.status).toBe(1)
    expect(unsupported.commands).toEqual([])
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
