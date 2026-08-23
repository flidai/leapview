import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import test from "node:test";

import {
  buildSpdxDocument,
  createReleaseManifest,
  validateReleasePolicy,
} from "./release-evidence.mjs";
import {
  validateSquirrelReleaseIndex,
  verifyReleaseEvidence,
} from "./verify-release-evidence.mjs";

const packageDocument = {
  name: "@leapview/desktop",
  productName: "LeapView",
  version: "0.1.0",
  devDependencies: {
    electron: "43.2.0",
    node: "24.14.0",
    "@electron-forge/cli": "8.0.0-alpha.9",
  },
};

const policy = {
  schemaVersion: 2,
  applicationVersion: "0.1.0",
  channel: "consumer-v1",
  distribution: {
    darwin: {
      installer: "dmg",
      updateArtifacts: ["zip"],
      updateMechanism: "squirrel-mac",
      scope: "user-installed",
    },
    linux: {
      installer: "deb",
      updateArtifacts: [],
      updateMechanism: "apt",
      scope: "system-package-manager",
    },
    win32: {
      installer: "exe",
      updateArtifacts: ["nupkg", "RELEASES"],
      updateMechanism: "squirrel-windows",
      scope: "per-user",
    },
  },
  runtime: {
    electron: "43.2.0",
    electronMajor: 43,
    chromium: "150.0.7871.129",
    node: "24.14.0",
    forge: "8.0.0-alpha.9",
    bun: "1.3.14",
  },
  updates: {
    origin: "https://releases.leapview.dev",
    pathVersion: "v1",
    channel: "stable",
    productName: "LeapView",
    applicationId: "dev.leapview.desktop",
    electronMajor: 43,
    windowsPackageId: "leapview",
  },
  supportMatrix: [
    {
      platform: "darwin",
      architectures: ["arm64", "x64"],
      minimumVersion: "macOS 13 Ventura",
    },
    {
      platform: "linux",
      architectures: ["x64"],
      minimumVersion: "Ubuntu 22.04 LTS",
    },
    {
      platform: "win32",
      architectures: ["x64"],
      minimumVersion: "Windows 10",
    },
  ],
  hardening: {
    asarOnly: true,
    fuses: {
      RunAsNode: "disabled",
      EnableCookieEncryption: "enabled",
      EnableNodeOptionsEnvironmentVariable: "disabled",
      EnableNodeCliInspectArguments: "disabled",
      EnableEmbeddedAsarIntegrityValidation: {
        darwin: "enabled",
        linux: "disabled",
        win32: "enabled",
      },
      OnlyLoadAppFromAsar: "enabled",
      LoadBrowserProcessSpecificV8Snapshot: "disabled",
      GrantFileProtocolExtraPrivileges: "disabled",
      WasmTrapHandlers: "enabled",
    },
  },
  publication: {
    codeSigningRequired: true,
    githubAttestationsRequired: true,
    immutableArtifactsRequired: true,
  },
  privacy: {
    evidenceContainsCustomerData: false,
    evidenceContainsCredentials: false,
    evidenceContainsDiagnostics: false,
  },
};

const packageVerification = {
  schemaVersion: 2,
  platform: "darwin",
  architecture: "arm64",
  packageFormat: "dmg",
  asarOnly: true,
  runtime: {
    electron: "43.2.0",
    chromium: "150.0.7871.129",
    node: "24.14.0",
  },
  fuses: {
    RunAsNode: "disabled",
    EnableCookieEncryption: "enabled",
    EnableNodeOptionsEnvironmentVariable: "disabled",
    EnableNodeCliInspectArguments: "disabled",
    EnableEmbeddedAsarIntegrityValidation: "enabled",
    OnlyLoadAppFromAsar: "enabled",
    LoadBrowserProcessSpecificV8Snapshot: "disabled",
    GrantFileProtocolExtraPrivileges: "disabled",
    WasmTrapHandlers: "enabled",
  },
  asarFiles: 27,
  accessibility: {
    mode: "open",
    announcement: "none",
    controls: 2,
    focusedControl: "LeapView URL",
    regions: ["Connect an instance"],
  },
  updates: {
    origin: "https://releases.leapview.dev",
    pathVersion: "v1",
    channel: "stable",
    productName: "LeapView",
    applicationId: "dev.leapview.desktop",
    electronMajor: 43,
    delivery: "electron-auto-updater",
  },
  installer: {
    format: "dmg",
    scope: "user-installed",
    policyIntegration: "deferred-not-supported",
    protocolIntegration: "consumer-owned-validated-url",
    updateArtifacts: ["zip"],
    updateMechanism: "squirrel-mac",
  },
  startup: "trusted-shell-ready",
};

