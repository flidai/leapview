import {
  access,
  mkdtemp,
  readdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { constants } from "node:fs";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

import { extractFile, listPackage } from "@electron/asar";
import {
  FuseState,
  FuseV1Options,
  FuseVersion,
  getCurrentFuseWire,
} from "@electron/fuses";
import {
  readTrustedShellAccessibility,
} from "./accessibility-contract.mjs";
import {
  verifyPackagedDiagnosticJournal,
} from "./packaged-diagnostics.mjs";
import { requirePackagedDistribution } from "./distribution-packaging.mjs";

const root = resolve(import.meta.dirname, "..");
const out = join(root, "out");
const packageDocument = JSON.parse(
  await readFile(join(root, "package.json"), "utf8"),
);
const releasePolicy = JSON.parse(
  await readFile(join(root, "release-policy.json"), "utf8"),
);
const platformName = {
  darwin: "darwin",
  linux: "linux",
  win32: "win32",
}[process.platform];
if (platformName === undefined) {
  throw new Error(`unsupported verification platform ${process.platform}`);
}
const candidates = (await readdir(out, { withFileTypes: true }))
  .filter(
    (entry) =>
      entry.isDirectory() && entry.name.startsWith(`LeapView-${platformName}-`),
  )
  .map((entry) => join(out, entry.name));
if (candidates.length !== 1) {
  throw new Error(
    `expected one packaged LeapView application, found ${candidates.length}`,
  );
}
const packageRoot = candidates[0];
const packagedElectronVersion = (
  await readFile(join(packageRoot, "version"), "utf8")
).trim();
if (
  packagedElectronVersion !== packageDocument.devDependencies.electron ||
  packagedElectronVersion !== releasePolicy.runtime.electron
) {
  throw new Error(
    `packaged Electron ${packagedElectronVersion} does not match the release policy`,
  );
}
const appPath =
  process.platform === "darwin"
    ? join(packageRoot, "LeapView.app")
    : process.platform === "win32"
      ? join(packageRoot, "LeapView.exe")
      : join(packageRoot, "LeapView");
const executablePath =
  process.platform === "darwin"
    ? join(appPath, "Contents", "MacOS", "LeapView")
    : appPath;
const resources =
  process.platform === "darwin"
    ? join(packageRoot, "LeapView.app", "Contents", "Resources")
    : join(packageRoot, "resources");
const asarPath = join(resources, "app.asar");
await access(appPath, constants.R_OK);
await access(asarPath, constants.R_OK);
await expectMissing(join(resources, "app"));
const packagedDistribution = requirePackagedDistribution(
  process.env.LEAPVIEW_DESKTOP_DISTRIBUTION,
);
const distributionMarkers = {
  preview: {
    filename: "preview-distribution.json",
    contents: '{"schemaVersion":1,"channel":"preview","updates":false}\n',
    other: "stable-distribution.json",
  },
  stable: {
    filename: "stable-distribution.json",
    contents: '{"schemaVersion":1,"channel":"stable","updates":true}\n',
    other: "preview-distribution.json",
  },
};
const expectedDistribution = distributionMarkers[packagedDistribution];
const marker = (await readFile(
  join(resources, expectedDistribution.filename),
  "utf8",
)).replace(/\r\n?/gu, "\n");
if (marker !== expectedDistribution.contents) {
  throw new Error(
    `packaged ${packagedDistribution} distribution marker is invalid`,
  );
}
await expectMissing(join(resources, expectedDistribution.other));
if (process.platform === "darwin") {
  const information = await readFile(
    join(appPath, "Contents", "Info.plist"),
    "utf8",
  );
  if (
    !information.includes("<string>LeapView Desktop</string>") ||
    !information.includes("<string>leapview-desktop</string>")
  ) {
    throw new Error(
      "packaged macOS application is missing the desktop URL handler",
    );
  }
}

const wire = await getCurrentFuseWire(appPath);
if (wire.version !== FuseVersion.V1) {
  throw new Error(`unexpected Electron fuse version ${wire.version}`);
}
const expectedFuses = new Map([
  [FuseV1Options.RunAsNode, FuseState.DISABLE],
  [FuseV1Options.EnableCookieEncryption, FuseState.ENABLE],
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable, FuseState.DISABLE],
  [FuseV1Options.EnableNodeCliInspectArguments, FuseState.DISABLE],
  [
    FuseV1Options.EnableEmbeddedAsarIntegrityValidation,
    process.platform === "linux" ? FuseState.DISABLE : FuseState.ENABLE,
  ],
  [FuseV1Options.OnlyLoadAppFromAsar, FuseState.ENABLE],
  [FuseV1Options.LoadBrowserProcessSpecificV8Snapshot, FuseState.DISABLE],
  [FuseV1Options.GrantFileProtocolExtraPrivileges, FuseState.DISABLE],
  [FuseV1Options.WasmTrapHandlers, FuseState.ENABLE],
]);
for (const [fuse, expected] of expectedFuses) {
  if (wire[fuse] !== expected) {
    throw new Error(
      `Electron fuse ${FuseV1Options[fuse]} is ${wire[fuse]}, expected ${expected}`,
    );
  }
}

