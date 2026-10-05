import assert from "node:assert/strict";
import test from "node:test";

import { readTrustedShellAccessibility, verifyTrustedShellAccessibility } from "./accessibility-contract.mjs";

const node = (role, name, properties = []) => ({
  role: { value: role },
  name: { value: name },
  properties,
});

const treeNode = (
  id,
  role,
  name,
  properties = [],
  childIds = [],
) => ({
  ...node(role, name, properties),
  nodeId: id,
  childIds,
});

// Exercise the actual protocol listener, including malformed debugger replies.
async function withDebugger(t, onCommand, check, beforeOpen) {
  const original = globalThis.WebSocket;
  const sockets = [];
  const listenerErrors = [];
  class DebuggerSocket {
    static OPEN = 1;
    readyState = 1;
    listeners = new Map();
    closed = false;
    constructor() {
      sockets.push(this);
      if (beforeOpen) {
        this.readyState = 0;
        queueMicrotask(() => beforeOpen(this));
      }
    }
    addEventListener(type, listener) {
      const listeners = this.listeners.get(type) ?? new Set();
      listeners.add(listener);
      this.listeners.set(type, listeners);
    }
    removeEventListener(type, listener) {
      this.listeners.get(type)?.delete(listener);
    }
    emit(type, event = {}) {
      for (const listener of this.listeners.get(type) ?? []) {
        try { listener(event); } catch (error) { listenerErrors.push(error); }
      }
    }
    receive(data) { this.emit("message", { data }); }
    send(raw) { queueMicrotask(() => onCommand(this, JSON.parse(raw))); }
    close() {
      if (!this.closed) {
        this.closed = true;
        this.emit("close");
      }
    }
  }
  globalThis.WebSocket = DebuggerSocket;
  t.after(() => { globalThis.WebSocket = original; });
  await check(readTrustedShellAccessibility("ws://127.0.0.1:9222/devtools/page/test"));
  assert.deepEqual(listenerErrors, [], "protocol listener must not throw");
  assert.ok(sockets.every(socket => socket.closed), "debugger socket must close");
}

test("rejects JSON null from the debugger without an uncaught listener error", async (t) => {
  await withDebugger(t, (socket, command) => {
    socket.receive("null");
    // Settle the old implementation too, so a failing regression leaves no timer.
    socket.receive(JSON.stringify({ id: command.id, error: {} }));
  }, result => assert.rejects(result, /response is (invalid|malformed)/u));
});

for (const [name, frame] of [
  ["array", "[]"], ["string", '"payload"'], ["number", "42"],
  ["boolean", "true"], ["malformed JSON", "{"],
  ["oversized frame", " ".repeat(4 * 1024 * 1024 + 1)],
  ["binary frame", new Uint8Array([1])], ["empty envelope", "{}"],
  ["string id", '{"id":"1"}'], ["prototype id", '{"id":"__proto__"}'],
  ["zero id", '{"id":0}'], ["negative id", '{"id":-1}'],
  ["unsafe id", '{"id":9007199254740992}'],
]) {
  test(`rejects debugger ${name} cleanly`, { timeout: 1_000 }, async (t) => {
    await withDebugger(t, socket => socket.receive(frame),
      result => assert.rejects(result, /response.*(invalid|malformed)/u));
  });
}

for (const event of ["close", "error"]) {
  test(`rejects a pending command on debugger ${event}`, { timeout: 1_000 }, async (t) => {
    await withDebugger(t, socket => socket.emit(event),
      result => assert.rejects(result, /debugger (closed|failed)/u));
  });
  test(`rejects debugger ${event} before opening`, { timeout: 1_000 }, async (t) => {
    await withDebugger(t, () => assert.fail("must not send a command"),
      result => assert.rejects(result, /failed to open/u), socket => socket.emit(event));
  });
}

test("rejects protocol errors without leaking a pending call", async (t) => {
  await withDebugger(t, (socket, command) => {
    socket.receive(JSON.stringify({ id: command.id, error: { code: -1 } }));
  }, result => assert.rejects(result, /Accessibility.enable failed/u));
});

test("ignores notifications and unknown/duplicate ids while resolving only registered calls", async (t) => {
  const calls = [];
  await withDebugger(t, (socket, command) => {
    calls.push(command.method);
    socket.receive('{"method":"Accessibility.nodesUpdated","params":{}}');
    socket.receive('{"id":12345,"error":{}}');
    const result = command.method === "Accessibility.enable" ? {} : { nodes: [
      node("RootWebArea", "LeapView", [{ name: "focused", value: { value: true } }]),
      node("main", ""), node("heading", "Connect to LeapView"),
      node("region", "Connect an instance"),
      node("textbox", "LeapView URL", [
        { name: "focused", value: { value: true } },
        { name: "required", value: { value: true } },
      ]), node("button", "Verify & open"),
    ] };
    socket.receive(JSON.stringify({ id: command.id, result }));
    socket.receive(JSON.stringify({ id: command.id, error: {} }));
  }, async result => assert.equal((await result).mode, "open"));
  assert.deepEqual(calls, ["Accessibility.enable", "Accessibility.getFullAXTree"]);
});

