import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const repository = resolve(import.meta.dirname, "..");
const dependencyScript = join(repository, "scripts/security_dependencies.sh");
const sourceScript = join(repository, "scripts/security_source.sh");

async function executable(path, contents) {
  await writeFile(path, contents, { mode: 0o755 });
}

async function fixtureRepository(t) {
  const root = await mkdtemp(join(tmpdir(), "leapview-security-contract-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const init = spawnSync("git", ["init", "--quiet"], { cwd: root, encoding: "utf8" });
  assert.equal(init.status, 0, init.stderr);
  for (const relative of [
    "go.mod",
    "nested/go.mod",
    "bun.lock",
    "desktop/bun.lock",
    "typespec/package-lock.json",
  ]) {
    const path = join(root, relative);
    await mkdir(resolve(path, ".."), { recursive: true });
    await writeFile(path, relative.endsWith("go.mod") ? "module example.test/fixture\n\ngo 1.25\n" : "{}\n");
  }
  const bin = join(root, "test-bin");
  await mkdir(bin);
  const scannerShim = `#!/usr/bin/env bash
set -euo pipefail
tool="$(basename "$0")"
printf '%s|%s|%s\\n' "$tool" "$PWD" "$*" >> "$SECURITY_TEST_LOG"
if [[ "$tool" == "go" && "$#" -ge 3 && "$1" == "run" && "$2" == "./internal/app/tools/securitypolicy" ]]; then
  printf '{"exceptions":[]}\\n'
  exit 0
fi
if [[ "\${SECURITY_TEST_FAILURE:-}" == "vulnerable" && "$tool" == "bun" ]]; then
  printf 'critical dependency finding in fixture\\n' >&2
  exit 1
fi
if [[ "\${SECURITY_TEST_FAILURE:-}" == "bun-operational" && "$tool" == "bun" ]]; then
  printf '{}\\n'
  printf 'bun scanner unavailable\\n' >&2
  exit 70
fi
if [[ "\${SECURITY_TEST_FAILURE:-}" == "bun-nonblocking" && "$tool" == "bun" ]]; then
  printf '{"example-package":[{"id":123,"severity":"moderate"}]}\\n'
  exit 1
fi
if [[ "\${SECURITY_TEST_FAILURE:-}" == "crash" && "$tool" == "go" ]]; then
  printf 'dependency scanner unavailable\\n' >&2
  exit 70
fi
`;
  for (const tool of ["go", "bun", "npm"]) await executable(join(bin, tool), scannerShim);
  return { root, bin, log: join(root, "scanner.log") };
}

async function coveredFixtureRepository(t) {
  const fixture = await fixtureRepository(t);
  await mkdir(join(fixture.root, ".security"), { recursive: true });
  await writeFile(join(fixture.root, ".security", "coverage.yaml"), "version: 1\n");
  const policy = join(fixture.root, "internal", "app", "tools", "securitypolicy");
  await mkdir(policy, { recursive: true });
  await writeFile(join(policy, "main.go"), "package main\n\nfunc main() {}\n");
  return fixture;
}

function run(script, fixture, extraEnv = {}) {
  return spawnSync("bash", [script], {
    cwd: fixture.root,
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${fixture.bin}:${process.env.PATH}`,
      SECURITY_TEST_LOG: fixture.log,
      ...extraEnv,
    },
  });
}

test("dependency gate discovers every maintained module and lockfile", async (t) => {
  const fixture = await fixtureRepository(t);
  const result = run(dependencyScript, fixture);
  assert.equal(result.status, 0, result.stderr);
  const log = await readFile(fixture.log, "utf8");
  for (const fragment of [
    `go|${fixture.root}|run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`,
    `go|${join(fixture.root, "nested")}|run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`,
    `bun|${fixture.root}|audit --audit-level critical`,
    `bun|${join(fixture.root, "desktop")}|audit --audit-level critical`,
    `npm|${join(fixture.root, "typespec")}|audit --package-lock-only --audit-level=critical --ignore-scripts`,
  ]) assert.match(log, new RegExp(fragment.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});

test("dependency gate rejects a vulnerable fixture and scanner outage", async (t) => {
  const vulnerable = await fixtureRepository(t);
  const finding = run(dependencyScript, vulnerable, { SECURITY_TEST_FAILURE: "vulnerable" });
  assert.notEqual(finding.status, 0);
  assert.match(finding.stderr, /critical dependency finding/);

  const unavailable = await fixtureRepository(t);
  const outage = run(dependencyScript, unavailable, { SECURITY_TEST_FAILURE: "crash" });
  assert.notEqual(outage.status, 0);
  assert.match(outage.stderr, /scanner unavailable/);
});

test("covered Bun audit rejects valid JSON from an operational scanner failure", async (t) => {
  const fixture = await coveredFixtureRepository(t);
  const result = run(dependencyScript, fixture, { SECURITY_TEST_FAILURE: "bun-operational" });
  assert.notEqual(result.status, 0);
  const output = `${result.stdout}${result.stderr}`;
  assert.match(output, /bun scanner unavailable/);
  assert.match(result.stderr, /scanner failed without decoded blocking findings/);
  assert.doesNotMatch(result.stderr, /no Critical findings/);
  const log = await readFile(fixture.log, "utf8");
  assert.match(log, /bun\|.*audit --audit-level critical --json/);
});

test("covered Bun audit accepts decoded advisories below the blocking threshold", async (t) => {
  const fixture = await coveredFixtureRepository(t);
  const result = run(dependencyScript, fixture, { SECURITY_TEST_FAILURE: "bun-nonblocking" });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /no Critical findings \(1 below threshold\)/);
  assert.doesNotMatch(result.stderr, /scanner failed/);
});

async function sourceFixture(t) {
  const fixture = await fixtureRepository(t);
  const scripts = join(fixture.root, "scripts");
  await mkdir(scripts);
  await executable(join(scripts, "security_secrets.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf 'secrets|%s\\n' "$PWD" >> "$SECURITY_TEST_LOG"
if [[ "\${SECURITY_TEST_SOURCE_FAILURE:-}" == "secret" ]]; then
  printf 'secret finding: [REDACTED]\\n' >&2
  exit 1
fi
`);
  await executable(join(fixture.bin, "docker"), `#!/usr/bin/env bash
set -euo pipefail
printf 'docker|%s\\n' "$*" >> "$SECURITY_TEST_LOG"
if [[ "\${SECURITY_TEST_SOURCE_FAILURE:-}" == "unavailable" ]]; then
  printf 'source scanner unavailable\\n' >&2
  exit 127
fi
if [[ "\${SECURITY_TEST_SOURCE_FAILURE:-}" == "misconfiguration" && "$1" == "run" ]]; then
  printf 'HIGH infrastructure misconfiguration\\n' >&2
  exit 1
fi
`);
  return fixture;
}

test("source gate uses the pinned scanner and rejects secret and IaC fixtures", async (t) => {
  const clean = await sourceFixture(t);
  const pass = run(sourceScript, clean);
  assert.equal(pass.status, 0, pass.stderr);
  const log = await readFile(clean.log, "utf8");
  assert.match(log, /aquasec\/trivy:0\.74\.0@sha256:[0-9a-f]{64}/);
  assert.match(log, /--scanners secret,misconfig/);
  assert.match(log, /--severity HIGH,CRITICAL/);

  const secret = await sourceFixture(t);
  const fixtureSecret = "sentinel_value_never_logged_123";
  await writeFile(join(secret.root, ".env"), `TOKEN=${fixtureSecret}\n`);
  const rejectedSecret = run(sourceScript, secret, { SECURITY_TEST_SOURCE_FAILURE: "secret" });
  assert.notEqual(rejectedSecret.status, 0);
  assert.match(rejectedSecret.stderr, /\[REDACTED\]/);
  assert.doesNotMatch(`${rejectedSecret.stdout}${rejectedSecret.stderr}`, new RegExp(fixtureSecret));

  const iac = await sourceFixture(t);
  const rejectedIaC = run(sourceScript, iac, { SECURITY_TEST_SOURCE_FAILURE: "misconfiguration" });
  assert.notEqual(rejectedIaC.status, 0);
  assert.match(rejectedIaC.stderr, /HIGH infrastructure misconfiguration/);

  const unavailable = await sourceFixture(t);
  const outage = run(sourceScript, unavailable, { SECURITY_TEST_SOURCE_FAILURE: "unavailable" });
  assert.notEqual(outage.status, 0);
  assert.match(outage.stderr, /scanner unavailable/);
});
