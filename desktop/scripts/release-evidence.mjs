import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  copyFile,
  lstat,
  mkdir,
  readFile,
  readdir,
  readlink,
  stat,
  writeFile,
} from "node:fs/promises";
import { basename, join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { parse as parseJSONC, printParseErrorCode } from "jsonc-parser";

import { verifyReleaseEvidence } from "./verify-release-evidence.mjs";

const exactVersionPattern = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
const exactRuntimeVersionPattern =
  /^\d+\.\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.-]+)?$/;
const shaPattern = /^[0-9a-f]{40}$/;
const vendoredResolutionPattern = /^file:vendor\/[0-9A-Za-z][0-9A-Za-z._-]*$/;

export function validateReleasePolicy(policy, packageDocument) {
  if (policy?.schemaVersion !== 2) {
    throw new Error("release policy must use schema version 2");
  }
  if (
    policy.applicationVersion !== packageDocument.version ||
    policy.channel !== "consumer-v1" ||
    !validDistribution(policy.distribution)
  ) {
    throw new Error("release policy does not match the application package");
  }
  const electron = packageDocument.devDependencies?.electron;
  if (!exactVersionPattern.test(electron ?? "")) {
    throw new Error("desktop package must pin an exact Electron version");
  }
  if (electron !== policy.runtime?.electron) {
    throw new Error("release policy does not match the exact Electron version");
  }
  const electronMajor = Number.parseInt(electron.split(".")[0], 10);
  if (electronMajor !== policy.runtime.electronMajor) {
    throw new Error("release policy Electron major is unsupported");
  }
  if (
    policy.updates?.origin !== "https://releases.leapview.dev" ||
    policy.updates?.pathVersion !== "v1" ||
    policy.updates?.channel !== "stable" ||
    policy.updates?.productName !== packageDocument.productName ||
    policy.updates?.applicationId !== "dev.leapview.desktop" ||
    policy.updates?.electronMajor !== electronMajor ||
    policy.updates?.windowsPackageId !== "leapview"
  ) {
    throw new Error("release policy updater identity is invalid");
  }
  for (const [dependency, expected] of [
    ["node", policy.runtime.node],
    ["@electron-forge/cli", policy.runtime.forge],
  ]) {
    if (
      !exactVersionPattern.test(
        packageDocument.devDependencies?.[dependency] ?? "",
      ) ||
      packageDocument.devDependencies[dependency] !== expected
    ) {
      throw new Error(
        `desktop package must pin the policy ${dependency} version`,
      );
    }
  }
  if (
    !exactRuntimeVersionPattern.test(policy.runtime.chromium ?? "") ||
    !exactRuntimeVersionPattern.test(policy.runtime.bun ?? "")
  ) {
    throw new Error("release policy runtime versions must be exact");
  }
  const platforms = new Set();
  for (const support of policy.supportMatrix ?? []) {
    if (
      !["darwin", "linux", "win32"].includes(support.platform) ||
      platforms.has(support.platform) ||
      !Array.isArray(support.architectures) ||
      support.architectures.length === 0 ||
      new Set(support.architectures).size !== support.architectures.length ||
      typeof support.minimumVersion !== "string" ||
      support.minimumVersion.length === 0
    ) {
      throw new Error("release policy support matrix is invalid");
    }
    platforms.add(support.platform);
  }
  if (platforms.size !== 3) {
    throw new Error("release policy must cover every desktop platform");
  }
  if (
    policy.hardening?.asarOnly !== true ||
    policy.publication?.codeSigningRequired !== true ||
    policy.publication?.githubAttestationsRequired !== true ||
    policy.publication?.immutableArtifactsRequired !== true ||
    Object.values(policy.privacy ?? {}).some((value) => value !== false)
  ) {
    throw new Error(
      "release policy security or privacy requirements are invalid",
    );
  }
}

export function parseBunLock(lockText) {
  const errors = [];
  const parsed = parseJSONC(lockText, errors, { allowTrailingComma: true });
  if (errors.length > 0) {
    const details = errors
      .map(
        ({ error, offset }) =>
          `${printParseErrorCode(error)} at byte offset ${offset}`,
      )
      .join(", ");
    throw new Error(`could not parse bun.lock: ${details}`);
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("could not parse bun.lock: root must be an object");
  }
  return parsed;
}