test("accepts the named, focused trusted-shell accessibility contract", () => {
  const report = verifyTrustedShellAccessibility([
    node("RootWebArea", "LeapView", [
      { name: "focused", value: { value: true } },
    ]),
    node("main", ""),
    node("heading", "Connect to LeapView"),
    node("region", "Connect an instance"),
    node("textbox", "LeapView URL", [
      { name: "focused", value: { value: true } },
      { name: "required", value: { value: true } },
    ]),
    node("button", "Verify & open"),
  ]);

  assert.deepEqual(report, {
    mode: "open",
    announcement: "none",
    controls: 2,
    focusedControl: "LeapView URL",
    regions: ["Connect an instance"],
  });
});

test("accepts the focused fail-closed managed-policy state", () => {
  const report = verifyTrustedShellAccessibility([
    node("RootWebArea", "LeapView", [
      { name: "focused", value: { value: true } },
    ]),
    node("main", ""),
    node("heading", "Connect to LeapView"),
    node(
      "alert",
      "The managed desktop configuration is invalid; contact your administrator.",
      [{ name: "live", value: { value: "assertive" } }],
    ),
  ]);

  assert.deepEqual(report, {
    mode: "locked",
    announcement: "assertive",
    controls: 0,
    focusedControl: "Application document",
    regions: [],
  });
});

test("accepts Windows AX trees that expose alert text as a child", () => {
  const report = verifyTrustedShellAccessibility([
    treeNode("root", "RootWebArea", "LeapView", [
      { name: "focused", value: { value: true } },
    ], ["main"]),
    treeNode("main", "main", "", [], ["heading", "alert"]),
    treeNode("heading", "heading", "Connect to LeapView"),
    treeNode("alert", "alert", "", [
      { name: "live", value: { value: "assertive" } },
    ], ["alert-text"]),
    treeNode(
      "alert-text",
      "StaticText",
      "The managed desktop configuration is invalid; contact your administrator.",
    ),
  ]);

  assert.deepEqual(report, {
    mode: "locked",
    announcement: "assertive",
    controls: 0,
    focusedControl: "Application document",
    regions: [],
  });
});

test("does not mistake unrelated accessible text for the policy alert", () => {
  assert.throws(
    () =>
      verifyTrustedShellAccessibility([
        treeNode("root", "RootWebArea", "LeapView", [
          { name: "focused", value: { value: true } },
        ], ["main"]),
        treeNode("main", "main", "", [], ["heading", "alert", "text"]),
        treeNode("heading", "heading", "Connect to LeapView"),
        treeNode("alert", "alert", "", [
          { name: "live", value: { value: "assertive" } },
        ]),
        treeNode(
          "text",
          "StaticText",
          "The managed desktop configuration is invalid; contact your administrator.",
        ),
      ]),
    /managed configuration alert/u,
  );
});

test("rejects missing landmarks, names, and deterministic initial focus", () => {
  for (const nodes of [
    [
      node("RootWebArea", "LeapView"),
      node("heading", "Connect to LeapView"),
      node("textbox", "LeapView URL"),
      node("button", "Verify & open"),
    ],
    [
      node("RootWebArea", "LeapView"),
      node("main", ""),
      node("heading", "Connect to LeapView"),
      node("region", "Connect an instance"),
      node("textbox", "", [
        { name: "focused", value: { value: true } },
      ]),
      node("button", "Verify & open"),
    ],
    [
      node("RootWebArea", "LeapView"),
      node("main", ""),
      node("heading", "Connect to LeapView"),
      node("region", "Connect an instance"),
      node("textbox", "LeapView URL"),
      node("button", "Verify & open"),
    ],
  ]) {
    assert.throws(
      () => verifyTrustedShellAccessibility(nodes),
      /accessib/u,
    );
  }
});

test("rejects duplicate or unnamed interactive controls", () => {
  assert.throws(
    () =>
      verifyTrustedShellAccessibility([
        node("RootWebArea", "LeapView"),
        node("main", ""),
        node("heading", "Connect to LeapView"),
        node("region", "Connect an instance"),
        node("textbox", "LeapView URL", [
          { name: "focused", value: { value: true } },
        ]),
        node("button", "Verify & open"),
        node("button", ""),
      ]),
    /accessible name/u,
  );
});
