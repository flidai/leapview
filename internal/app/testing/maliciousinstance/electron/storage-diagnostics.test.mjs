import assert from "node:assert/strict";
import test from "node:test";

import { createStorageOperationDiagnostics } from "./storage-diagnostics.mjs";

test("failed storage operation keeps its error and records the precise check", async () => {
  const result = { currentCheck: "storage.cross-profile", storageDiagnostics: [] };
  const diagnostics = createStorageOperationDiagnostics(result);
  const failure = new Error("renderer script exceeded 5000ms");

  await assert.rejects(
    diagnostics.run("first.seed-renderer-state", () => Promise.reject(failure)),
    (error) => error === failure && error.name === failure.name,
  );

  assert.equal(result.currentCheck, "storage.cross-profile.first.seed-renderer-state");
  assert.equal(result.storageDiagnostics.length, 1);
  const [diagnostic] = result.storageDiagnostics;
  assert.deepEqual(Object.keys(diagnostic).sort(), ["durationMs", "operation", "status"]);
  assert.equal(diagnostic.operation, "first.seed-renderer-state");
  assert.equal(diagnostic.status, "failed");
  assert.ok(Number.isInteger(diagnostic.durationMs));
  assert.ok(diagnostic.durationMs >= 0);
  assert.ok(diagnostic.durationMs <= 40_000);
});

test("result persistence captures elapsed time for an operation still running", async () => {
  const result = { currentCheck: "storage.cross-profile", storageDiagnostics: [] };
  const diagnostics = createStorageOperationDiagnostics(result);
  let finish;
  const pending = new Promise((resolve) => {
    finish = resolve;
  });
  const operation = diagnostics.run("second.read-renderer-state", () => pending);

  diagnostics.updateActive();
  assert.equal(result.storageDiagnostics.length, 1);
  const [diagnostic] = result.storageDiagnostics;
  assert.deepEqual(Object.keys(diagnostic).sort(), ["durationMs", "operation", "status"]);
  assert.equal(diagnostic.operation, "second.read-renderer-state");
  assert.equal(diagnostic.status, "running");
  assert.ok(Number.isInteger(diagnostic.durationMs));
  assert.ok(diagnostic.durationMs >= 0);
  assert.ok(diagnostic.durationMs <= 40_000);

  finish("done");
  assert.equal(await operation, "done");
  assert.equal(diagnostic.status, "complete");
});
