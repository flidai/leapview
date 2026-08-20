/**
 * Removes undefined and empty optional fields while retaining security
 * requirement objects verbatim. This is the canonical JSON-IR normalization
 * boundary; keeping it separate makes byte stability explicit at emission.
 */
export function normalizeDocument<T>(value: T): T {
  if (Array.isArray(value)) {
    return value.filter((item) => item !== undefined).map((item) => normalizeDocument(item)) as T;
  }
  if (value && typeof value === "object") {
    if (isSecurityRequirementObject(value)) {
      return value;
    }
    const output: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value)) {
      if (child === undefined) continue;
      if (Array.isArray(child) && child.length === 0 && key !== "failures") continue;
      if (key === "extensions") {
        output[key] = child;
        continue;
      }
      output[key] = normalizeDocument(child);
    }
    return output as T;
  }
  return value;
}

function isSecurityRequirementObject(value: object): value is Record<string, string[]> {
  const entries = Object.entries(value);
  return entries.length > 0 && entries.every(([, child]) => Array.isArray(child));
}
