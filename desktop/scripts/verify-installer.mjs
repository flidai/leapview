import { execFile } from "node:child_process";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import { promisify } from "node:util";
import { pathToFileURL } from "node:url";

const execFileAsync = promisify(execFile);
const distributionByPlatform = {
  darwin: {
    format: "dmg",
    scope: "user-installed",
    updateArtifacts: ["zip"],
    updateMechanism: "squirrel-mac",
  },
  linux: {
    format: "deb",
    scope: "system-package-manager",
    updateArtifacts: [],
    updateMechanism: "apt",
  },
  win32: {
    format: "exe",
    scope: "per-user",
    updateArtifacts: ["nupkg", "RELEASES"],
    updateMechanism: "squirrel-windows",
  },
};

export function validateInstallerContract({
  format,
  platform,
  policyIntegration,
  protocolIntegration,
  scope,
  updateArtifacts,
  updateMechanism,
}) {
  const expected = distributionByPlatform[platform];
  if (
    expected === undefined ||
    format !== expected.format ||
    scope !== expected.scope ||
    JSON.stringify(updateArtifacts) !==
      JSON.stringify(expected.updateArtifacts) ||
    updateMechanism !== expected.updateMechanism ||
    policyIntegration !== "deferred-not-supported" ||
    protocolIntegration !== "consumer-owned-validated-url"
  ) {
    throw new Error("production installer contract is incomplete");
  }
  return {
    format,
    scope,
    policyIntegration,
    protocolIntegration,
    updateArtifacts: [...updateArtifacts],
    updateMechanism,
  };
}

export function squirrelArchiveArguments(archive, destination) {
  return ["--force-local", "-xf", archive, "-C", destination];
}

async function main() {
  const desktopRoot = resolve(import.meta.dirname, "..");
  const out = join(desktopRoot, "out");
  const verificationPath = join(out, "package-verification.json");
  const verification = JSON.parse(
    await readFile(verificationPath, "utf8"),
  );
  const format = distributionByPlatform[process.platform]?.format;
  if (
    format === undefined ||
    verification.platform !== process.platform ||
    verification.packageFormat !== format
  ) {
    throw new Error("installer target does not match package verification");
  }
  const artifacts = await findFiles(
    join(out, "make"),
    (path) => path.toLowerCase().endsWith(`.${format}`),
  );
  if (artifacts.length !== 1) {
    throw new Error(
      `expected exactly one ${format} installer, found ${artifacts.length}`,
    );
  }
  const updateArtifacts = await inspectUpdateArtifacts(
    join(out, "make"),
    process.platform,
  );
  const inspection =
    process.platform === "darwin"
      ? await inspectMacOSInstaller(artifacts[0])
      : process.platform === "linux"
        ? await inspectDebianInstaller(artifacts[0])
        : await inspectWindowsInstaller(
            artifacts[0],
            join(out, "make"),
          );
  verification.installer = validateInstallerContract({
    format,
    platform: process.platform,
    updateArtifacts,
    ...inspection,
  });
  await writeFile(
    verificationPath,
    `${JSON.stringify(verification, null, 2)}\n`,
  );
  process.stdout.write(
    `${JSON.stringify({
      artifact: basename(artifacts[0]),
      ...verification.installer,
    })}\n`,
  );
}

async function inspectMacOSInstaller(artifact) {
  const temporary = await mkdtemp(
    join(tmpdir(), "leapview-dmg-inspection-"),
  );
  const mount = join(temporary, "mount");
  try {
    await mkdir(mount);
    await runFile("hdiutil", [
      "attach",
      "-readonly",
      "-nobrowse",
      "-mountpoint",
      mount,
      artifact,
    ]);
    const files = await findFiles(mount, () => true);
    const plist = files.find((path) =>
      path.endsWith("/LeapView.app/Contents/Info.plist"),
    );
    if (plist === undefined) {
      throw new Error("macOS consumer image is missing LeapView.app");
    }
    const plistBody = await readFile(plist, "utf8");
    if (
      !plistBody.includes("<string>leapview-desktop</string>") ||
      !plistBody.includes("<string>dev.leapview.desktop</string>")
    ) {
      throw new Error("macOS consumer image has an unsafe identity");
    }
    return {
      scope: "user-installed",
      policyIntegration: "deferred-not-supported",
      protocolIntegration: "consumer-owned-validated-url",
      updateMechanism: "squirrel-mac",
    };
  } finally {
    await runFile("hdiutil", ["detach", mount]).catch(() => undefined);
    await rm(temporary, { force: true, recursive: true });
  }
}

