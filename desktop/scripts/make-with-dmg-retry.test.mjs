import assert from "node:assert/strict";
import test from "node:test";

import { runWithDMGDetachRetry } from "./make-with-dmg-retry.mjs";

const transientDetachFailure = `Making a dmg distributable for darwin/x64
Command failed: hdiutil detach /Volumes/LeapView
hdiutil: detach failed - No such file or directory`;

test("retries one macOS make after the exact already-detached DMG failure", async () => {
  const attempts = [
    { code: 1, output: transientDetachFailure },
    { code: 0, output: "DMG created" },
  ];
  const diagnostics = [];
  let calls = 0;

  const code = await runWithDMGDetachRetry(
    async () => attempts[calls++],
    { platform: "darwin", diagnostic: (message) => diagnostics.push(message) },
  );

  assert.equal(code, 0);
  assert.equal(calls, 2);
  assert.deepEqual(diagnostics, [
    "macOS DMG cleanup found an already-detached LeapView volume; retrying Electron Forge make once",
  ]);
});

test("does not retry unrelated, partial, or non-macOS failures", async (t) => {
  for (const scenario of [
    {
      name: "unrelated make failure",
      platform: "darwin",
      output: "Electron Forge make failed",
    },
    {
      name: "detach command without the exact error",
      platform: "darwin",
      output: "hdiutil detach /Volumes/LeapView\nhdiutil: detach failed - Resource busy",
    },
    {
      name: "detach error without the exact command",
      platform: "darwin",
      output: "hdiutil: detach failed - No such file or directory",
    },
    {
      name: "matching output on another platform",
      platform: "linux",
      output: transientDetachFailure,
    },
  ]) {
    await t.test(scenario.name, async () => {
      let calls = 0;
      const code = await runWithDMGDetachRetry(
        async () => {
          calls += 1;
          return { code: 23, output: scenario.output };
        },
        {
          platform: scenario.platform,
          diagnostic: () => assert.fail("unexpected retry"),
        },
      );

      assert.equal(code, 23);
      assert.equal(calls, 1);
    });
  }
});

test("keeps a failed retry fatal and never attempts a third make", async () => {
  const attempts = [
    { code: 1, output: transientDetachFailure },
    { code: 42, output: "retry failed for another reason" },
  ];
  let calls = 0;

  const code = await runWithDMGDetachRetry(
    async () => attempts[calls++],
    { platform: "darwin", diagnostic: () => undefined },
  );

  assert.equal(code, 42);
  assert.equal(calls, 2);
});

test("does not retry a successful make", async () => {
  let calls = 0;
  const code = await runWithDMGDetachRetry(
    async () => {
      calls += 1;
      return { code: 0, output: transientDetachFailure };
    },
    { platform: "darwin", diagnostic: () => assert.fail("unexpected retry") },
  );

  assert.equal(code, 0);
  assert.equal(calls, 1);
});
