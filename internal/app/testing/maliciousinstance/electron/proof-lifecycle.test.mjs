import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";

import { startProofLifecycle } from "./proof-lifecycle.mjs";

class FakeApplication extends EventEmitter {
  constructor(events) {
    super();
    this.events = events;
    this.exited = false;
    this.readyPromise = new Promise((resolve, reject) => {
      this.resolveReady = resolve;
      this.rejectReady = reject;
    });
  }

  whenReady() {
    return this.readyPromise;
  }

  becomeReady() {
    this.events.push("ready");
    this.resolveReady();
  }

  failReady(error) {
    this.rejectReady(error);
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

  const lifecycle = await startProofLifecycle({
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
  assert.deepEqual(events, ["write:bootstrap"]);
  app.becomeReady();
  await lifecycle.completion;

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

  const lifecycle = await startProofLifecycle({
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
  assert.deepEqual(events, ["write:bootstrap"]);
  app.becomeReady();
  await lifecycle.completion;

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

test("application readiness rejection is persisted as a proof failure", async () => {
  const events = [];
  const app = new FakeApplication(events);
  const result = { passed: false, phase: "bootstrap" };

  const lifecycle = await startProofLifecycle({
    app,
    result,
    runProof: async () => {
      assert.fail("proof ran after readiness failed");
    },
    writeResult: async () => {
      events.push(`write:${result.phase}`);
    },
  });
  app.failReady(new Error("Electron readiness failed"));
  await lifecycle.completion;

  assert.deepEqual(events, ["write:bootstrap", "write:failed", "exit:1"]);
  assert.equal(result.error, "Electron readiness failed");
});
