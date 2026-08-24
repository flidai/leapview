import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import test from "node:test";

test("desktop workflow builds and qualifies both native macOS architectures", async () => {
  const root = resolve(import.meta.dirname, "..", "..");
  const [workflow, mergeWorkflow, candidateAction, readme] = await Promise.all([
    readFile(resolve(root, ".github/workflows/electron-security-proof.yml"), "utf8"),
    readFile(resolve(root, ".github/workflows/merge-validation.yml"), "utf8"),
    readFile(resolve(root, ".github/actions/desktop-preview-candidate/action.yml"), "utf8"),
    readFile(resolve(root, "desktop/README.md"), "utf8"),
  ]);

  for (const required of [
    "merge_group:",
    "types: [checks_requested]",
    "name: macOS Apple silicon",
    "os: macos-15",
    "artifact: macos-arm64",
    "name: macOS Intel",
    "os: macos-15-intel",
    "artifact: macos-x64",
    "name: Policy integration (macOS ${{ matrix.architecture }})",
    "architecture: Apple silicon",
    "architecture: Intel",
    "sbom=\"$(find candidate/out/evidence -type f -name '*.spdx.json' -print -quit)\"",
    "sbom-path: ${{ steps.evidence.outputs.sbom_path }}",
    "name: Electron gate",
    "needs: [contract, packages, macos, windows, linux]",
    "EVENT_NAME: ${{ github.event_name }}",
    "MACOS_RESULT: ${{ needs.macos.result }}",
    "WINDOWS_RESULT: ${{ needs.windows.result }}",
  ]) {
    assert.ok(workflow.includes(required), `workflow is missing ${required}`);
  }
  for (const required of [
    "paths:",
    '".github/actions/desktop-preview-candidate/**"',
    "matrix.artifact == 'linux-x64'",
  ]) {
    assert.ok(workflow.includes(required), `workflow is missing ${required}`);
  }
  assert.ok(
    workflow.includes("uses: ./.github/actions/desktop-preview-candidate"),
    "security workflow does not reuse the candidate action",
  );
  for (const required of [
    "name: Require native desktop merge proof",
    "actions: read",
    "electron-security-proof.yml/runs?head_sha=$REVISION&event=merge_group",
    "for _ in $(seq 1 240)",
    "test \"$conclusion\" = success",
  ]) {
    assert.ok(
      mergeWorkflow.includes(required),
      `merge validation is missing ${required}`,
    );
  }
  assert.doesNotMatch(
    workflow,
    /^ {4}if:.*matrix\./mu,
    "security workflow filters a matrix job before GitHub expands the matrix",
  );
  for (const required of [
    "TestPackagedLeapViewPreservesRemoteContentBoundary",
    "runner.os == 'macOS'",
    'LEAPVIEW_PACKAGED_APP="$GITHUB_WORKSPACE/$executable"',
    "runner.os == 'Windows'",
    "runner.os == 'Linux'",
  ]) {
    assert.ok(candidateAction.includes(required), `candidate action is missing ${required}`);
  }
  assert.match(readme, /macOS Intel and Apple-silicon candidates are\s+built natively/);
  assert.doesNotMatch(readme, /Only the Intel macOS/);
});

test("desktop preview publication is manual, unsigned, immutable, and attested", async () => {
  const root = resolve(import.meta.dirname, "..", "..");
  const [workflow, candidateAction] = await Promise.all([
    readFile(resolve(root, ".github/workflows/desktop-preview-release.yml"), "utf8"),
    readFile(resolve(root, ".github/actions/desktop-preview-candidate/action.yml"), "utf8"),
  ]);
  for (const required of [
    "workflow_dispatch:",
    "confirm_unsigned_preview:",
    "environment: desktop-preview",
    "fetch-depth: 0",
    'git merge-base --is-ancestor "$source_sha" "origin/$default_branch"',
    "LEAPVIEW_DESKTOP_DISTRIBUTION: preview",
    'require("./docs/desktop-release.json").release.tag',
    'require("./docs/desktop-release.json").release.version',
    "node scripts/preview-release.mjs stage",
    "attestations: write",
    "id-token: write",
    'gh release create "$release_tag"',
    "--draft",
    "--prerelease",
    "--target \"$source_sha\"",
    "This build is unsigned",
    "Verify staged draft assets and attestations",
    'cmp "release/$name" "$download/$name"',
    "sha256sum --check SHA256SUMS",
    "test \"$(find . -maxdepth 1 -type f -name '*.spdx.json' | wc -l)\" -eq 4",
    "gh attestation verify \"$evidence\"",
    "--source-digest \"$SOURCE_SHA\"",
    "name: Remove failed draft preview",
    "if: ${{ failure() }}",
    "is_draft=\"$(gh release view \"$RELEASE_TAG\" --json isDraft --jq '.isDraft' 2>/dev/null || true)\"",
    "if [[ \"$is_draft\" == \"true\" ]]",
    "gh release delete \"$RELEASE_TAG\" --cleanup-tag --yes",
    'gh release edit "$RELEASE_TAG" --draft=false',
  ]) {
    assert.ok(workflow.includes(required), `preview workflow is missing ${required}`);
  }
  assert.ok(
    workflow.includes("uses: ./.github/actions/desktop-preview-candidate"),
    "preview workflow does not reuse the candidate action",
  );
  assert.ok(
    candidateAction.includes("bun run evidence"),
    "candidate action does not generate release evidence from the tested artifact",
  );
  assert.doesNotMatch(workflow, /^\s{2}(?:push|pull_request):/mu);
  assert.doesNotMatch(workflow, /latest|stable-pointer|auto-update/iu);
  const draft = workflow.indexOf("--draft");
  const verification = workflow.indexOf("Verify staged draft assets and attestations");
  const publish = workflow.indexOf('gh release edit "$RELEASE_TAG" --draft=false');
  const finalVerification = workflow.indexOf("Verify published prerelease shape");
  const cleanup = workflow.indexOf("Remove failed draft preview");
  assert.ok(draft >= 0 && verification > draft && publish > verification && finalVerification > publish && cleanup > finalVerification);
  assert.match(workflow.slice(cleanup), /if: \$\{\{ failure\(\) \}\}/);
  assert.match(workflow.slice(cleanup), /is_draft=.*gh release view[\s\S]*if \[\[ "\$is_draft" == "true" \]\]/);
  assert.match(workflow.slice(verification, publish), /isDraft == true/);
  assert.match(workflow.slice(publish), /isDraft == false/);
});
