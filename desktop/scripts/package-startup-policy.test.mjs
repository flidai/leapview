import assert from "node:assert/strict";
import { describe, test } from "node:test";

import {
  packagedStartupTimeoutMilliseconds,
} from "./package-startup-policy.mjs";

describe("packaged startup verification policy", () => {
  test("allows bounded Windows cold-start scanning without weakening other platforms", () => {
    assert.equal(packagedStartupTimeoutMilliseconds("win32"), 45_000);
    assert.equal(packagedStartupTimeoutMilliseconds("darwin"), 15_000);
    assert.equal(packagedStartupTimeoutMilliseconds("linux"), 15_000);
  });

  test("rejects unsupported platforms", () => {
    assert.throws(
      () => packagedStartupTimeoutMilliseconds("freebsd"),
      /unsupported packaged startup platform freebsd/u,
    );
  });
});