export function buildSpdxDocument({
  artifactSha256,
  createdAt,
  files,
  lock,
  packageDocument,
  packageVerification,
  sourceSha,
}) {
  const lockPackages = Object.entries(lock?.packages ?? {}).sort(
    ([left], [right]) => left.localeCompare(right),
  );
  const packageIds = new Map(
    lockPackages.map(([installPath]) => [
      installPath,
      `SPDXRef-Dependency-${shortHash(installPath)}`,
    ]),
  );
  const parsedPackages = lockPackages.map(([installPath, entry]) => {
    const { name, version } = parseResolution(
      entry?.[0],
      installPath,
      sourceSha,
    );
    const integrity = entry?.at(-1);
    const checksums =
      typeof integrity === "string" && integrity.startsWith("sha512-")
        ? [
            {
              algorithm: "SHA512",
              checksumValue: Buffer.from(
                integrity.slice("sha512-".length),
                "base64",
              )
                .toString("hex")
                .toUpperCase(),
            },
          ]
        : undefined;
    return compactObject({
      SPDXID: packageIds.get(installPath),
      name,
      versionInfo: version,
      downloadLocation: "NOASSERTION",
      filesAnalyzed: false,
      licenseConcluded: "NOASSERTION",
      licenseDeclared: "NOASSERTION",
      copyrightText: "NOASSERTION",
      primaryPackagePurpose: "LIBRARY",
      checksums,
      externalRefs: isRegistryVersion(version)
        ? [
            {
              referenceCategory: "PACKAGE-MANAGER",
              referenceType: "purl",
              referenceLocator: `pkg:npm/${encodePurlName(name)}@${encodeURIComponent(version)}`,
            },
          ]
        : undefined,
      comment: `Bun lock install path: ${installPath}`,
    });
  });
  const fileEntries = files.map((file) => ({
    SPDXID: `SPDXRef-File-${shortHash(file.path)}`,
    fileName: `./${file.path}`,
    checksums: [
      { algorithm: "SHA1", checksumValue: file.sha1.toUpperCase() },
      { algorithm: "SHA256", checksumValue: file.sha256.toUpperCase() },
    ],
    fileTypes: [file.type],
    licenseConcluded: "NOASSERTION",
    licenseInfoInFiles: ["NOASSERTION"],
    copyrightText: "NOASSERTION",
  }));
  const rootId = "SPDXRef-Package-LeapView-Desktop";
  const runtimePackages = [
    ["Electron", packageVerification.runtime.electron, "FRAMEWORK"],
    ["Chromium", packageVerification.runtime.chromium, "LIBRARY"],
    ["Node.js", packageVerification.runtime.node, "LIBRARY"],
  ].map(([name, version, purpose]) => ({
    SPDXID: `SPDXRef-Runtime-${name.replaceAll(/[^A-Za-z0-9.-]/g, "-")}`,
    name,
    versionInfo: version,
    downloadLocation: "NOASSERTION",
    filesAnalyzed: false,
    licenseConcluded: "NOASSERTION",
    licenseDeclared: "NOASSERTION",
    copyrightText: "NOASSERTION",
    primaryPackagePurpose: purpose,
    comment: "Runtime embedded in the packaged Electron application.",
  }));
  const relationships = [
    {
      spdxElementId: "SPDXRef-DOCUMENT",
      relationshipType: "DESCRIBES",
      relatedSpdxElement: rootId,
    },
    ...runtimePackages.map((runtimePackage) => ({
      spdxElementId: rootId,
      relationshipType: "CONTAINS",
      relatedSpdxElement: runtimePackage.SPDXID,
    })),
    ...fileEntries.map((file) => ({
      spdxElementId: rootId,
      relationshipType: "CONTAINS",
      relatedSpdxElement: file.SPDXID,
    })),
  ];
  const dependencyInstallPaths = buildDependencyInstallPathIndex(lockPackages);
  for (const [installPath, entry] of lockPackages) {
    const dependencyObject = entry?.find(
      (candidate) =>
        candidate &&
        typeof candidate === "object" &&
        !Array.isArray(candidate) &&
        candidate.dependencies,
    );
    for (const dependencyName of Object.keys(
      dependencyObject?.dependencies ?? {},
    ).sort()) {
      const relatedInstallPath = resolveLockedDependency(
        dependencyName,
        installPath,
        dependencyInstallPaths,
      );
      if (relatedInstallPath !== undefined) {
        relationships.push({
          spdxElementId: packageIds.get(installPath),
          relationshipType: "DEPENDS_ON",
          relatedSpdxElement: packageIds.get(relatedInstallPath),
        });
      }
    }
  }
  for (const dependencyName of Object.keys(
    lock?.workspaces?.[""]?.devDependencies ?? {},
  ).sort()) {
    const relatedInstallPath = resolveLockedDependency(
      dependencyName,
      "",
      dependencyInstallPaths,
    );
    if (relatedInstallPath !== undefined) {
      relationships.push({
        spdxElementId: rootId,
        relationshipType: "DEPENDS_ON",
        relatedSpdxElement: packageIds.get(relatedInstallPath),
      });
    }
  }

  const packageVerificationCode = createHash("sha1")
    .update(
      files
        .map((file) => file.sha1.toLowerCase())
        .sort()
        .join(""),
    )
    .digest("hex")
    .toUpperCase();
  return {
    spdxVersion: "SPDX-2.3",
    dataLicense: "CC0-1.0",
    SPDXID: "SPDXRef-DOCUMENT",
    name: `LeapView Desktop ${packageDocument.version} ${packageVerification.platform}-${packageVerification.architecture}`,
    documentNamespace: `https://leapview.dev/spdx/${sourceSha}/${packageVerification.platform}-${packageVerification.architecture}/${artifactSha256}`,
    creationInfo: {
      created: createdAt,
      creators: ["Organization: LeapView", "Tool: leapview-release-evidence/1"],
    },
    packages: [
      {
        SPDXID: rootId,
        name: packageDocument.name,
        versionInfo: packageDocument.version,
        downloadLocation: "NOASSERTION",
        filesAnalyzed: true,
        packageVerificationCode: {
          packageVerificationCodeValue: packageVerificationCode,
        },
        licenseConcluded: "NOASSERTION",
        licenseDeclared: "NOASSERTION",
        copyrightText: "NOASSERTION",
        primaryPackagePurpose: "APPLICATION",
        checksums: [
          {
            algorithm: "SHA256",
            checksumValue: artifactSha256.toUpperCase(),
          },
        ],
      },
      ...runtimePackages,
      ...parsedPackages,
    ],
    files: fileEntries,
    relationships,
  };
}

