import { spawnSync } from "node:child_process";
import { join } from "node:path";

export function nativeMakerPreparation(platform, desktopRoot) {
  if (platform !== "darwin") {
    return null;
  }
  const packageDirectory = join(
    desktopRoot,
    "node_modules",
    "macos-alias",
  );
  return {
    addon: join(packageDirectory, "build", "Release", "volume.node"),
    packageDirectory,
    pinnedNode: join(desktopRoot, "node_modules", "node", "bin", "node"),
    nodeGyp: join(
      desktopRoot,
      "node_modules",
      "node-gyp",
      "bin",
      "node-gyp.js",
    ),
  };
}

function addonLoads(plan) {
  const result = spawnSync(
    plan.pinnedNode,
    ["-e", "require(process.argv[1])", plan.addon],
    { stdio: "ignore" },
  );
  return result.status === 0;
}

export function prepareNativeMakerModules(platform, desktopRoot) {
  const plan = nativeMakerPreparation(platform, desktopRoot);
  if (plan === null || addonLoads(plan)) {
    return;
  }
  const rebuild = spawnSync(
    plan.pinnedNode,
    [plan.nodeGyp, "rebuild", "--release"],
    {
      cwd: plan.packageDirectory,
      stdio: "inherit",
    },
  );
  if (rebuild.error !== undefined || rebuild.status !== 0) {
    throw new Error(
      `failed to rebuild macOS maker dependency for pinned Node: ${
        rebuild.error?.message ?? `exit ${rebuild.status}`
      }`,
    );
  }
  if (!addonLoads(plan)) {
    throw new Error(
      "rebuilt macOS maker dependency is incompatible with pinned Node",
    );
  }
}
