import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";

import { runProofLifecycle } from "./proof-lifecycle.mjs";

class FakeApplication extends EventEmitter {
  constructor(events) {
    super();
    this.events = events;
    this.exited = false;
  }

  async whenReady() {
    this.events.push("ready");
  }

  closeLastWindow() {
    this.events.push("windows-closed");
    if (this.listenerCount("window-all-closed") === 0) {
      this.exited = true;
      this.events.push("default-quit");
      return;
    }
    this.emit("window-all-closed");
  }

  quit() {
    this.exited = true;
    this.events.push("quit");
  }

  exit(code) {
    this.exited = true;
    this.events.push(`exit:${code}`);
  }
}

test("last-window closure cannot exit before the complete result is durable", async () => {
  const events = [];
  const app = new FakeApplication(events);
  const result = { passed: false, phase: "bootstrap" };

  await runProofLifecycle({
    app,
    result,
    runProof: async () => {
      events.push("proof");
      app.closeLastWindow();
      assert.equal(app.exited, false);
    },
    writeResult: async () => {
      await new Promise((resolve) => setTimeout(resolve, 5));
      events.push(`write:${result.phase}`);
    },
  });

  assert.deepEqual(events, [
    "write:bootstrap",
    "ready",
    "proof",
    "windows-closed",
    "write:complete",
    "quit",
  ]);
  assert.deepEqual(result, { passed: true, phase: "complete" });
});

test("last-window closure cannot hide a failed proof result", async () => {
  const events = [];
  const app = new FakeApplication(events);
  const result = { passed: false, phase: "bootstrap" };

  await runProofLifecycle({
    app,
    result,
    runProof: async () => {
      app.closeLastWindow();
      throw new Error("policy proof failed");
    },
    writeResult: async () => {
      await new Promise((resolve) => setTimeout(resolve, 5));
      events.push(`write:${result.phase}`);
    },
  });

  assert.deepEqual(events, [
    "write:bootstrap",
    "ready",
    "windows-closed",
    "write:failed",
    "exit:1",
  ]);
  assert.equal(result.passed, false);
  assert.equal(result.phase, "failed");
  assert.equal(result.error, "policy proof failed");
});
