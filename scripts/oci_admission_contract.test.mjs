import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdtemp, mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import test from "node:test";

const run = promisify(execFile);
const root = resolve(import.meta.dirname, "..");
const script = resolve(root, "scripts/admit_oci_artifact.sh");
const policy = resolve(root, ".github/security/container-vulnerability-policy.json");
const revision = "0123456789abcdef0123456789abcdef01234567";
const digest = `sha256:${"a".repeat(64)}`;
const image = `ghcr.io/flidai/leapview@${digest}`;
const workflow = "flidai/leapview/.github/workflows/artifacts.yml";

async function fixture(overrides = {}) {
  const directory = await mkdtemp(join(tmpdir(), "leapview-oci-admission-"));
  const policyBytes = await readFile(policy);
  const evidence = {
    schemaVersion: 1,
    image,
    digest,
    registryDigest: digest,
    attestation: {
      verified: true,
      repository: "flidai/leapview",
      workflow,
      sourceRevision: revision,
    },
    sbom: { discoverable: true, predicateType: "https://spdx.dev/Document/v2.3" },
    vulnerabilityPolicy: {
      sha256: createHash("sha256").update(policyBytes).digest("hex"),
      scanner: "trivy",
      passed: true,
    },
    ...overrides,
  };
  const evidencePath = join(directory, "evidence.json");
  await writeFile(evidencePath, `${JSON.stringify(evidence)}\n`);
  return { directory, evidencePath };
}

async function executable(path, contents) {
  await writeFile(path, contents);
  await chmod(path, 0o755);
}

async function liveFixture() {
  const directory = await mkdtemp(join(tmpdir(), "leapview-oci-live-"));
  const bin = join(directory, "bin");
  await mkdir(bin);
  await executable(join(bin, "gh"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == "attestation verify --help" ]]; then exit 0; fi
if [[ "\${OCI_TEST_ATTESTATION:-valid}" == unavailable ]]; then exit 70; fi
workflow="${workflow}"
revision="${revision}"
if [[ "\${OCI_TEST_ATTESTATION:-valid}" == wrong-workflow ]]; then
  workflow="flidai/leapview/.github/workflows/untrusted.yml"
fi
if [[ "\${OCI_TEST_ATTESTATION:-valid}" == wrong-revision ]]; then
  revision="${"f".repeat(40)}"
fi
jq -n --arg workflow "$workflow" --arg revision "$revision" '[{
  verificationResult: {signature: {certificate: {
    sourceRepository: "flidai/leapview",
    workflow: $workflow,
    sourceRepositoryDigest: $revision
  }}}
}]'
`);
  await executable(join(bin, "docker"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"imagetools inspect"* ]]; then
  if [[ "\${OCI_TEST_SBOM:-present}" == missing ]]; then printf '{}\\n'; else printf '{"SPDX":{"SPDXID":"SPDXRef-DOCUMENT"}}\\n'; fi
  exit 0
fi
printf 'unexpected docker invocation: %s\\n' "$*" >&2
exit 64
`);
  await executable(join(bin, "trivy"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == version ]]; then printf '{"Version":"0.74.0"}\\n'; exit 0; fi
if [[ "\${OCI_TEST_TRIVY:-clean}" == unavailable ]]; then exit 70; fi
if [[ "\${OCI_TEST_TRIVY:-clean}" == vulnerable ]]; then
  printf '{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-2026-0001"}]}]}\\n'
else
  printf '{"Results":[]}\\n'
fi
`);
  return { directory, bin };
}

function liveArgs() {
  return [
    script,
    "--image", image,
    "--repository", "ghcr.io/flidai/leapview",
    "--expected-workflow", workflow,
    "--source-revision", revision,
    "--policy", policy,
  ];
}

function liveEnv(fixtureData, overrides = {}) {
  return {
    ...process.env,
    PATH: `${fixtureData.bin}:/usr/bin:/bin`,
    GH_TOKEN: "fixture-token",
    GITHUB_REPOSITORY: "flidai/leapview",
    ...overrides,
  };
}

function args(fixtureData, imageReference = image) {
  return [
    script,
    "--image",
    imageReference,
    "--repository",
    "ghcr.io/flidai/leapview",
    "--expected-workflow",
    workflow,
    "--source-revision",
    revision,
    "--policy",
    policy,
    "--mode",
    "hermetic",
    "--evidence",
    fixtureData.evidencePath,
  ];
}

test("OCI admission rejects mutable image references", async () => {
  const fixtureData = await fixture();
  await assert.rejects(
    run("bash", args(fixtureData, "ghcr.io/flidai/leapview:main")),
    /digest/,
  );
});

test("OCI admission rejects live mode when a verifier is missing", async () => {
  await assert.rejects(
    run(
      "bash",
      [
        script,
        "--image",
        image,
        "--repository",
        "ghcr.io/flidai/leapview",
        "--expected-workflow",
        workflow,
        "--source-revision",
        revision,
        "--policy",
        policy,
      ],
      { env: { ...process.env, PATH: "/usr/bin:/bin", GITHUB_TOKEN: "test" } },
    ),
    /verifier .*missing/,
  );
});

test("OCI admission rejects wrong attestation identity evidence", async () => {
  const fixtureData = await fixture({
    attestation: {
      verified: true,
      repository: "attacker/example",
      workflow,
      sourceRevision: revision,
    },
  });
  await assert.rejects(run("bash", args(fixtureData)), /hermetic evidence/);
});

test("OCI admission rejects evidence for a substituted digest", async () => {
  const fixtureData = await fixture({
    image: "ghcr.io/flidai/leapview@sha256:" + "b".repeat(64),
  });
  await assert.rejects(run("bash", args(fixtureData)), /hermetic evidence/);
});

test("admission helper is executable and fail-closed", async () => {
  assert.equal((await stat(script)).mode & 0o111, 0o111);
});

test("live OCI admission verifies attestation, SBOM, scanner version, and policy", async () => {
  const fixtureData = await liveFixture();
  const result = await run("bash", liveArgs(), { env: liveEnv(fixtureData) });
  assert.match(result.stdout, new RegExp(`${image}\\n$`));
});

for (const testCase of [
  { name: "wrong workflow", env: { OCI_TEST_ATTESTATION: "wrong-workflow" }, error: /identity or source revision/ },
  { name: "wrong source revision", env: { OCI_TEST_ATTESTATION: "wrong-revision" }, error: /identity or source revision/ },
  { name: "missing SBOM", env: { OCI_TEST_SBOM: "missing" }, error: /no SPDX SBOM/ },
  { name: "scanner outage", env: { OCI_TEST_TRIVY: "unavailable" }, error: /scan could not complete/ },
  { name: "policy-level CVE", env: { OCI_TEST_TRIVY: "vulnerable" }, error: /exceeds policy/ },
]) {
  test(`live OCI admission rejects ${testCase.name}`, async () => {
    const fixtureData = await liveFixture();
    await assert.rejects(
      run("bash", liveArgs(), { env: liveEnv(fixtureData, testCase.env) }),
      testCase.error,
    );
  });
}
