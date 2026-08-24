import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  verifyPackagedDiagnosticEvent,
  verifyPackagedDiagnosticJournal,
} from "./packaged-diagnostics.mjs";

const at = "2026-07-29T19:55:25.000Z";

test("accepts the exact updater events a packaged startup can emit", () => {
  for (const phase of [
    "checking",
    "available",
    "not-available",
    "downloaded",
    "deferred",
    "restart-requested",
    "failed",
  ]) {
    assert.doesNotThrow(() =>
      verifyPackagedDiagnosticEvent({
        at,
        kind: "update",
        phase,
      }),
    );
  }
});

test("rejects untrusted updater details and unknown phases", () => {
  for (const event of [
    {
      at,
      kind: "update",
      phase: "failed",
      error: "https://attacker.example/token",
    },
    {
      at,
      kind: "update",
      phase: "downloaded",
      releaseNotes: "<script>attacker</script>",
    },
    { at, kind: "update", phase: "remote-controlled" },
  ]) {
    assert.throws(
      () => verifyPackagedDiagnosticEvent(event),
      /invalid update data/,
    );
  }
});

test("preserves the bounded pre-updater startup event contract", () => {
  for (const event of [
    { at, kind: "startup", packaged: true },
    {
      at,
      kind: "policy",
      mode: "open",
      userInstances: "allowed",
      diagnostics: "enabled",
    },
    {
      at,
      kind: "render-process-gone",
      surface: "trusted-shell",
      reason: "crashed",
    },
    {
      at,
      kind: "child-process-gone",
      processType: "gpu",
      reason: "oom",
    },
  ]) {
    assert.doesNotThrow(() => verifyPackagedDiagnosticEvent(event));
  }
  assert.throws(
    () =>
      verifyPackagedDiagnosticEvent({
        at,
        kind: "navigation",
        action: "blocked-popup",
      }),
    /unexpected startup event/,
  );
});

test("gives diagnostic persistence its own bounded wait", async () => {
  const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
  const path = join(directory, "diagnostics.json");
  const document = `${JSON.stringify({
    schemaVersion: 1,
    events: [{ at, kind: "startup", packaged: true }],
  })}\n`;
  try {
    const persisted = new Promise((resolve, reject) => {
      setTimeout(() => {
        writeFile(path, document, { mode: 0o600 }).then(resolve, reject);
      }, 75);
    });
    await verifyPackagedDiagnosticJournal(path, 1_000);
    await persisted;
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});
