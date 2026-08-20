import assert from "node:assert/strict";
import test from "node:test";

import {
  squirrelArchiveArguments,
  validateInstallerContract,
} from "./verify-installer.mjs";

test("uses PowerShell ZIP extraction for Windows Squirrel archives", () => {
  assert.deepEqual(
    squirrelArchiveArguments(
      "D:\\a\\leapview\\leapview\\desktop\\out\\make\\package.nupkg",
      "C:\\Users\\runneradmin\\AppData\\Local\\Temp\\payload",
    ),
    [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      "Expand-Archive -LiteralPath 'D:\\a\\leapview\\leapview\\desktop\\out\\make\\package.nupkg' -DestinationPath 'C:\\Users\\runneradmin\\AppData\\Local\\Temp\\payload' -Force",
    ],
  );
});

test("installer verification accepts only the selected consumer formats", () => {
  for (const [platform, format, scope, updateMechanism, updateArtifacts] of [
    ["darwin", "dmg", "user-installed", "squirrel-mac", ["zip"]],
    ["linux", "deb", "system-package-manager", "apt", []],
    [
      "win32",
      "exe",
      "per-user",
      "squirrel-windows",
      ["nupkg", "RELEASES"],
    ],
  ]) {
    assert.deepEqual(
      validateInstallerContract({
        platform,
        format,
        scope,
        policyIntegration: "deferred-not-supported",
        protocolIntegration: "consumer-owned-validated-url",
        updateArtifacts,
        updateMechanism,
      }),
      {
        format,
        scope,
        policyIntegration: "deferred-not-supported",
        protocolIntegration: "consumer-owned-validated-url",
        updateArtifacts,
        updateMechanism,
      },
    );
  }
  assert.throws(
    () =>
      validateInstallerContract({
        platform: "win32",
        format: "zip",
        scope: "per-user",
        policyIntegration: "deferred-not-supported",
        protocolIntegration: "runtime-owned",
        updateArtifacts: [],
        updateMechanism: "squirrel-windows",
      }),
    /incomplete/,
  );
});
