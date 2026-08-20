import type { ForgeConfig } from "@electron-forge/shared-types";
import { MakerDeb } from "@electron-forge/maker-deb";
import { MakerDMG } from "@electron-forge/maker-dmg";
import { MakerSquirrel } from "@electron-forge/maker-squirrel";
import { MakerZIP } from "@electron-forge/maker-zip";
import { resolve } from "node:path";
import {
  type FuseConfig,
  FuseV1Options,
  FuseVersion,
} from "@electron/fuses";

import {
  consumerDistributionContract,
} from "./installer-contract.js";
import { ProjectFusesPlugin } from "./fuses-plugin.js";

const desktopRoot = import.meta.dirname;
const distributionResource = {
  preview: "preview-distribution.json",
  stable: "stable-distribution.json",
}[process.env.LEAPVIEW_DESKTOP_DISTRIBUTION ?? ""];

export function createFuseConfig(platform: NodeJS.Platform): FuseConfig {
  return {
    version: FuseVersion.V1,
    strictlyRequireAllFuses: true,
    [FuseV1Options.RunAsNode]: false,
    [FuseV1Options.EnableCookieEncryption]: true,
    [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
    [FuseV1Options.EnableNodeCliInspectArguments]: false,
    [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]:
      platform === "darwin" || platform === "win32",
    [FuseV1Options.OnlyLoadAppFromAsar]: true,
    [FuseV1Options.LoadBrowserProcessSpecificV8Snapshot]: false,
    [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
    [FuseV1Options.WasmTrapHandlers]: true,
  };
}

export const fuseConfig = createFuseConfig(process.platform);

const config: ForgeConfig = {
  packagerConfig: {
    appBundleId: "dev.leapview.desktop",
    asar: true,
    executableName: "LeapView",
    extraResource: [
      ...(process.platform === "win32"
        ? [
            resolve(
              desktopRoot,
              "dist/native/leapview-windows-policy.exe",
            ),
          ]
        : []),
      ...(distributionResource
        ? [resolve(desktopRoot, distributionResource)]
        : []),
    ],
    protocols: [
      {
        name: "LeapView Desktop",
        schemes: ["leapview-desktop"],
      },
    ],
    ignore: [
      /^\/(?!dist(?:\/|$)|package\.json$).+/u,
      /^\/dist\/(?:forge\.config\.js|installer-contract\.js|makers(?:\/|$))/u,
      /^\/dist\/native(?:\/|$)/u,
    ],
  },
  rebuildConfig: {},
  makers: [
    new MakerSquirrel(
      {
        authors: "LeapView",
        description:
          "End-user desktop client for deployed LeapView instances.",
        name: "leapview",
      },
      ["win32"],
    ),
    new MakerDMG({}, ["darwin"]),
    new MakerDeb(
      {
        options: {
          bin: "LeapView",
          categories: ["Office"],
          description:
            "End-user desktop client for deployed LeapView instances.",
          homepage: "https://leapview.dev",
          maintainer: "LeapView",
          mimeType: [
            `x-scheme-handler/${consumerDistributionContract.protocol.scheme}`,
          ],
          name: "leapview-desktop",
          productDescription:
            "Connects to deployed LeapView instances while preserving server-side authentication, access, and dashboard authority.",
          productName: "LeapView",
          section: "utils",
        },
      },
      ["linux"],
    ),
    new MakerZIP({}, ["darwin"]),
  ],
  plugins: [new ProjectFusesPlugin(fuseConfig)],
};

export default config;
