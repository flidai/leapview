import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join, relative, resolve } from "node:path";
import { tmpdir } from "node:os";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");
const exceptionMatcher = resolve(root, "scripts/security_exception_match.sh");

async function repositoryFile(path) {
  return readFile(resolve(root, path), "utf8");
}

test("required Security gate aggregates every fail-closed lane", async () => {
  const workflow = await repositoryFile(".github/workflows/security.yml");
  for (const fragment of [
    "pull_request:",
    "push:",
    "branches: [main]",
    "merge_group:",
    "policy-validation:",
    "dependency-validation:",
    "source-validation:",
    "uses: ./.github/actions/setup-ci",
    "sast-validation:",
    "build-mode: autobuild",
    "build-mode: ${{ matrix.build-mode }}",
    "name: Security gate",
    "if: ${{ always() }}",
    "needs: [policy-validation, dependency-validation, source-validation, sast-validation]",
    "./scripts/require_security_results.sh",
  ]) {
    assert.match(workflow, new RegExp(fragment.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
});

test("aggregate Security gate rejects every non-success dependency result", () => {
  const aggregate = resolve(root, "scripts/require_security_results.sh");
  const passing = spawnSync(aggregate, ["success", "success", "success", "success"], { encoding: "utf8" });
  assert.equal(passing.status, 0, passing.stderr);
  for (const state of ["failure", "cancelled", "skipped", "timed_out", "", "unknown"]) {
    const result = spawnSync(aggregate, ["success", state, "success", "success"], { encoding: "utf8" });
    assert.notEqual(result.status, 0, `aggregate accepted ${JSON.stringify(state)}`);
    assert.match(result.stderr, /Security validation result:/);
  }
  const missing = spawnSync(aggregate, ["success", "success", "success"], { encoding: "utf8" });
  assert.equal(missing.status, 64);
});

test("aggregate job checks out the candidate-owned result contract", async () => {
  const workflow = await repositoryFile(".github/workflows/security.yml");
  const aggregateJob = workflow.slice(workflow.indexOf("  security-gate:"));
  assert.match(aggregateJob, /uses: actions\/checkout@[0-9a-f]{40}/);
  assert.match(aggregateJob, /\.\/scripts\/require_security_results\.sh/);
});

test("source and dependency scanners are pinned, redacted, and bounded", async () => {
  const [dependencies, secrets, source] = await Promise.all([
    repositoryFile("scripts/security_dependencies.sh"),
    repositoryFile("scripts/security_secrets.sh"),
    repositoryFile("scripts/security_source.sh"),
  ]);
  assert.match(dependencies, /GOVULNCHECK_VERSION="v1\.6\.0"/);
  assert.match(dependencies, /AUDIT_LEVEL="critical"/);
  assert.match(dependencies, /-not -path '\*\/node_modules\/\*'/);
  assert.match(secrets, /GITLEAKS_VERSION="v8\.30\.1"/);
  assert.equal((secrets.match(/--redact=100/g) ?? []).length, 2);
  assert.match(secrets, /git ls-files --cached --others --exclude-standard -z/);
  assert.match(secrets, /origin\/main/);
  assert.match(source, /aquasec\/trivy:0\.74\.0@sha256:[0-9a-f]{64}/);
  assert.match(source, /--severity HIGH,CRITICAL/);
  assert.match(await repositoryFile(".github/workflows/security.yml"), /version: v0\.74\.0/);
});

test("security workflow contains no mutable third-party action refs", async () => {
  const workflow = await repositoryFile(".github/workflows/security.yml");
  for (const match of workflow.matchAll(/^\s*uses:\s*([^\s#]+)/gm)) {
    const value = match[1];
    if (value.startsWith("./")) continue;
    assert.match(value, /@[0-9a-f]{40}$/);
  }
});

test("native OCI platform refs are admitted before release and site manifest assembly", async () => {
  const workflows = await Promise.all([
    repositoryFile(".github/workflows/release.yml"),
    repositoryFile(".github/workflows/site-image.yml"),
  ]);
  for (const workflow of workflows) {
    const attestation = workflow.indexOf("Attest native");
    const admission = workflow.indexOf("      - name: Admit exact native");
    const record = workflow.indexOf("Record admitted native");
    const assembly = workflow.indexOf("docker buildx imagetools create");
    const topLevelAdmission = workflow.lastIndexOf("uses: ./.github/actions/oci-admission");
    assert.ok(attestation >= 0, "native provenance attestation is required");
    assert.ok(admission > attestation, "native admission must follow provenance attestation");
    assert.ok(record > admission, "only admitted refs may be recorded");
    assert.ok(assembly > record, "manifest assembly must follow native admission");
    assert.ok(topLevelAdmission > assembly, "top-level manifest admission must remain after assembly");
    const admittedBlock = workflow.slice(admission, record);
    assert.match(admittedBlock, /uses: \.\/\.github\/actions\/oci-admission/);
    assert.match(admittedBlock, /image: \$\{\{ env\.IMAGE_NAME \}\}@\$\{\{ steps\.publish\.outputs\.digest \}\}/);
    assert.match(admittedBlock, /repository: \$\{\{ env\.IMAGE_NAME \}\}/);
    assert.match(admittedBlock, /expected-workflow: flidai\/leapview\/\.github\/workflows\/(?:release|site-image)\.yml/);
    assert.match(admittedBlock, /source-revision: \$\{\{ needs\.identity\.outputs\.revision \}\}/);
    const recordedBlock = workflow.slice(record, assembly);
    assert.match(recordedBlock, /IMAGE_REFERENCE: \$\{\{ steps\.admission\.outputs\.image \}\}/);
    assert.match(recordedBlock, /printf '%s\\n' "\$IMAGE_REFERENCE"/);
    assert.match(workflow.slice(assembly), /image_references\[@\]/);
  }
});

test("security exception matcher resolves relative roots before changing directory", async (t) => {
  const fixture = await mkdtemp(join(tmpdir(), "leapview-security-match-"));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  const bin = join(fixture, "bin");
  const log = join(fixture, "go-args.log");
  await mkdir(bin);
  await writeFile(
    join(bin, "go"),
    '#!/usr/bin/env bash\nprintf \'%s\\n\' "$PWD" "$@" > "$SECURITY_MATCH_LOG"\n',
    { mode: 0o755 },
  );

  const result = spawnSync("bash", [exceptionMatcher, "--root", relative(root, fixture), "--scanner", "test", "--rule", "test", "--resource", "test"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, SECURITY_MATCH_LOG: log },
  });
  assert.equal(result.status, 0, result.stderr);
  const args = (await readFile(log, "utf8")).trim().split("\n");
  assert.equal(args[0], fixture);
  assert.ok(args.includes("--root"));
  assert.equal(args[args.indexOf("--root") + 1], fixture);
});