const archiveFiles = listPackage(asarPath).map((file) =>
  file.replaceAll("\\", "/"),
);
const unexpected = archiveFiles.filter(
  (file) =>
    file !== "/package.json" && file !== "/dist" && !file.startsWith("/dist/"),
);
if (unexpected.length > 0) {
  throw new Error(
    `packaged ASAR contains unexpected files: ${unexpected.join(", ")}`,
  );
}
for (const required of [
  "/package.json",
  "/dist/app.css",
  "/dist/files/inter-cyrillic-ext-wght-normal.woff2",
  "/dist/files/inter-cyrillic-wght-normal.woff2",
  "/dist/files/inter-greek-ext-wght-normal.woff2",
  "/dist/files/inter-greek-wght-normal.woff2",
  "/dist/files/inter-latin-ext-wght-normal.woff2",
  "/dist/files/inter-latin-wght-normal.woff2",
  "/dist/files/inter-vietnamese-wght-normal.woff2",
  "/dist/src/main.js",
  "/dist/src/auth.js",
  "/dist/src/deep-link.js",
  "/dist/src/diagnostics.js",
  "/dist/src/distribution.js",
  "/dist/src/managed-policy.js",
  "/dist/src/native-menu.js",
  "/dist/src/remote-lifecycle.js",
  "/dist/src/security/remote-policy.mjs",
  "/dist/src/update-coordinator.js",
  "/dist/src/updater.js",
  "/dist/src/window-state.js",
]) {
  if (!archiveFiles.includes(required)) {
    throw new Error(`packaged ASAR is missing ${required}`);
  }
}
if (
  archiveFiles.some(
    (file) =>
      file.endsWith(".ts") ||
      file.includes(".test.") ||
      file.includes("maliciousinstance"),
  )
) {
  throw new Error("packaged ASAR contains source or test-only content");
}
const packagedUpdater = extractFile(
  asarPath,
  join("dist", "src", "updater.js"),
).toString("utf8");
for (const expected of [
  releasePolicy.updates.origin,
  releasePolicy.updates.channel,
  releasePolicy.updates.pathVersion,
  String(releasePolicy.updates.electronMajor),
]) {
  if (!packagedUpdater.includes(JSON.stringify(expected).slice(1, -1))) {
    throw new Error(
      "packaged updater does not match the immutable release policy",
    );
  }
}

const startup = await verifyPackagedStartup(executablePath, {
  // A missing enterprise policy is normal consumer open mode. The native
  // Windows helper still fails closed if an existing policy location is
  // insecure or if the signed helper cannot be executed.
  verifyDiagnosticJournal: true,
});
if (
  startup.chromiumVersion !== releasePolicy.runtime.chromium ||
  startup.electronVersion !== releasePolicy.runtime.electron
) {
  throw new Error(
    `packaged runtime ${startup.electronVersion}/${startup.chromiumVersion} does not match the release policy`,
  );
}
if (packageDocument.devDependencies.node !== releasePolicy.runtime.node) {
  throw new Error("packaged Node runtime does not match the release policy");
}
const verifiedFuseReport = Object.fromEntries(
  [...expectedFuses].map(([fuse, state]) => [
    FuseV1Options[fuse],
    state === FuseState.ENABLE ? "enabled" : "disabled",
  ]),
);
const verificationReport = {
  schemaVersion: 2,
  platform: platformName,
  architecture: process.arch,
  packageFormat: releasePolicy.distribution[platformName].installer,
  asarOnly: true,
  runtime: {
    electron: packagedElectronVersion,
    chromium: startup.chromiumVersion,
    node: releasePolicy.runtime.node,
  },
  fuses: verifiedFuseReport,
  asarFiles: archiveFiles.length,
  startup: startup.status,
  accessibility: startup.accessibility,
  updates: {
    origin: releasePolicy.updates.origin,
    pathVersion: releasePolicy.updates.pathVersion,
    channel: releasePolicy.updates.channel,
    productName: releasePolicy.updates.productName,
    applicationId: releasePolicy.updates.applicationId,
    electronMajor: releasePolicy.updates.electronMajor,
    delivery:
      platformName === "linux"
        ? "system-package-manager"
        : "electron-auto-updater",
  },
};
await writeFile(
  join(out, "package-verification.json"),
  `${JSON.stringify(verificationReport, null, 2)}\n`,
);
process.stdout.write(
  `${JSON.stringify({
    application: appPath,
    asarFiles: archiveFiles.length,
    fuseVersion: wire.version,
    startup: startup.status,
    verifiedFuses: expectedFuses.size,
  })}\n`,
);

