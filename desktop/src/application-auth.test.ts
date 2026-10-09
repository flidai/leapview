import { expect, test } from "bun:test";
import type { Session } from "electron";

import { DesktopAuthenticationCoordinator } from "./application-auth.js";
import type { DiagnosticEvent } from "./diagnostics.js";
import type { Profile } from "./profiles.js";

const profile: Profile = {
  id: "profile_0123456789abcdef0123456789abcdef",
  canonicalOrigin: "https://analytics.example.test",
  instanceId: "instance_0123456789abcdef0123456789abcdef",
  displayName: "Analytics",
  lastSafePath: "/explore",
  partitionVersion: 1,
};

function pendingSessionCheck() {
  let complete!: (response: Response) => void;
  let started!: () => void;
  const ready = new Promise<void>((resolve) => { started = resolve; });
  const response = new Promise<Response>((resolve) => { complete = resolve; });
  const session = {
    fetch: async () => { started(); return response; },
    clearStorageData: async () => undefined,
    clearCache: async () => undefined,
    clearAuthCache: async () => undefined,
    flushStorageData: () => undefined,
  } as unknown as Session;
  return { session, ready, complete };
}

for (const cancelMode of ["profile", "all"] as const) {
  for (const status of [204, 401]) {
    test(`${cancelMode} cancellation owns the pending session check (${status})`, async () => {
      const check = pendingSessionCheck();
      let opened = 0;
      const events: DiagnosticEvent[] = [];
      const coordinator = new DesktopAuthenticationCoordinator(async () => {
        opened += 1;
        throw new Error("unexpected authorization after cancellation");
      }, (event) => events.push(event));
      const outcome = coordinator.ensure(profile, check.session).then(
        () => "completed", () => "cancelled",
      );
      await check.ready;
      const activeDuringCheck = coordinator.size;
      const cancellation = cancelMode === "profile"
        ? coordinator.cancel(profile.id)
        : (coordinator.cancelAll(), Promise.resolve());
      check.complete(new Response(null, { status }));
      await cancellation;
      expect(await outcome).toBe("cancelled");
      expect(opened).toBe(0);
      expect(activeDuringCheck).toBe(1);
      expect(coordinator.size).toBe(0);
      expect(events).not.toContainEqual({ kind: "authentication", phase: "session-valid" });
      expect(events).not.toContainEqual({ kind: "authentication", phase: "completed" });
    });
  }
}

test("concurrent callers share preparation and a valid session", async () => {
  const check = pendingSessionCheck();
  const events: DiagnosticEvent[] = [];
  const coordinator = new DesktopAuthenticationCoordinator(async () => {
    throw new Error("a valid session must not authorize again");
  }, (event) => events.push(event));
  const first = coordinator.ensure(profile, check.session);
  await check.ready;
  const second = coordinator.ensure(profile, check.session);
  expect(coordinator.size).toBe(1);
  check.complete(new Response(null, { status: 204 }));
  await Promise.all([first, second]);
  expect(events).toEqual([{ kind: "authentication", phase: "session-valid" }]);
  expect(coordinator.size).toBe(0);
});

test("preparation consumes the transaction limit and releases it on completion", async () => {
  const check = pendingSessionCheck();
  const coordinator = new DesktopAuthenticationCoordinator(async () => undefined, () => undefined, 1);
  const first = coordinator.ensure(profile, check.session);
  await check.ready;
  const other = { ...profile, id: "profile_1123456789abcdef0123456789abcdef" };
  await expect(coordinator.ensure(other, check.session)).rejects.toThrow("Too many");
  check.complete(new Response(null, { status: 204 }));
  await first;
  await coordinator.ensure(other, check.session);
  expect(coordinator.size).toBe(0);
});

test("cancellation aborts the native session transport and permits a fresh attempt", async () => {
  let started!: () => void;
  const ready = new Promise<void>((resolve) => { started = resolve; });
  let aborted = false;
  let calls = 0;
  const session = {
    fetch: async (_input: string, init: RequestInit) => {
      calls += 1;
      if (calls > 1) return new Response(null, { status: 204 });
      started();
      return new Promise<Response>((_resolve, reject) => {
        init.signal!.addEventListener("abort", () => {
          aborted = true;
          reject(init.signal!.reason);
        }, { once: true });
      });
    },
  } as unknown as Session;
  const coordinator = new DesktopAuthenticationCoordinator(async () => undefined, () => undefined);
  const outcome = coordinator.ensure(profile, session).then(() => "completed", () => "cancelled");
  await ready;
  await coordinator.cancel(profile.id);
  expect(await outcome).toBe("cancelled");
  expect(aborted).toBe(true);
  expect(coordinator.size).toBe(0);
  await coordinator.ensure(profile, session);
  expect(calls).toBe(2);
});

test("immediate shutdown cancels before the session transport starts", async () => {
  let calls = 0;
  const session = { fetch: async () => { calls += 1; return new Response(null, { status: 204 }); } } as unknown as Session;
  const coordinator = new DesktopAuthenticationCoordinator(async () => undefined, () => undefined);
  const outcome = coordinator.ensure(profile, session).then(() => "completed", () => "cancelled");
  coordinator.cancelAll();
  expect(await outcome).toBe("cancelled");
  expect(calls).toBe(0);
  expect(coordinator.size).toBe(0);
});

test("concurrent invalid-session callers complete one real loopback authorization", async () => {
  let opened = 0;
  let checks = 0;
  let redeemed = 0;
  let cleared = 0;
  const events: DiagnosticEvent[] = [];
  const session = {
    fetch: async (input: string, init: RequestInit) => {
      if (input.endsWith("/auth/desktop/session")) {
        checks += 1;
        return new Response(null, { status: 401 });
      }
      expect(input).toBe(`${profile.canonicalOrigin}/auth/desktop/redeem`);
      expect(init.method).toBe("POST");
      expect(init.credentials).toBe("include");
      const form = new URLSearchParams(String(init.body));
      expect(form.get("profile_id")).toBe(profile.id);
      expect(form.get("instance_id")).toBe(profile.instanceId);
      redeemed += 1;
      return new Response(null, { status: 204 });
    },
    clearStorageData: async () => { cleared += 1; },
    clearCache: async () => undefined,
    clearAuthCache: async () => undefined,
    flushStorageData: () => undefined,
  } as unknown as Session;
  const coordinator = new DesktopAuthenticationCoordinator(async (rawURL) => {
    opened += 1;
    const authorization = new URL(rawURL);
    expect(authorization.origin).toBe(profile.canonicalOrigin);
    expect(authorization.pathname).toBe("/auth/desktop/authorize");
    const callback = new URL(authorization.searchParams.get("redirect_uri")!);
    callback.searchParams.set("state", authorization.searchParams.get("state")!);
    callback.searchParams.set("code", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq");
    const response = await fetch(callback);
    expect(response.ok).toBe(true);
  }, (event) => events.push(event));
  await Promise.all([coordinator.ensure(profile, session), coordinator.ensure(profile, session)]);
  expect({ opened, checks, redeemed, cleared }).toEqual({ opened: 1, checks: 1, redeemed: 1, cleared: 1 });
  expect(events).toEqual([
    { kind: "authentication", phase: "required" },
    { kind: "authentication", phase: "started" },
    { kind: "authentication", phase: "completed" },
  ]);
  expect(coordinator.size).toBe(0);
});