export async function createReleaseManifest({
  artifactPath,
  channel,
  createdAt,
  lockfileSha256,
  packageDocument,
  packageDocumentSha256,
  packageVerification,
  policy,
  policySha256,
  sbomPath,
  source,
  updateArtifactPaths,
}) {
  const artifact = await fileIdentity(artifactPath);
  const sbom = await fileIdentity(sbomPath);
  const expectedUpdateFormats =
    policy.distribution?.[packageVerification.platform]?.updateArtifacts;
  if (
    !Array.isArray(updateArtifactPaths) ||
    !Array.isArray(expectedUpdateFormats) ||
    updateArtifactPaths.length !== expectedUpdateFormats.length
  ) {
    throw new Error(
      "release manifest requires every declared update artifact",
    );
  }
  const updateArtifacts = await Promise.all(
    updateArtifactPaths.map(async (path, index) => {
      const identity = await fileIdentity(path);
      return {
        fileName: basename(path),
        format: expectedUpdateFormats[index],
        bytes: identity.bytes,
        sha256: identity.sha256,
      };
    }),
  );
  if (
    new Set(updateArtifacts.map((artifact) => artifact.fileName)).size !==
      updateArtifacts.length ||
    updateArtifacts.some(
      (artifact) =>
        !matchesUpdateArtifactFormat(artifact.fileName, artifact.format),
    )
  ) {
    throw new Error("release update artifact identity is invalid");
  }
  const support = policy.supportMatrix.find(
    (candidate) => candidate.platform === packageVerification.platform,
  );
  if (
    support === undefined ||
    !support.architectures.includes(packageVerification.architecture)
  ) {
    throw new Error("packaged target is outside the release support matrix");
  }
  assertPackageVerification(packageVerification, policy);
  validateSource(source);
  return {
    schemaVersion: 2,
    application: {
      name: packageDocument.productName,
      packageName: packageDocument.name,
      version: packageDocument.version,
    },
    channel,
    createdAt,
    source,
    toolchain: policy.runtime,
    artifact: {
      fileName: basename(artifactPath),
      format: packageVerification.packageFormat,
      platform: packageVerification.platform,
      architecture: packageVerification.architecture,
      bytes: artifact.bytes,
      sha256: artifact.sha256,
    },
    updateArtifacts,
    sbom: {
      fileName: basename(sbomPath),
      format: "SPDX-2.3-json",
      sha256: sbom.sha256,
    },
    reproducibility: {
      lockfileSha256,
      packageDocumentSha256,
      policySha256,
      sourceDateEpoch: Math.floor(new Date(createdAt).getTime() / 1000),
    },
    support: {
      minimumVersion: support.minimumVersion,
      qualification: "candidate",
    },
    packageVerification,
    signing: {
      state: "unsigned-candidate",
      productionEligible: false,
      identity: null,
    },
    attestations: {
      provider: "GitHub artifact attestations",
      provenanceRequiredForPublication:
        policy.publication.githubAttestationsRequired,
      sbomRequiredForPublication: policy.publication.githubAttestationsRequired,
      generatedForMainCandidates: true,
    },
    privacy: policy.privacy,
  };
}

