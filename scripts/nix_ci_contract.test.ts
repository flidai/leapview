import { expect, test } from 'bun:test'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse } from 'yaml'

test('protected controller adversarial checks run in local and hosted CI', () => {
  const command = 'python3 -m unittest discover -s scripts/tests -p test_nix_cli_publication.py'
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain(command)
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  expect(workflow.jobs.image.steps.some((step: any) => step.run?.includes(command))).toBe(true)
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
  const baseline = readFileSync('deploy/compose/qualification/Dockerfile.authoring-client', 'utf8')
    .match(/^FROM (debian:bookworm-slim@sha256:[a-f0-9]{64})$/m)![1]
  expect(probe.run).toContain(`host=${baseline}`)
  expect(probe.run).toContain('--network none --read-only')
  expect(probe.run).toContain('--kind cli-archive')
  expect(probe.run).toContain('--verify .tmp/nix-cli-evidence/candidate-manifest.json')
  expect(probe.run).toContain("'releaseAdmission': False")
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
  expect(qualify.permissions).toEqual({ contents: 'read' })
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
    .toBe('${{ needs.qualify.outputs.artifact_id }}')
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
  expect(qualifier).toContain('image="$(docker image inspect "$reference" --format')
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
  expect(final.permissions).toEqual({ contents: 'read', packages: 'read' })
  expect(final.environment).toBeUndefined()
  expect(jobs.publish.outputs.image).toBe('${{ steps.publish.outputs.image }}')
  expect(jobs.publish.outputs.bindings_artifact_id).toBe('${{ steps.bindings.outputs.artifact-id }}')
  const downloads = final.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads.map((step: any) => step.with['artifact-ids']))
    .toEqual(['${{ needs.qualify.outputs.artifact_id }}', '${{ needs.publish.outputs.bindings_artifact_id }}'])
  const verify = final.steps.findIndex((step: any) => step.run?.includes('nix_candidate_publication.py verify-signed'))
  const run = final.steps.findIndex((step: any) => step.run?.includes('check_nix_registry_image.sh'))
  const bind = final.steps.findIndex((step: any) => step.run?.includes('bind-qualified'))
  expect(verify).toBeGreaterThan(-1)
  expect(run).toBeGreaterThan(verify)
  expect(bind).toBeGreaterThan(run)
  for (const index of [verify, run, bind]) {
    expect(final.steps[index].env.IMAGE).toBe('${{ needs.publish.outputs.image }}')
    expect(final.steps[index].if).toBeUndefined()
  }
  expect(final.steps[bind].run).toContain('--signed-evidence bindings/nix-signed.json')
  expect(final.steps[bind].run).toContain('--qualification-report candidate/final/image-qualification-report.json')
  const retain = final.steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(retain.if).toBe('always()')
  expect(retain.with.path).toBe('candidate/final/')
})

test('registry image qualification retains the immutable reference and stops on pull failure', () => {
  const root = mkdtempSync(join(tmpdir(), 'nix-published-image-'))
  const script = join(root, 'scripts', 'check_nix_registry_image.sh')
  const calls = join(root, 'calls')
  const image = `ghcr.io/flidai/leapview@sha256:${'a'.repeat(64)}`
  try {
    mkdirSync(join(root, 'scripts'))
    mkdirSync(join(root, 'bin'))
    mkdirSync(join(root, 'application', 'bin'), { recursive: true })
    copyFileSync(resolve('scripts/check_nix_registry_image.sh'), script)
    writeFileSync(join(root, 'bin', 'docker'), '#!/bin/sh\nprintf "docker %s\\n" "$*" >> "$CALLS"\nif [ "$1" = pull ]; then exit "$PULL_STATUS"; fi\n[ "$1" = run ]\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'cc'), '#!/bin/sh\ntouch "$3"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'patchelf'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
    writeFileSync(join(root, 'application', 'bin', 'leapviewctl'), '#!/bin/sh\nprintf "cli %s\\n" "$*" >> "$CALLS"\n', { mode: 0o755 })
    for (const fixture of [
      { image: 'ghcr.io/flidai/leapview:latest', pull: '0', status: 1, expected: [] },
      { image: `example.com/leapview@sha256:${'a'.repeat(64)}`, pull: '0', status: 1, expected: [] },
      { image, pull: '17', status: 17, expected: [`docker pull --platform linux/amd64 ${image}`] },
      { image, pull: '0', status: 0, expected: [`docker pull --platform linux/amd64 ${image}`] },
    ]) {
      rmSync(calls, { force: true })
      const run = spawnSync('bash', [script, fixture.image, join(root, 'application')], {
        env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}`, CALLS: calls, PULL_STATUS: fixture.pull },
      })
      expect(run.status).toBe(fixture.status)
      const commands = fixture.expected.length ? readFileSync(calls, 'utf8').trim().split('\n') : []
      if (!fixture.expected.length) expect(() => readFileSync(calls)).toThrow()
      expect(commands.slice(0, fixture.expected.length)).toEqual(fixture.expected)
      if (fixture.status === 0) {
        expect(commands).toHaveLength(3)
        expect(commands[1]).toEndWith(image)
        expect(commands[2]).toContain(`qualify image --image ${image} --require-immutable`)
        expect(commands.join('\n')).not.toContain('docker push')
        expect(commands.join('\n')).not.toContain('docker tag')
      }
    }
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

test('image qualification rejects fixture tags and uses the normalized Docker image ID', () => {
  const root = mkdtempSync(join(tmpdir(), 'nix-image-import-'))
  const script = join(root, 'scripts', 'check_nix_image.sh')
  const archive = join(root, 'image.tar')
  const calls = join(root, 'docker-calls')
  const imageID = `sha256:${'b'.repeat(64)}`
  try {
    mkdirSync(join(root, 'scripts'))
    mkdirSync(join(root, 'bin'))
    copyFileSync(resolve('scripts/check_nix_image.sh'), script)
    writeFileSync(join(root, 'bin', 'docker'), `#!/bin/sh
printf '%s\\n' "$*" >> '${calls}'
case "$1" in
  load) exit 0 ;;
  image) printf '%s\\n' '${imageID}' ;;
  run) exit 77 ;;
  *) exit 1 ;;
esac
`, { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'cc'), '#!/bin/sh\ntouch "$3"\n', { mode: 0o755 })
    writeFileSync(join(root, 'bin', 'patchelf'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
    for (const tag of ['postgres:18', 'leapview-nix:abcdef123456']) {
      writeFileSync(join(root, 'manifest.json'), JSON.stringify([{ Config: `${'a'.repeat(64)}.json`, RepoTags: [tag] }]))
      expect(spawnSync('tar', ['-cf', archive, '-C', root, 'manifest.json']).status).toBe(0)
      const run = spawnSync('bash', [script, archive, root], {
        env: { ...process.env, PATH: `${join(root, 'bin')}:${process.env.PATH}` },
      })
      if (tag === 'postgres:18') {
        expect(run.status).toBe(1)
        expect(() => readFileSync(calls)).toThrow()
      } else {
        expect(run.status).toBe(77)
        const commands = readFileSync(calls, 'utf8').trim().split('\n')
        expect(commands[2]).toEndWith(imageID)
        expect(commands[2]).not.toContain(`sha256:${'a'.repeat(64)}`)
      }
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