test("release policy pins the supported Electron line and packaging contract", () => {
  assert.doesNotThrow(() => validateReleasePolicy(policy, packageDocument));

  const unsupported = structuredClone(policy);
  unsupported.runtime.electronMajor = 42;
  assert.throws(
    () => validateReleasePolicy(unsupported, packageDocument),
    /Electron major/,
  );

  const mutable = structuredClone(packageDocument);
  mutable.devDependencies.electron = "^43.2.0";
  assert.throws(
    () => validateReleasePolicy(policy, mutable),
    /exact Electron version/,
  );

  assert.throws(
    () =>
      buildSpdxDocument({
        artifactSha256: "a".repeat(64),
        createdAt: "2026-07-29T12:00:00.000Z",
        files: [],
        lock: {
          workspaces: { "": { devDependencies: { electron: "^43.0.0" } } },
          packages: {
            electron: ["electron@^43.0.0", "", {}, "sha512-ZWx1Y3Ryb24="],
          },
        },
        packageDocument,
        packageVerification,
        sourceSha: "d".repeat(40),
      }),
    /mutable resolution/,
  );
});

test("SPDX document covers every locked dependency and packaged runtime file", () => {
  const lock = {
    workspaces: {
      "": {
        devDependencies: {
          electron: "43.2.0",
          rxjs: "7.8.2",
        },
      },
    },
    packages: {
      electron: [
        "electron@43.2.0",
        "",
        { dependencies: { "@electron/get": "^3.0.0" } },
        "sha512-ZWx1Y3Ryb24=",
      ],
      rxjs: ["rxjs@7.8.2", "", {}, "sha512-cnhqcw=="],
      "@electron/get": ["@electron/get@3.1.0", "", {}, "sha512-Z2V0"],
    },
  };
  const files = [
    {
      path: "LeapView.app/Contents/MacOS/LeapView",
      sha1: "1".repeat(40),
      sha256: "a".repeat(64),
      type: "BINARY",
    },
    {
      path: "LeapView.app/Contents/Resources/app.asar",
      sha1: "2".repeat(40),
      sha256: "b".repeat(64),
      type: "ARCHIVE",
    },
  ];
  const document = buildSpdxDocument({
    artifactSha256: "c".repeat(64),
    createdAt: "2026-07-29T12:00:00.000Z",
    files,
    lock,
    packageDocument,
    packageVerification,
    sourceSha: "d".repeat(40),
  });

  assert.equal(document.spdxVersion, "SPDX-2.3");
  assert.equal(
    document.packages.filter((entry) =>
      entry.SPDXID.startsWith("SPDXRef-Dependency-"),
    ).length,
    Object.keys(lock.packages).length,
  );
  assert.equal(document.files.length, files.length);
  assert.ok(
    document.relationships.some(
      (entry) =>
        entry.relationshipType === "DEPENDS_ON" &&
        document.packages.some(
          (candidate) =>
            candidate.SPDXID === entry.relatedSpdxElement &&
            candidate.name === "@electron/get",
        ),
    ),
  );
});

test("SPDX document binds vendored compatibility packages to the source commit", () => {
  const sourceSha = "d".repeat(40);
  const document = buildSpdxDocument({
    artifactSha256: "c".repeat(64),
    createdAt: "2026-07-29T12:00:00.000Z",
    files: [],
    lock: {
      workspaces: {
        "": {
          devDependencies: {
            "brace-expansion": "file:vendor/brace-expansion-compat",
          },
        },
      },
      packages: {
        "brace-expansion": [
          "brace-expansion@file:vendor/brace-expansion-compat",
          { dependencies: { "brace-expansion-next": "npm:brace-expansion@5.0.9" } },
        ],
        "brace-expansion-next": [
          "brace-expansion@5.0.9",
          "",
          {},
          "sha512-YnJhY2Vz",
        ],
      },
    },
    packageDocument,
    packageVerification,
    sourceSha,
  });
  const compatibilityPackage = document.packages.find(
    (entry) => entry.name === "brace-expansion" && entry.versionInfo.startsWith("file:"),
  );
  assert.equal(
    compatibilityPackage?.versionInfo,
    `file:vendor/brace-expansion-compat#${sourceSha}`,
  );
  assert.equal(compatibilityPackage?.externalRefs, undefined);

  assert.throws(
    () =>
      buildSpdxDocument({
        artifactSha256: "c".repeat(64),
        createdAt: "2026-07-29T12:00:00.000Z",
        files: [],
        lock: {
          workspaces: { "": { devDependencies: {} } },
          packages: {
            escape: ["escape@file:../outside", "", {}],
          },
        },
        packageDocument,
        packageVerification,
        sourceSha,
      }),
    /mutable resolution/,
  );
});

