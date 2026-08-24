"use strict";

const assert = require("node:assert/strict");
const { mkdtempSync, writeFileSync } = require("node:fs");
const { createRequire } = require("node:module");
const { tmpdir } = require("node:os");
const { join } = require("node:path");
const { test } = require("node:test");

let appdmgRequire;
try {
  appdmgRequire = createRequire(require.resolve("appdmg"));
} catch {
  // appdmg is a macOS-only optional maker dependency on non-macOS hosts.
}
const imageSize = appdmgRequire
  ? appdmgRequire("image-size")
  : require("./image-size-next-compat/index.cjs");

test("appdmg resolves the vendored compatibility package", (t) => {
  if (!appdmgRequire) {
    t.skip("appdmg is a macOS-only optional dependency");
    return;
  }
  const resolvedImageSize = appdmgRequire.resolve("image-size");
  const resolvedPackage = appdmgRequire("image-size/package.json");
  assert.match(resolvedImageSize, /node_modules[\\/]image-size[\\/]index\.cjs$/);
  assert.equal(resolvedPackage.name, "image-size");
  assert.equal(resolvedPackage.version, "1.2.2-leapview.0");
  assert.equal(typeof imageSize, "function");
});

const png = Buffer.from(
  "89504e470d0a1a0a0000000d4948445200000001000000020806000000f4" +
    "78d4fa0000000a49444154789c6360000000020001e221bc330000000049454e44ae426082",
  "hex",
);

test("keeps image-size callable for Buffer and legacy path inputs", async () => {
  const root = mkdtempSync(join(tmpdir(), "leapview-image-size-"));
  const path = join(root, "sample.png");
  writeFileSync(path, png);

  assert.deepEqual(imageSize(png), { width: 1, height: 2, type: "png" });
  assert.deepEqual(imageSize(path), { width: 1, height: 2, type: "png" });

  await new Promise((resolve, reject) => imageSize(path, (error, value) => {
    try {
      assert.equal(error, null);
      assert.deepEqual(value, { width: 1, height: 2, type: "png" });
      resolve();
    } catch (callbackError) {
      reject(callbackError);
    }
  }));
});

test("rejects malformed container bytes instead of looping", () => {
  // These malformed entries exercise the ICNS minimum-length and JPEG-XL
  // non-progress guards while remaining bounded if a regression reappears.
  const icns = Buffer.concat([
    Buffer.from("icns", "ascii"),
    Buffer.from([0, 0, 0, 16]),
    Buffer.from("ic08", "ascii"),
    Buffer.from([0, 0, 0, 0]),
  ]);
  const jxl = Buffer.concat([
    Buffer.from([0, 0, 0, 12]),
    Buffer.from("JXL ", "ascii"),
    Buffer.alloc(4),
    Buffer.from([0, 0, 0, 12]),
    Buffer.from("ftypjxl ", "ascii"),
    Buffer.from([0, 0, 0, 0]),
    Buffer.from("jxlp", "ascii"),
  ]);
  assert.throws(() => imageSize(icns), /invalid|unsupported/i);
  assert.throws(() => imageSize(jxl), /codestream|unsupported|invalid/i);
});
