const maximumOperations = 64;
const maximumDurationMs = 40_000;

export function createStorageOperationDiagnostics(result) {
  const startTimes = new WeakMap();

  async function run(label, operation) {
    const diagnostic = begin(label);

    try {
      const value = await operation();
      diagnostic.status = "complete";
      return value;
    } catch (error) {
      diagnostic.status = "failed";
      throw error;
    } finally {
      updateDuration(diagnostic);
    }
  }

  function runSync(label, operation) {
    const diagnostic = begin(label);

    try {
      const value = operation();
      diagnostic.status = "complete";
      return value;
    } catch (error) {
      diagnostic.status = "failed";
      throw error;
    } finally {
      updateDuration(diagnostic);
    }
  }

  function updateActive() {
    for (const diagnostic of result.storageDiagnostics) {
      if (diagnostic.status === "running") {
        updateDuration(diagnostic);
      }
    }
  }

  function updateDuration(diagnostic) {
    const startedAt = startTimes.get(diagnostic);
    if (startedAt === undefined) {
      return;
    }
    diagnostic.durationMs = Math.min(
      maximumDurationMs,
      Math.max(0, Math.round(performance.now() - startedAt)),
    );
    if (diagnostic.status !== "running") {
      startTimes.delete(diagnostic);
    }
  }

  function begin(label) {
    result.currentCheck = `storage.cross-profile.${label}`;
    const diagnostic = {
      operation: label,
      status: "running",
      durationMs: 0,
    };
    if (result.storageDiagnostics.length < maximumOperations) {
      result.storageDiagnostics.push(diagnostic);
      startTimes.set(diagnostic, performance.now());
    }
    return diagnostic;
  }

  return { run, runSync, updateActive };
}
