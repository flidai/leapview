import { flipFuses, type FuseConfig } from "@electron/fuses";
import {
  namedHookWithTaskFn,
  PluginBase,
} from "@electron-forge/plugin-base";
import type {
  ForgeArch,
  ForgeListrTask,
  ForgePlatform,
  ResolvedForgeConfig,
} from "@electron-forge/shared-types";
import { join, resolve } from "node:path";

/**
 * Electron Forge 7's published fuses plugin is CommonJS, while the current
 * @electron/fuses contract is ESM. Keep this small hook in the project so the
 * Bun test runner and the pinned Node packaging runner use the same module
 * format and fuse implementation.
 */
export class ProjectFusesPlugin extends PluginBase<FuseConfig> {
  name = "fuses";

  getHooks() {
    return {
      packageAfterCopy: namedHookWithTaskFn<"packageAfterCopy">(
        async (
          _task: ForgeListrTask<unknown> | null,
          resolvedForgeConfig: ResolvedForgeConfig,
          resourcesPath: string,
          _electronVersion: string,
          platform: ForgePlatform,
          arch: ForgeArch,
        ) => {
          if (Object.keys(this.config).length === 0) {
            return;
          }

          const applePlatforms = ["darwin", "mas"];
          const pathToElectronExecutable = applePlatforms.includes(platform)
            ? join(resolve(resourcesPath, "../.."), "MacOS", "Electron")
            : join(
                resolve(resourcesPath, "../.."),
                `electron${platform === "win32" ? ".exe" : ""}`,
              );
          const osxSignConfig = resolvedForgeConfig.packagerConfig.osxSign;
          const hasOSXSignConfig =
            (typeof osxSignConfig === "object" &&
              osxSignConfig !== null &&
              Object.keys(osxSignConfig).length > 0) ||
            Boolean(osxSignConfig);

          await flipFuses(pathToElectronExecutable, {
            resetAdHocDarwinSignature:
              !hasOSXSignConfig &&
              applePlatforms.includes(platform) &&
              arch === "arm64",
            ...this.config,
          });
        },
        "Flipping Fuses",
      ),
    };
  }
}
