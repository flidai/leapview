import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
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