async function generate() {
  const root = resolve(import.meta.dirname, "..");
  const out = join(root, "out");
  const evidenceDirectory = join(out, "evidence");
  const packageDocumentPath = join(root, "package.json");
  const lockfilePath = join(root, "bun.lock");
  const policyPath = join(root, "release-policy.json");
  const verificationPath = join(out, "package-verification.json");
  const [packageText, lockText, policyText, verificationText] =
    await Promise.all([
      readFile(packageDocumentPath, "utf8"),
      readFile(lockfilePath, "utf8"),
      readFile(policyPath, "utf8"),
      readFile(verificationPath, "utf8"),
    ]);
  const packageDocument = JSON.parse(packageText);
  const policy = JSON.parse(policyText);
  const packageVerification = JSON.parse(verificationText);
  parseBunLock(lockText);
  validateReleasePolicy(policy, packageDocument);
  assertPackageVerification(packageVerification, policy);

  const packageFormat =
    policy.distribution[packageVerification.platform].installer;
  const artifacts = await findFiles(join(out, "make"), (path) =>
    path.toLowerCase().endsWith(`.${packageFormat}`),
  );
  if (artifacts.length !== 1) {
    throw new Error(
      `expected exactly one ${packageFormat} installer, found ${artifacts.length}`,
    );
  }
  const updateArtifactPaths = [];
  for (const format of policy.distribution[
    packageVerification.platform
  ].updateArtifacts) {
    const matches = await findFiles(join(out, "make"), (path) =>
      matchesUpdateArtifactFormat(basename(path), format),
    );
    if (matches.length !== 1) {
      throw new Error(
        `expected exactly one ${format} update artifact, found ${matches.length}`,
      );
    }
    updateArtifactPaths.push(matches[0]);
  }
  const packageRoots = (await readdir(out, { withFileTypes: true }))
    .filter(
      (entry) =>
        entry.isDirectory() &&
        entry.name.startsWith(
          `LeapView-${packageVerification.platform}-${packageVerification.architecture}`,
        ),
    )
    .map((entry) => join(out, entry.name));
  if (packageRoots.length !== 1) {
    throw new Error(
      `expected exactly one packaged application, found ${packageRoots.length}`,
    );
  }
  const files = await inventoryPackage(packageRoots[0]);
  const artifactIdentity = await fileIdentity(artifacts[0]);
  const source = resolveSourceIdentity();
  const createdAt = resolveCreationTime();
  const sbom = buildSpdxDocument({
    artifactSha256: artifactIdentity.sha256,
    createdAt,
    files,
    lock: parsedLock.config,
    packageDocument,
    packageVerification,
    sourceSha: source.commit,
  });
  await mkdir(evidenceDirectory, { recursive: true });
  const stem = `leapview-desktop-${packageVerification.platform}-${packageVerification.architecture}-${packageDocument.version}`;
  const sbomPath = join(evidenceDirectory, `${stem}.spdx.json`);
  const manifestPath = join(evidenceDirectory, `${stem}.release.json`);
  const checksumsPath = join(evidenceDirectory, "checksums.txt");
  await writeFile(sbomPath, `${JSON.stringify(sbom, null, 2)}\n`);
  const manifest = await createReleaseManifest({
    artifactPath: artifacts[0],
    channel: resolveChannel(),
    createdAt,
    lockfileSha256: sha256(lockText),
    packageDocument,
    packageDocumentSha256: sha256(packageText),
    packageVerification,
    policy,
    policySha256: sha256(policyText),
    sbomPath,
    source,
    updateArtifactPaths,
  });
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  const manifestIdentity = await fileIdentity(manifestPath);
  await writeFile(
    checksumsPath,
    releaseChecksums(
      manifest,
      basename(manifestPath),
      manifestIdentity.sha256,
    ),
  );
  await copyFile(
    join(import.meta.dirname, "verify-release-evidence.mjs"),
    join(evidenceDirectory, "verify-release-evidence.mjs"),
  );
  await verifyReleaseEvidence({
    artifactPath: artifacts[0],
    checksumsPath,
    manifestPath,
    policy,
    policySha256: sha256(policyText),
    sbomPath,
    updateArtifactPaths,
  });
  process.stdout.write(
    `${JSON.stringify({
      artifact: relative(root, artifacts[0]).replaceAll("\\", "/"),
      updateArtifacts: updateArtifactPaths.map((path) =>
        relative(root, path).replaceAll("\\", "/"),
      ),
      files: files.length,
      manifest: relative(root, manifestPath).replaceAll("\\", "/"),
      packages: sbom.packages.length,
      sbom: relative(root, sbomPath).replaceAll("\\", "/"),
      status: "verified-unsigned-candidate",
    })}\n`,
  );
}

