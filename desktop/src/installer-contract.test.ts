import { describe, expect, test } from "bun:test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import {
  addWindowsManagedDeployment,
  consumerDistributionContract,
  managedInstallerGroundwork,
} from "../installer-contract.js";
import { macOSComponentPropertyList } from "../makers/maker-pkg.js";

describe("desktop installer contract", () => {
  test("selects consumer installers and platform-native update artifacts", () => {
    expect(consumerDistributionContract).toEqual({
      schemaVersion: 2,
      channel: "consumer-v1",
      platforms: {
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
      protocol: {
        scheme: "leapview-desktop",
        argumentToken: "%1",
      },
    });
  });

  test("pins Forge makers for every consumer artifact", async () => {
    const root = resolve(import.meta.dirname, "..");
    const [packageBody, forgeBody] = await Promise.all([
      readFile(resolve(root, "package.json"), "utf8"),
      readFile(resolve(root, "forge.config.ts"), "utf8"),
    ]);
    const packageDocument = JSON.parse(packageBody) as {
      devDependencies?: Record<string, string>;
      trustedDependencies?: string[];
    };
    expect(packageDocument.devDependencies).toMatchObject({
      "@electron-forge/maker-deb": "8.0.0-alpha.9",
      "@electron-forge/maker-dmg": "8.0.0-alpha.9",
      "@electron-forge/maker-squirrel": "8.0.0-alpha.9",
      "@electron-forge/maker-zip": "8.0.0-alpha.9",
    });
    expect(forgeBody).toContain("new MakerSquirrel");
    expect(forgeBody).toContain("new MakerDMG");
    expect(forgeBody).toContain("new MakerDeb");
    expect(forgeBody).toContain("new MakerZIP");
    expect(forgeBody).not.toContain("new MakerWix");
    expect(forgeBody).not.toContain("new MakerPKG");
    expect(packageDocument.trustedDependencies).toEqual([
      "@bitdisaster/exe-icon-extractor",
      "electron",
      "electron-winstaller",
      "fs-xattr",
      "macos-alias",
      "node",
    ]);
  });

  test("preserves completed managed-installer work outside the v1 contract", () => {
    expect(managedInstallerGroundwork).toMatchObject({
      installationScope: "per-machine",
      formats: {
        darwin: "pkg",
        linux: "deb",
        win32: "msi",
      },
      supportedInConsumerV1: false,
    });
  });

  test("preserves a transactional machine protocol and protected ProgramData directory", () => {
    const template = [
      "<Product>",
      'InstallerVersion="405"',
      "      <!-- Desktop -->",
      '    <Feature Id="Complete"',
      '        <ComponentRef Id="PurgeOnUninstall" />',
      "</Product>",
    ].join("\n");
    const configured = addWindowsManagedDeployment(template);
    expect(configured).toContain('Id="CommonAppDataFolder"');
    expect(configured).toContain('InstallerVersion="500"');
    expect(configured).toContain('Id="LeapViewPolicyDirectory"');
    expect(configured).toContain(
      'Sddl="D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)(A;OICI;GRGX;;;BU)"',
    );
    expect(configured).toContain(
      'Key="Software\\Classes\\leapview-desktop"',
    );
    expect(configured).toContain(
      'Value="&quot;[APPLICATIONROOTDIRECTORY]LeapView.exe&quot; &quot;%1&quot;"',
    );
    expect(configured).toContain('ForceDeleteOnUninstall="yes"');
    expect(configured).toContain(
      '<ComponentRef Id="LeapViewDesktopProtocol" />',
    );
  });

  test("fails closed when the pinned maker template changes", () => {
    expect(() => addWindowsManagedDeployment("<Product/>")).toThrow(
      /pinned WiX template/,
    );
  });

  test("makes the macOS app non-relocatable and atomically upgradeable", () => {
    expect(macOSComponentPropertyList).toContain(
      "<key>BundleIsRelocatable</key>\n    <false/>",
    );
    expect(macOSComponentPropertyList).toContain(
      "<key>BundleHasStrictIdentifier</key>\n    <true/>",
    );
    expect(macOSComponentPropertyList).toContain(
      "<key>BundleOverwriteAction</key>\n    <string>upgrade</string>",
    );
    expect(macOSComponentPropertyList).toContain(
      "<string>Applications/LeapView.app</string>",
    );
  });

  test("POSIX installer scripts preserve policy and repair root-only ownership", async () => {
    const root = resolve(import.meta.dirname, "..");
    const [
      macPreinstall,
      macPostinstall,
      linuxPreinstall,
      linuxPostinstall,
      macQualification,
    ] = await Promise.all([
        readFile(
          resolve(root, "installer/macos/scripts/preinstall"),
          "utf8",
        ),
        readFile(
          resolve(root, "installer/macos/scripts/postinstall"),
          "utf8",
        ),
        readFile(
          resolve(root, "installer/linux/preinst"),
          "utf8",
        ),
        readFile(
          resolve(root, "installer/linux/postinst"),
          "utf8",
        ),
        readFile(
          resolve(root, "scripts/qualify-installer-macos.sh"),
          "utf8",
        ),
      ]);
    for (const script of [
      macPreinstall,
      macPostinstall,
      linuxPreinstall,
      linuxPostinstall,
    ]) {
      expect(script).toContain("desktop-policy.json");
      expect(script).toContain("policy location must not be a symlink");
      expect(script).not.toContain("rm ");
    }
    expect(macPostinstall).toContain("chown root:wheel");
    expect(macPostinstall).toContain("chmod 0644");
    expect(linuxPostinstall).toContain("chown root:root");
    expect(linuxPostinstall).toContain("chmod 0644");
    expect(macQualification).toContain(
      "URLForApplicationToOpenURL(url)",
    );
    expect(macQualification).not.toContain("-dump");
  });

  test("consumer qualification uses user-scoped installers without managed policy", async () => {
    const root = resolve(import.meta.dirname, "..");
    const [macOS, windows, linux] = await Promise.all([
      readFile(resolve(root, "scripts/qualify-consumer-macos.sh"), "utf8"),
      readFile(
        resolve(root, "scripts/qualify-consumer-windows.ps1"),
        "utf8",
      ),
      readFile(resolve(root, "scripts/qualify-installer-linux.sh"), "utf8"),
    ]);
    expect(macOS).toContain("hdiutil attach");
    expect(macOS).toContain("pwd -P");
    expect(macOS).toContain('${HOME}/Applications');
    expect(macOS).toContain("URLsForApplicationsToOpenURL(url)");
    expect(macOS).not.toContain("installer -pkg");
    expect(windows).toContain('$env:LocalAppData "leapview"');
    expect(windows).not.toContain("msiexec");
    expect(windows).not.toContain("Join-Path $env:ProgramFiles");
    expect(linux).toContain("apt-get install");
    for (const script of [macOS, windows, linux]) {
      expect(script).not.toContain("desktop-policy.json");
    }
  });
});
