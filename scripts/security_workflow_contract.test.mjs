import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const root = resolve(import.meta.dirname, "..");

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
  assert.match(await repositoryFile(".github/workflows/security.yml"), /version: 0\.74\.0/);
});

test("security workflow contains no mutable third-party action refs", async () => {
  const workflow = await repositoryFile(".github/workflows/security.yml");
  for (const match of workflow.matchAll(/^\s*uses:\s*([^\s#]+)/gm)) {
    const value = match[1];
    if (value.startsWith("./")) continue;
    assert.match(value, /@[0-9a-f]{40}$/);
  }
});