function releaseChecksums(manifest, manifestFileName, manifestSha256) {
  return [
    manifest.artifact,
    ...manifest.updateArtifacts,
    manifest.sbom,
    { fileName: manifestFileName, sha256: manifestSha256 },
  ]
    .map((identity) => `${identity.sha256} *${identity.fileName}\n`)
    .join("");
}

function matchesUpdateArtifactFormat(fileName, format) {
  if (format === "RELEASES") {
    return fileName === "RELEASES";
  }
  return fileName.toLowerCase().endsWith(`.${format}`);
}

function assertPackageVerification(verification, policy) {
  if (
    verification?.schemaVersion !== 2 ||
    verification.packageFormat !==
      policy.distribution?.[verification.platform]?.installer ||
    verification.asarOnly !== policy.hardening.asarOnly ||
    verification.startup !== "trusted-shell-ready" ||
    !validAccessibilityVerification(verification.accessibility)
  ) {
    throw new Error("package verification report is incomplete");
  }
  if (
    verification.installer?.format !== verification.packageFormat ||
    verification.installer?.scope !==
      policy.distribution?.[verification.platform]?.scope ||
    verification.installer?.updateMechanism !==
      policy.distribution?.[verification.platform]?.updateMechanism ||
    JSON.stringify(verification.installer?.updateArtifacts) !==
      JSON.stringify(
        policy.distribution?.[verification.platform]?.updateArtifacts,
      ) ||
    verification.installer?.policyIntegration !==
      "deferred-not-supported" ||
    verification.installer?.protocolIntegration !==
      "consumer-owned-validated-url" ||
    verification.updates?.origin !== policy.updates.origin ||
    verification.updates?.pathVersion !== policy.updates.pathVersion ||
    verification.updates?.channel !== policy.updates.channel ||
    verification.updates?.productName !== policy.updates.productName ||
    verification.updates?.applicationId !== policy.updates.applicationId ||
    verification.updates?.electronMajor !==
      policy.updates.electronMajor ||
    verification.updates?.delivery !==
      (verification.platform === "linux"
        ? "system-package-manager"
        : "electron-auto-updater")
  ) {
    throw new Error("installer verification report is incomplete");
  }
  for (const runtime of ["electron", "chromium", "node"]) {
    if (verification.runtime?.[runtime] !== policy.runtime[runtime]) {
      throw new Error(
        `packaged ${runtime} version does not match release policy`,
      );
    }
  }
  for (const [name, expected] of Object.entries(policy.hardening.fuses)) {
    const platformExpected =
      typeof expected === "string" ? expected : expected[verification.platform];
    if (verification.fuses?.[name] !== platformExpected) {
      throw new Error(`packaged Electron fuse ${name} does not match policy`);
    }
  }
}