test("SPDX relationships preserve Bun alias install paths and wrapper edges", () => {
  const sourceSha = "d".repeat(40);
  const lock = {
    workspaces: {
      "": {
        devDependencies: {
          "brace-expansion": "file:vendor/brace-expansion-compat",
          "image-size": "file:vendor/image-size-next-compat",
          "minimatch-modern": "npm:minimatch@10.2.6",
        },
      },
    },
    packages: {
      "brace-expansion": [
        "brace-expansion@file:vendor/brace-expansion-compat",
        { dependencies: { "brace-expansion-next": "npm:brace-expansion@5.0.9" } },
      ],
      "brace-expansion-next": ["brace-expansion@5.0.9", "", {}],
      "image-size": [
        "image-size@file:vendor/image-size-next-compat",
        { dependencies: { "image-size-next": "1.2.2" } },
      ],
      "image-size-next": ["image-size-next@1.2.2", "", {}],
      "minimatch-modern": [
        "minimatch@10.2.6",
        "",
        { dependencies: { "brace-expansion": "^5.0.8" } },
      ],
      "minimatch-modern/brace-expansion": [
        "brace-expansion@file:vendor/brace-expansion-compat",
        { dependencies: { "brace-expansion-next": "npm:brace-expansion@5.0.9" } },
      ],
      "minimatch/brace-expansion": [
        "brace-expansion@file:vendor/brace-expansion-compat",
        { dependencies: { "brace-expansion-next": "npm:brace-expansion@5.0.9" } },
      ],
      wrapper: [
        "wrapper@1.0.0",
        "",
        { dependencies: { shared: "1.0.0" } },
      ],
      "wrappe/shared": ["shared@1.0.0", "", {}],
      "elsewhere/shared": ["shared@1.0.0", "", {}],
    },
  };
  const document = buildSpdxDocument({
    artifactSha256: "c".repeat(64),
    createdAt: "2026-07-29T12:00:00.000Z",
    files: [],
    lock,
    packageDocument,
    packageVerification,
    sourceSha,
  });
  const packageIdForPath = (installPath) =>
    installPath === ""
      ? "SPDXRef-Package-LeapView-Desktop"
      : document.packages.find((entry) =>
          entry.comment?.endsWith(`install path: ${installPath}`),
        )?.SPDXID;
  const dependsOn = (parentInstallPath, childInstallPath) =>
    document.relationships.some(
      (entry) =>
        entry.relationshipType === "DEPENDS_ON" &&
        entry.spdxElementId === packageIdForPath(parentInstallPath) &&
        entry.relatedSpdxElement === packageIdForPath(childInstallPath),
    );

  assert.ok(dependsOn("", "minimatch-modern"));
  assert.ok(dependsOn("minimatch-modern", "minimatch-modern/brace-expansion"));
  assert.ok(dependsOn("brace-expansion", "brace-expansion-next"));
  assert.ok(dependsOn("image-size", "image-size-next"));
  assert.equal(
    document.relationships.some(
      (entry) =>
        entry.relationshipType === "DEPENDS_ON" &&
        entry.spdxElementId === packageIdForPath("wrapper"),
    ),
    false,
  );
});

test("Squirrel RELEASES binds the exact nupkg identity", () => {
  const nupkg = {
    bytes: 140201271,
    fileName: "leapview-0.1.0-full.nupkg",
    sha1: "7d0e39642527d2b6c790737cf73b77707f487461",
  };
  assert.doesNotThrow(() =>
    validateSquirrelReleaseIndex(
      "\uFEFF7D0E39642527D2B6C790737CF73B77707F487461 leapview-0.1.0-full.nupkg 140201271",
      nupkg,
    ),
  );
  assert.throws(
    () =>
      validateSquirrelReleaseIndex(
        "7D0E39642527D2B6C790737CF73B77707F487461 leapview-0.1.0-full.nupkg 1",
        nupkg,
      ),
    /exact nupkg/,
  );
});

