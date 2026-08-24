import assert from "node:assert/strict";
import { join } from "node:path";
import test from "node:test";

import { nativeMakerPreparation } from "./native-maker-modules.mjs";

test("prepares only the macOS DMG native helper with pinned build tools", () => {
  assert.deepEqual(nativeMakerPreparation("darwin", "/desktop"), {
    addon: join(
      "/desktop",
      "node_modules",
      "macos-alias",
      "build",
      "Release",
      "volume.node",
    ),
    packageDirectory: join("/desktop", "node_modules", "macos-alias"),
    pinnedNode: join("/desktop", "node_modules", "node", "bin", "node"),
    nodeGyp: join(
      "/desktop",
      "node_modules",
      "node-gyp",
      "bin",
      "node-gyp.js",
    ),
  });
  assert.equal(nativeMakerPreparation("linux", "/desktop"), null);
  assert.equal(nativeMakerPreparation("win32", "/desktop"), null);
});