function validDistribution(distribution) {
  const expected = {
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
  };
  return JSON.stringify(distribution) === JSON.stringify(expected);
}

function validAccessibilityVerification(accessibility) {
  if (
    accessibility === null ||
    typeof accessibility !== "object" ||
    !Array.isArray(accessibility.regions) ||
    !Number.isSafeInteger(accessibility.controls) ||
    accessibility.controls < 0
  ) {
    return false;
  }
  if (accessibility.mode === "open") {
    return (
      accessibility.announcement === "none" &&
      accessibility.controls >= 2 &&
      accessibility.focusedControl === "LeapView URL" &&
      accessibility.regions.includes("Connect an instance")
    );
  }
  return (
    accessibility.mode === "locked" &&
    accessibility.announcement === "assertive" &&
    accessibility.controls === 0 &&
    [
      "Application document",
      "Managed configuration error",
    ].includes(accessibility.focusedControl)
  );
}

async function inventoryPackage(root) {
  const inventory = [];
  async function visit(directory) {
    const entries = await readdir(directory, { withFileTypes: true });
    entries.sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const path = join(directory, entry.name);
      const metadata = await lstat(path);
      if (metadata.isDirectory()) {
        await visit(path);
      } else {
        const normalized = relative(root, path).replaceAll("\\", "/");
        if (metadata.isSymbolicLink()) {
          const target = await readlink(path);
          inventory.push({
            path: normalized,
            sha1: digest("sha1", target),
            sha256: digest("sha256", target),
            type: "OTHER",
          });
        } else if (metadata.isFile()) {
          const content = await readFile(path);
          inventory.push({
            path: normalized,
            sha1: digest("sha1", content),
            sha256: digest("sha256", content),
            type: classifyFile(normalized),
          });
        }
      }
    }
  }
  await visit(root);
  return inventory;
}

async function findFiles(root, predicate) {
  const matches = [];
  async function visit(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) {
        await visit(path);
      } else if (entry.isFile() && predicate(path)) {
        matches.push(path);
      }
    }
  }
  await visit(root);
  return matches.sort();
}

async function fileIdentity(path) {
  const [content, metadata] = await Promise.all([readFile(path), stat(path)]);
  return { bytes: metadata.size, sha256: digest("sha256", content) };
}

function resolveSourceIdentity() {
  const commit = process.env.GITHUB_SHA ?? git("rev-parse", "HEAD");
  const workflowRevision = process.env.GITHUB_WORKFLOW_SHA ?? commit;
  const repository = process.env.GITHUB_REPOSITORY ?? "flidai/leapview";
  const source = {
    commit,
    repository,
    workflowRef:
      process.env.GITHUB_WORKFLOW_REF ??
      `${repository}/.github/workflows/electron-security-proof.yml@local`,
    workflowRevision,
    runId: process.env.GITHUB_RUN_ID ?? "local",
    runAttempt: process.env.GITHUB_RUN_ATTEMPT ?? "1",
    dirty: git("status", "--porcelain", "--untracked-files=no").length > 0,
  };
  validateSource(source);
  if (process.env.CI === "true" && source.dirty) {
    throw new Error("CI release evidence requires a clean source checkout");
  }
  return source;
}

