import { describe, expect, test } from "bun:test";
import { chmod, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";

import { DesktopDiscoveryError } from "./discovery.js";
import {
  profilePartitionName,
  ProfileStore,
} from "./profiles.js";

const discovery = {
  schemaVersion: 1,
  canonicalOrigin: "https://analytics.company.com",
  instanceId: "instance_0123456789abcdef0123456789abcdef",
  displayName: "Company Analytics",
  serverVersion: "v1.4.2",
  desktopProtocolMin: 1,
  desktopProtocolMax: 1,
  authenticationModes: ["browser-session", "system-browser-pkce"],
  capabilities: ["remote-web"],
};

describe("ProfileStore", () => {
  test("persists only non-secret connection metadata in a private file", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const path = join(directoryPath, "profiles.json");
      const store = new ProfileStore(path);
      const profile = await store.upsertFromDiscovery(discovery);

      expect(profile.id).toMatch(/^profile_[0-9a-f]{32}$/);
      expect(await new ProfileStore(path).list()).toEqual([profile]);
      if (process.platform !== "win32") {
        expect((await stat(path)).mode & 0o777).toBe(0o600);
      }
      const persisted = await readFile(path, "utf8");
      expect(persisted).not.toContain("cookie");
      expect(persisted).not.toContain("token");
      expect(persisted).not.toContain("password");
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("updates an existing profile by immutable instance id", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const store = new ProfileStore(join(directoryPath, "profiles.json"));
      const first = await store.upsertFromDiscovery(discovery);
      const updated = await store.upsertFromDiscovery({
        ...discovery,
        displayName: "Renamed Analytics",
      });
      expect(updated.id).toBe(first.id);
      expect(await store.list()).toHaveLength(1);
      expect(updated.displayName).toBe("Renamed Analytics");
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("keeps a user label separate from the server-controlled display name", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const store = new ProfileStore(join(directoryPath, "profiles.json"));
      const profile = await store.upsertFromDiscovery(discovery);

      const renamed = await store.setLabel(profile.id, "Quarterly reporting");
      const rediscovered = await store.upsertFromDiscovery({
        ...discovery,
        displayName: "Company Analytics v2",
      });

      expect(renamed.label).toBe("Quarterly reporting");
      expect(rediscovered.label).toBe("Quarterly reporting");
      expect(rediscovered.displayName).toBe("Company Analytics v2");
      expect((await store.setLabel(profile.id, null)).label).toBeUndefined();
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("persists only a validated safe route for lifecycle recovery", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const path = join(directoryPath, "profiles.json");
      const store = new ProfileStore(path);
      const profile = await store.upsertFromDiscovery(discovery);

      const updated = await store.setLastSafePath(
        profile.id,
        "/dashboards/sales",
      );

      expect(updated.lastSafePath).toBe("/dashboards/sales");
      expect((await new ProfileStore(path).list())[0]?.lastSafePath).toBe(
        "/dashboards/sales",
      );
      for (const candidate of [
        "https://attacker.example/",
        "//attacker.example/",
        "/admin",
        "/dashboards/%2fadmin",
        "/explore?token=secret",
        "/explore#fragment",
        "/".repeat(2_049),
      ]) {
        await expect(
          store.setLastSafePath(profile.id, candidate),
        ).rejects.toThrow("safe path");
      }
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("confirmed replacement gets a new mapping and partition", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const store = new ProfileStore(join(directoryPath, "profiles.json"));
      const original = await store.upsertFromDiscovery(discovery);
      await store.setLabel(original.id, "Finance reporting");
      const replacementDiscovery = {
        ...discovery,
        instanceId: "instance_abcdef0123456789abcdef0123456789",
        displayName: "Replacement Analytics",
      };

      const replacement = await store.replaceFromDiscovery(
        original.id,
        replacementDiscovery,
      );

      expect(replacement.id).not.toBe(original.id);
      expect(replacement.label).toBe("Finance reporting");
      expect(replacement.lastSafePath).toBe("/");
      expect(profilePartitionName(replacement)).not.toBe(
        profilePartitionName(original),
      );
      expect(await store.list()).toEqual([replacement]);
      await expect(store.remove(original.id)).rejects.toThrow("not found");
      await expect(
        store.replaceFromDiscovery(replacement.id, {
          ...replacementDiscovery,
          canonicalOrigin: "https://unrelated.example.com",
          instanceId: "instance_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        }),
      ).rejects.toThrow("not related");
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("removes only the selected profile and cannot reopen a stale mapping", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const store = new ProfileStore(join(directoryPath, "profiles.json"));
      const first = await store.upsertFromDiscovery(discovery);
      const second = await store.upsertFromDiscovery({
        ...discovery,
        canonicalOrigin: "https://finance.company.com",
        instanceId: "instance_abcdef0123456789abcdef0123456789",
        displayName: "Finance",
      });
      await store.remove(first.id);
      expect(await store.list()).toEqual([second]);
      await expect(store.remove(first.id)).rejects.toThrow("not found");
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("detects origin replacement and instance migration instead of silently trusting them", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const store = new ProfileStore(join(directoryPath, "profiles.json"));
      await store.upsertFromDiscovery(discovery);
      try {
        await store.upsertFromDiscovery({
          ...discovery,
          instanceId: "instance_abcdef0123456789abcdef0123456789",
        });
        throw new Error("expected instance identity mismatch");
      } catch (error) {
        expect(error).toBeInstanceOf(DesktopDiscoveryError);
        expect((error as DesktopDiscoveryError).kind).toBe(
          "instance_identity_mismatch",
        );
      }
      try {
        await store.upsertFromDiscovery({
          ...discovery,
          canonicalOrigin: "https://new.company.com",
        });
        throw new Error("expected canonical origin mismatch");
      } catch (error) {
        expect(error).toBeInstanceOf(DesktopDiscoveryError);
        expect((error as DesktopDiscoveryError).kind).toBe(
          "canonical_origin_mismatch",
        );
      }
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });

  test("fails closed on corrupt or overly permissive storage", async () => {
    const directoryPath = await mkdtemp(join(tmpdir(), "leapview-profiles-"));
    try {
      const path = join(directoryPath, "profiles.json");
      await writeFile(path, `{"schemaVersion":1,"profiles":[{"id":"bad"}]}`, {
        mode: 0o600,
      });
      await expect(new ProfileStore(path).list()).rejects.toThrow("profile");
      await writeFile(path, `{"schemaVersion":1,"profiles":[]}`);
      await chmod(path, 0o644);
      if (process.platform !== "win32") {
        await expect(new ProfileStore(path).list()).rejects.toThrow("permissions");
      }
      await chmod(path, 0o600);
      await writeFile(path, JSON.stringify({
        schemaVersion: 3,
        profiles: [],
      }), { mode: 0o600 });
      await expect(new ProfileStore(path).list()).rejects.toThrow(
        "schema version",
      );
      await writeFile(path, JSON.stringify({
        schemaVersion: 2,
        profiles: [{
          id: "profile_0123456789abcdef0123456789abcdef",
          canonicalOrigin: discovery.canonicalOrigin,
          instanceId: discovery.instanceId,
          displayName: discovery.displayName,
          lastSafePath: "/",
          partitionVersion: 1,
          token: "must-not-be-tolerated",
        }],
      }), { mode: 0o600 });
      await expect(new ProfileStore(path).list()).rejects.toThrow(
        "unknown fields",
      );
    } finally {
      await rm(directoryPath, { force: true, recursive: true });
    }
  });
});
