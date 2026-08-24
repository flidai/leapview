import { readFile, stat } from "node:fs/promises";

const DIAGNOSTIC_JOURNAL_TIMEOUT_MS = 5_000;

const processGoneReasons = new Set([
  "clean-exit",
  "abnormal-exit",
  "killed",
  "crashed",
  "oom",
  "launch-failed",
  "integrity-failure",
  "memory-eviction",
]);

const updatePhases = new Set([
  "checking",
  "available",
  "not-available",
  "downloaded",
  "deferred",
  "restart-requested",
  "failed",
]);

export function verifyPackagedDiagnosticEvent(event) {
  if (typeof event !== "object" || event === null || Array.isArray(event)) {
    throw new Error("packaged diagnostic journal contains an invalid event");
  }
  if (
    typeof event.at !== "string" ||
    new Date(event.at).toISOString() !== event.at
  ) {
    throw new Error(
      "packaged diagnostic journal contains an invalid timestamp",
    );
  }
  if (event.kind === "startup") {
    if (
      Object.keys(event).sort().join(",") !== "at,kind,packaged" ||
      event.packaged !== true
    ) {
      throw new Error(
        "packaged diagnostic journal contains invalid startup data",
      );
    }
    return;
  }
  if (event.kind === "policy") {
    if (
      Object.keys(event).sort().join(",") !==
        "at,diagnostics,kind,mode,userInstances" ||
      !["open", "managed", "locked"].includes(event.mode) ||
      !["allowed", "restricted"].includes(event.userInstances) ||
      !["enabled", "disabled"].includes(event.diagnostics)
    ) {
      throw new Error(
        "packaged diagnostic journal contains invalid policy data",
      );
    }
    return;
  }
  if (event.kind === "update") {
    if (
      Object.keys(event).sort().join(",") !== "at,kind,phase" ||
      !updatePhases.has(event.phase)
    ) {
      throw new Error(
        "packaged diagnostic journal contains invalid update data",
      );
    }
    return;
  }
  if (event.kind === "render-process-gone") {
    if (
      Object.keys(event).sort().join(",") !== "at,kind,reason,surface" ||
      !["trusted-shell", "unknown"].includes(event.surface) ||
      !processGoneReasons.has(event.reason)
    ) {
      throw new Error(
        "packaged diagnostic journal contains invalid renderer data",
      );
    }
    return;
  }
  if (event.kind === "child-process-gone") {
    if (
      Object.keys(event).sort().join(",") !== "at,kind,processType,reason" ||
      ![
        "utility",
        "zygote",
        "sandbox-helper",
        "gpu",
        "pepper-plugin",
        "pepper-plugin-broker",
        "unknown",
      ].includes(event.processType) ||
      !processGoneReasons.has(event.reason)
    ) {
      throw new Error(
        "packaged diagnostic journal contains invalid child-process data",
      );
    }
    return;
  }
  throw new Error(
    "packaged diagnostic journal contains an unexpected startup event",
  );
}

export async function verifyPackagedDiagnosticJournal(
  path,
  timeoutMs = DIAGNOSTIC_JOURNAL_TIMEOUT_MS,
) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const body = await readFile(path, "utf8");
      if (Buffer.byteLength(body, "utf8") > 128 * 1024) {
        throw new Error("packaged diagnostic journal exceeds its size limit");
      }
      const document = JSON.parse(body);
      if (
        Object.keys(document).sort().join(",") !== "events,schemaVersion" ||
        document.schemaVersion !== 1 ||
        !Array.isArray(document.events) ||
        document.events.length === 0 ||
        document.events.length > 256
      ) {
        throw new Error(
          "packaged diagnostic journal has an unexpected manifest",
        );
      }
      if (
        !document.events.some(
          (event) => event?.kind === "startup" && event.packaged === true,
        )
      ) {
        throw new Error(
          "packaged diagnostic journal is missing its startup event",
        );
      }
      for (const event of document.events) {
        verifyPackagedDiagnosticEvent(event);
      }
      if (
        /https?:|origin|cookie|token|authorization|console|filename/iu.test(
          body,
        )
      ) {
        throw new Error(
          "packaged diagnostic journal contains forbidden sensitive fields",
        );
      }
      if (
        process.platform !== "win32" &&
        ((await stat(path)).mode & 0o077) !== 0
      ) {
        throw new Error(
          "packaged diagnostic journal permissions are not private",
        );
      }
      return;
    } catch (error) {
      if (
        error instanceof SyntaxError ||
        (error instanceof Error &&
          !("code" in error && error.code === "ENOENT"))
      ) {
        throw error;
      }
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
  }
  throw new Error("packaged application did not persist diagnostics");
}