function validateSource(source) {
  if (
    !shaPattern.test(source?.commit ?? "") ||
    !shaPattern.test(source?.workflowRevision ?? "") ||
    !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(source?.repository ?? "") ||
    typeof source.workflowRef !== "string" ||
    source.workflowRef.length === 0 ||
    typeof source.runId !== "string" ||
    source.runId.length === 0 ||
    typeof source.runAttempt !== "string" ||
    source.runAttempt.length === 0 ||
    typeof source.dirty !== "boolean"
  ) {
    throw new Error("release source identity is incomplete");
  }
}

function resolveCreationTime() {
  const epoch =
    process.env.SOURCE_DATE_EPOCH ?? git("show", "-s", "--format=%ct", "HEAD");
  if (!/^\d+$/.test(epoch)) {
    throw new Error("SOURCE_DATE_EPOCH must be an integer");
  }
  return new Date(Number(epoch) * 1000).toISOString();
}

function resolveChannel() {
  if (process.env.GITHUB_EVENT_NAME === "pull_request") {
    return "pull-request";
  }
  if (process.env.GITHUB_EVENT_NAME === "push") {
    return "main-candidate";
  }
  return "local-candidate";
}

function buildDependencyInstallPathIndex(lockPackages) {
  return new Set(lockPackages.map(([installPath]) => installPath));
}

function resolveLockedDependency(name, parentInstallPath, index) {
  let scope = parentInstallPath;
  while (scope.length > 0) {
    const scopedInstallPath = `${scope}/${name}`;
    if (index.has(scopedInstallPath)) {
      return scopedInstallPath;
    }
    const separator = scope.lastIndexOf("/");
    scope = separator >= 0 ? scope.slice(0, separator) : "";
  }
  return index.has(name) ? name : undefined;
}

function parseResolution(resolution, installPath, sourceSha) {
  if (typeof resolution !== "string" || resolution.length === 0) {
    throw new Error(
      `bun.lock package ${installPath} has no immutable resolution`,
    );
  }
  const separator = resolution.lastIndexOf("@");
  if (separator <= 0 || separator === resolution.length - 1) {
    throw new Error(
      `bun.lock package ${installPath} has an invalid resolution`,
    );
  }
  return {
    name: resolution.slice(0, separator),
    version: assertImmutableResolution(
      resolution.slice(separator + 1),
      installPath,
      sourceSha,
    ),
  };
}

function assertImmutableResolution(version, installPath, sourceSha) {
  if (vendoredResolutionPattern.test(version) && shaPattern.test(sourceSha)) {
    return `${version}#${sourceSha}`;
  }
  if (
    !exactRuntimeVersionPattern.test(version) &&
    !/(?:#|[-])(?:[0-9a-f]{7}|[0-9a-f]{40})$/iu.test(version)
  ) {
    throw new Error(
      `bun.lock package ${installPath} has a mutable resolution ${version}`,
    );
  }
  return version;
}

function classifyFile(path) {
  if (path.endsWith(".asar") || path.endsWith(".zip")) {
    return "ARCHIVE";
  }
  if (
    path.endsWith(".exe") ||
    path.endsWith(".dll") ||
    path.endsWith(".dylib") ||
    path.endsWith(".so") ||
    path.includes("/MacOS/") ||
    !basename(path).includes(".")
  ) {
    return "BINARY";
  }
  if (/\.(?:html|json|md|plist|txt|xml)$/i.test(path)) {
    return "TEXT";
  }
  return "OTHER";
}

function encodePurlName(name) {
  return name.startsWith("@")
    ? name
        .split("/")
        .map((part) => encodeURIComponent(part))
        .join("/")
    : encodeURIComponent(name);
}

function isRegistryVersion(version) {
  return exactVersionPattern.test(version);
}

function compactObject(value) {
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => entry !== undefined),
  );
}

function shortHash(value) {
  return createHash("sha256").update(value).digest("hex").slice(0, 24);
}

function digest(algorithm, value) {
  return createHash(algorithm).update(value).digest("hex");
}

function sha256(value) {
  return digest("sha256", value);
}

function git(...args) {
  return execFileSync("git", args, {
    cwd: resolve(import.meta.dirname, "../.."),
    encoding: "utf8",
  }).trim();
}

if (
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  await generate();
}