test("release evidence verification detects artifact, SBOM, and publication tampering", async () => {
  const directory = await mkdtemp(join(tmpdir(), "leapview-evidence-test-"));
  const artifactPath = join(directory, "LeapView-darwin-arm64-0.1.0.dmg");
  const updateArtifactPath = join(
    directory,
    "LeapView-darwin-arm64-0.1.0.zip",
  );
  const sbomPath = join(directory, "release.spdx.json");
  const manifestPath = join(directory, "release.json");
  const checksumsPath = join(directory, "checksums.txt");
  await writeFile(artifactPath, "candidate");
  await writeFile(updateArtifactPath, "update-candidate");

  const files = [
    {
      path: "LeapView.app/Contents/Resources/app.asar",
      sha1: "2".repeat(40),
      sha256: "b".repeat(64),
      type: "ARCHIVE",
    },
  ];
  const sbom = buildSpdxDocument({
    artifactSha256:
      "dda18a0e21ae47c53b4309434cbc02ae8bf764fa83a6defbb719431242722aa7",
    createdAt: "2026-07-29T12:00:00.000Z",
    files,
    lock: { workspaces: { "": { devDependencies: {} } }, packages: {} },
    packageDocument,
    packageVerification,
    sourceSha: "d".repeat(40),
  });
  await writeFile(sbomPath, `${JSON.stringify(sbom, null, 2)}\n`);

  const manifest = await createReleaseManifest({
    artifactPath,
    createdAt: "2026-07-29T12:00:00.000Z",
    lockfileSha256: "e".repeat(64),
    packageDocument,
    packageDocumentSha256: "f".repeat(64),
    packageVerification,
    policy,
    policySha256: "0".repeat(64),
    sbomPath,
    source: {
      commit: "d".repeat(40),
      repository: "flidai/leapview",
      workflowRef:
        "flidai/leapview/.github/workflows/electron-security-proof.yml@refs/heads/main",
      workflowRevision: "d".repeat(40),
      runId: "123",
      runAttempt: "1",
      dirty: false,
    },
    channel: "pull-request",
    updateArtifactPaths: [updateArtifactPath],
  });
  assert.deepEqual(manifest.updateArtifacts, [
    {
      fileName: basename(updateArtifactPath),
      format: "zip",
      bytes: 16,
      sha256:
        "75fd7b42226a9a5e50257a990d79770186e87e2891b035cc6c93de8a0422b757",
    },
  ]);
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  const manifestSha256 = createHash("sha256")
    .update(await readFile(manifestPath))
    .digest("hex");
  const checksumsFor = (releaseManifestSha256) =>
    [
      manifest.artifact,
      ...manifest.updateArtifacts,
      manifest.sbom,
      {
        fileName: basename(manifestPath),
        sha256: releaseManifestSha256,
      },
    ]
      .map((identity) => `${identity.sha256} *${identity.fileName}\n`)
      .join("");
  await writeFile(
    checksumsPath,
    checksumsFor(manifestSha256),
  );

  await assert.doesNotReject(() =>
    verifyReleaseEvidence({
      artifactPath,
      checksumsPath,
      manifestPath,
      policy,
      sbomPath,
      updateArtifactPaths: [updateArtifactPath],
    }),
  );
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        sbomPath,
      }),
    /update artifact set is incomplete/,
  );
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy: { ...policy, channel: "enterprise" },
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /consumer release policy/,
  );
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        publication: true,
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /signed release/,
  );

  const injected = { ...manifest, instanceOrigin: "https://tenant.invalid" };
  await writeFile(manifestPath, `${JSON.stringify(injected, null, 2)}\n`);
  const injectedManifestSha256 = createHash("sha256")
    .update(await readFile(manifestPath))
    .digest("hex");
  await writeFile(
    checksumsPath,
    checksumsFor(injectedManifestSha256),
  );
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /unexpected fields/,
  );

  const inaccessible = structuredClone(manifest);
  inaccessible.packageVerification.accessibility = {
    mode: "open",
    announcement: "none",
    controls: 0,
    focusedControl: "",
    regions: [],
  };
  await writeFile(
    manifestPath,
    `${JSON.stringify(inaccessible, null, 2)}\n`,
  );
  const inaccessibleManifestSha256 = createHash("sha256")
    .update(await readFile(manifestPath))
    .digest("hex");
  await writeFile(
    checksumsPath,
    checksumsFor(inaccessibleManifestSha256),
  );
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /immutable release policy/,
  );

  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(
    checksumsPath,
    checksumsFor(manifestSha256),
  );
  await writeFile(updateArtifactPath, "tampered");
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /update artifact checksum/,
  );
  await writeFile(updateArtifactPath, "update-candidate");
  await writeFile(artifactPath, "tampered");
  await assert.rejects(
    () =>
      verifyReleaseEvidence({
        artifactPath,
        checksumsPath,
        manifestPath,
        policy,
        sbomPath,
        updateArtifactPaths: [updateArtifactPath],
      }),
    /artifact checksum/,
  );
});
