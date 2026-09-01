import { describe, expect, test } from "bun:test";
import {
  access,
  mkdtemp,
  readFile,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  DiagnosticJournal,
  normalizeChildProcessType,
  normalizeProcessGoneReason,
  writeDiagnosticReport,
  type DiagnosticEnvironment,
} from "./diagnostics.js";

const environment: DiagnosticEnvironment = {
  applicationVersion: "0.1.0",
  electronVersion: "44.0.0",
  chromiumVersion: "152.0.7977.54",
  nodeVersion: "24.14.0",
  platform: "darwin",
  osRelease: "25.5.0",
  architecture: "arm64",
  packaged: true,
  policyRevision: "desktop-policy-v1",
};

describe("DiagnosticJournal", () => {
  test("persists only allowlisted structured events in a private bounded journal", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      const path = join(directory, "diagnostics.json");
      let now = new Date("2026-07-28T12:00:00.000Z");
      const journal = await DiagnosticJournal.open(path, {
        now: () => now,
      });
      journal.record({ kind: "startup", packaged: true });
      journal.record({ kind: "discovery", outcome: "success" });
      journal.record({
        kind: "navigation",
        action: "blocked-main-frame",
      });
      journal.record({
        kind: "render-process-gone",
        surface: "remote",
        reason: "crashed",
      });
      journal.record({
        kind: "policy",
        mode: "managed",
        userInstances: "restricted",
        diagnostics: "enabled",
      });
      await journal.flush();

      const reopened = await DiagnosticJournal.open(path, {
        now: () => now,
      });
      expect(reopened.events()).toHaveLength(5);
      if (process.platform !== "win32") {
        expect((await stat(path)).mode & 0o777).toBe(0o600);
      }
      const body = await readFile(path, "utf8");
      expect(body).not.toContain("origin");
      expect(body).not.toContain("displayName");
      expect(body).not.toContain("console");

      now = new Date("2026-08-05T12:00:00.000Z");
      const expired = await DiagnosticJournal.open(path, {
        now: () => now,
      });
      expect(expired.events()).toEqual([]);
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  test("rejects remote strings, extra fields, and secret-seeding attempts", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      const journal = await DiagnosticJournal.open(
        join(directory, "diagnostics.json"),
      );
      const secrets = [
        "https://analytics.company.com/dashboards/acquisition?token=secret",
        "pkce-verifier-super-secret",
        "authorization-code-secret",
        "session-cookie-secret",
        "tenant-dashboard-title",
      ];
      for (const secret of secrets) {
        expect(() =>
          journal.record({
            kind: "navigation",
            action: "blocked-popup",
            message: secret,
          } as never),
        ).toThrow("diagnostic event");
      }
      expect(() =>
        journal.record({
          kind: "navigation",
          action: "remote-controlled-event-name",
        } as never),
      ).toThrow("diagnostic event");

      journal.record({ kind: "navigation", action: "blocked-popup" });
      const serialized = JSON.stringify(journal.report(environment));
      for (const secret of secrets) {
        expect(serialized).not.toContain(secret);
      }
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  test("coalesces repeated decisions and caps retained event count", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      let currentTime = Date.parse("2026-07-28T12:00:00.000Z");
      const journal = await DiagnosticJournal.open(
        join(directory, "diagnostics.json"),
        { now: () => new Date(currentTime) },
      );
      for (let index = 0; index < 20; index += 1) {
        journal.record({ kind: "navigation", action: "blocked-popup" });
      }
      expect(journal.events()).toHaveLength(1);

      for (let index = 0; index < 300; index += 1) {
        currentTime += 31_000;
        journal.record({
          kind: "profile",
          action: index % 2 === 0 ? "opened" : "disconnected",
          outcome: "success",
        });
      }
      expect(journal.events()).toHaveLength(256);
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  test("ignores corrupt local state and honors disabled collection", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      const corruptPath = join(directory, "corrupt.json");
      await writeFile(
        corruptPath,
        `{"schemaVersion":1,"events":[{"kind":"navigation","url":"secret"}]}`,
        { mode: 0o600 },
      );
      expect(
        (
          await DiagnosticJournal.open(corruptPath)
        ).events(),
      ).toEqual([]);

      const disabledPath = join(directory, "disabled.json");
      const disabled = await DiagnosticJournal.open(disabledPath, {
        enabled: false,
      });
      disabled.record({ kind: "startup", packaged: true });
      await disabled.flush();
      expect(disabled.events()).toEqual([]);
      await expect(access(disabledPath)).rejects.toThrow();
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  test("normalizes untrusted process details into fixed allowlisted values", () => {
    expect(normalizeProcessGoneReason("crashed")).toBe("crashed");
    expect(
      normalizeProcessGoneReason(
        "https://analytics.company.com/?token=secret",
      ),
    ).toBe("abnormal-exit");
    expect(normalizeChildProcessType("Sandbox Helper")).toBe(
      "sandbox-helper",
    );
    expect(normalizeChildProcessType("tenant-dashboard-title")).toBe(
      "unknown",
    );
  });

  test("accepts only derived managed policy revisions", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      const journal = await DiagnosticJournal.open(
        join(directory, "diagnostics.json"),
      );
      expect(
        journal.report({
          ...environment,
          policyRevision:
            "desktop-policy-v1-managed-0123456789abcdef",
        }).environment.policyRevision,
      ).toBe("desktop-policy-v1-managed-0123456789abcdef");
      expect(() =>
        journal.report({
          ...environment,
          policyRevision: "tenant-controlled-policy-name",
        }),
      ).toThrow("environment");
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });

  test("produces a reviewable exact-manifest report and writes it privately", async () => {
    const directory = await mkdtemp(join(tmpdir(), "leapview-diagnostics-"));
    try {
      const journal = await DiagnosticJournal.open(
        join(directory, "diagnostics.json"),
        {
          now: () => new Date("2026-07-28T12:00:00.000Z"),
        },
      );
      journal.record({
        kind: "child-process-gone",
        processType: "gpu",
        reason: "oom",
      });
      const report = journal.report(environment);
      expect(report.manifest.files).toEqual([
        {
          name: "leapview-diagnostic-report.json",
          sections: ["environment", "privacy", "events"],
        },
      ]);
      expect(report.manifest.eventFields.policy).toEqual([
        "at",
        "kind",
        "mode",
        "userInstances",
        "diagnostics",
      ]);
      expect(report.privacy).toEqual({
        crashCollection: "disabled",
        crashUpload: "disabled",
        rendererConsole: "not-collected",
        minidumps: "excluded",
        instanceOrigins: "excluded",
        instanceMetadata: "excluded",
        credentials: "excluded",
        retentionDays: 7,
      });
      expect(report.environment).toEqual(environment);

      const reportPath = join(
        directory,
        "leapview-diagnostic-report.json",
      );
      const poisonedReport = structuredClone(report);
      Object.assign(poisonedReport.events[0] as object, {
        url: "https://analytics.company.com/?token=secret",
      });
      await expect(
        writeDiagnosticReport(reportPath, poisonedReport),
      ).rejects.toThrow("diagnostic event");
      const poisonedPrivacy = structuredClone(report);
      Object.assign(poisonedPrivacy.privacy, {
        credentials: "session-cookie-secret",
      });
      await expect(
        writeDiagnosticReport(reportPath, poisonedPrivacy),
      ).rejects.toThrow("diagnostic report");

      await writeDiagnosticReport(reportPath, report);
      if (process.platform !== "win32") {
        expect((await stat(reportPath)).mode & 0o777).toBe(0o600);
      }
      expect(JSON.parse(await readFile(reportPath, "utf8"))).toEqual(report);
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });
});