async function expectMissing(path) {
  try {
    await access(path, constants.F_OK);
  } catch {
    return;
  }
  throw new Error(`mutable unpackaged application directory exists: ${path}`);
}

async function verifyPackagedStartup(
  executable,
  { verifyDiagnosticJournal },
) {
  const userData = await mkdtemp(join(tmpdir(), "leapview-package-smoke-"));
  const devtoolsPort = await reserveLoopbackPort();
  const child = spawn(
    executable,
    [
      "--headless",
      "--disable-gpu",
      `--remote-debugging-port=${devtoolsPort}`,
      `--user-data-dir=${userData}`,
    ],
    {
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let diagnostic = "";
  const appendDiagnostic = (chunk) => {
    diagnostic = `${diagnostic}${String(chunk)}`.slice(-16_384);
  };
  child.stdout.on("data", appendDiagnostic);
  child.stderr.on("data", appendDiagnostic);
  try {
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      if (child.exitCode !== null || child.signalCode !== null) {
        throw startupFailure(
          "packaged application exited during startup",
          child,
          diagnostic,
        );
      }
      let shellTarget;
      try {
        const response = await fetch(
          `http://127.0.0.1:${devtoolsPort}/json/list`,
          {
            signal: AbortSignal.timeout(1_000),
          },
        );
        if (!response.ok) {
          throw new Error("packaged application debug target was unavailable");
        }
        const targets = await response.json();
        shellTarget =
          Array.isArray(targets)
            ? targets.find(
                (target) =>
                  target?.type === "page" &&
                  target?.url === "leapview://app/",
              )
            : undefined;
      } catch {}
      if (shellTarget !== undefined) {
        if (typeof shellTarget.webSocketDebuggerUrl !== "string") {
          throw new Error(
            "packaged application shell debugger target was malformed",
          );
        }
        const runtime = await readRuntimeVersions(devtoolsPort);
        const accessibility = await readTrustedShellAccessibility(
          shellTarget.webSocketDebuggerUrl,
        );
        if (verifyDiagnosticJournal) {
          await verifyPackagedDiagnosticJournal(
            join(userData, "diagnostics.json"),
          );
        }
        return {
          status: "trusted-shell-ready",
          accessibility,
          ...runtime,
        };
      }
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
    }
    throw startupFailure(
      "packaged application did not open its trusted shell",
      child,
      diagnostic,
    );
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill();
      await Promise.race([
        new Promise((resolveExit) => child.once("exit", resolveExit)),
        new Promise((resolveDelay) => setTimeout(resolveDelay, 2_000)),
      ]);
      if (child.exitCode === null && child.signalCode === null) {
        child.kill("SIGKILL");
      }
    }
    await rm(userData, {
      force: true,
      maxRetries: 5,
      recursive: true,
      retryDelay: 100,
    });
  }
}

async function readRuntimeVersions(devtoolsPort) {
  const response = await fetch(
    `http://127.0.0.1:${devtoolsPort}/json/version`,
    {
      signal: AbortSignal.timeout(1_000),
    },
  );
  if (!response.ok) {
    throw new Error("packaged application runtime version was unavailable");
  }
  const version = await response.json();
  const chromiumVersion = /^Chrome\/(.+)$/u.exec(version?.Browser ?? "")?.[1];
  const electronVersion = /Electron\/([0-9.]+)/u.exec(
    version?.["User-Agent"] ?? "",
  )?.[1];
  if (chromiumVersion === undefined || electronVersion === undefined) {
    throw new Error("packaged application runtime version was malformed");
  }
  return { chromiumVersion, electronVersion };
}

async function reserveLoopbackPort() {
  const server = createServer();
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  await new Promise((resolveClose, rejectClose) => {
    server.close((error) => {
      if (error) {
        rejectClose(error);
        return;
      }
      resolveClose();
    });
  });
  if (address === null || typeof address === "string") {
    throw new Error("failed to reserve a loopback debug port");
  }
  return address.port;
}

function startupFailure(message, child, diagnostic) {
  return new Error(
    [
      message,
      `exit=${child.exitCode ?? "none"}`,
      `signal=${child.signalCode ?? "none"}`,
      diagnostic.trim(),
    ]
      .filter(Boolean)
      .join("\n"),
  );
}