async function inspectDebianInstaller(artifact) {
  const temporary = await mkdtemp(
    join(tmpdir(), "leapview-deb-inspection-"),
  );
  const control = join(temporary, "control");
  const payload = join(temporary, "payload");
  try {
    await runFile("dpkg-deb", ["--control", artifact, control]);
    await runFile("dpkg-deb", ["--extract", artifact, payload]);
    const desktopEntry = await readFile(
      join(
        payload,
        "usr/share/applications/leapview-desktop.desktop",
      ),
      "utf8",
    );
    if (
      !desktopEntry.includes("Exec=leapview-desktop %U\n") ||
      !desktopEntry.includes(
        "MimeType=x-scheme-handler/leapview-desktop;\n",
      ) ||
      desktopEntry.includes("sh -c")
    ) {
      throw new Error("Debian installer has an unsafe desktop protocol");
    }
    return {
      scope: "system-package-manager",
      policyIntegration: "deferred-not-supported",
      protocolIntegration: "consumer-owned-validated-url",
      updateMechanism: "apt",
    };
  } finally {
    await rm(temporary, { force: true, recursive: true });
  }
}

async function inspectWindowsInstaller(artifact, makeRoot) {
  const temporary = await mkdtemp(
    join(tmpdir(), "leapview-squirrel-inspection-"),
  );
  const payload = join(temporary, "payload");
  try {
    const executable = await readFile(artifact);
    if (executable.subarray(0, 2).toString("ascii") !== "MZ") {
      throw new Error("Windows consumer installer is not a PE executable");
    }
    const packages = await findFiles(
      makeRoot,
      (path) => path.toLowerCase().endsWith(".nupkg"),
    );
    if (packages.length !== 1) {
      throw new Error(
        `expected exactly one Squirrel package, found ${packages.length}`,
      );
    }
    await mkdir(payload);
    // Git for Windows' GNU tar treats a drive letter in an absolute path as a
    // remote archive unless --force-local is provided. Without this flag the
    // post-package verification fails only on the Windows runner, before it
    // can inspect the Squirrel payload.
    await runFile("tar.exe", squirrelArchiveArguments(packages[0], payload));
    const files = await findFiles(payload, () => true);
    const specification = files.find((path) =>
      path.toLowerCase().endsWith(".nuspec"),
    );
    const application = files.find((path) =>
      path
        .replaceAll("\\", "/")
        .toLowerCase()
        .endsWith("/leapview.exe"),
    );
    if (specification === undefined || application === undefined) {
      throw new Error("Squirrel package payload is incomplete");
    }
    const specificationBody = await readFile(specification, "utf8");
    if (!specificationBody.includes("<id>leapview</id>")) {
      throw new Error("Squirrel package identity is unexpected");
    }
    return {
      scope: "per-user",
      policyIntegration: "deferred-not-supported",
      protocolIntegration: "consumer-owned-validated-url",
      updateMechanism: "squirrel-windows",
    };
  } finally {
    await rm(temporary, { force: true, recursive: true });
  }
}

async function inspectUpdateArtifacts(makeRoot, platform) {
  const expected = distributionByPlatform[platform]?.updateArtifacts;
  if (expected === undefined) {
    throw new Error("unsupported consumer platform");
  }
  const observed = [];
  for (const type of expected) {
    const matches = await findFiles(makeRoot, (path) =>
      type === "RELEASES"
        ? basename(path) === "RELEASES"
        : path.toLowerCase().endsWith(`.${type.toLowerCase()}`),
    );
    if (matches.length !== 1) {
      throw new Error(
        `expected exactly one ${type} update artifact, found ${matches.length}`,
      );
    }
    observed.push(type);
  }
  return observed;
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

async function runFile(executable, arguments_) {
  await execFileAsync(executable, arguments_, {
    encoding: "utf8",
    maxBuffer: 4 * 1024 * 1024,
    windowsHide: true,
  });
}

if (
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  await main();
}
