import { readFileSync } from "node:fs";
import { join } from "node:path";

export const PREVIEW_DISTRIBUTION_MARKER = "preview-distribution.json";
export const STABLE_DISTRIBUTION_MARKER = "stable-distribution.json";
const previewMarker = '{"schemaVersion":1,"channel":"preview","updates":false}\n';
const stableMarker = '{"schemaVersion":1,"channel":"stable","updates":true}\n';

export type DesktopDistribution =
  | "development"
  | "preview"
  | "stable"
  | "invalid";

export interface DesktopDistributionOptions {
  packaged: boolean;
  resourcesPath: string;
  readFile?: (path: string) => string;
}

export function resolveDesktopDistribution(
  options: DesktopDistributionOptions,
): DesktopDistribution {
  if (!options.packaged) {
    return "development";
  }
  const readFile =
    options.readFile ?? ((path: string) => readFileSync(path, "utf8"));
  const preview = readMarker(
    readFile,
    join(options.resourcesPath, PREVIEW_DISTRIBUTION_MARKER),
  );
  const stable = readMarker(
    readFile,
    join(options.resourcesPath, STABLE_DISTRIBUTION_MARKER),
  );
  if (preview === previewMarker && stable === undefined) {
    return "preview";
  }
  if (stable === stableMarker && preview === undefined) {
    return "stable";
  }
  return "invalid";
}

function readMarker(
  readFile: (path: string) => string,
  path: string,
): string | undefined | null {
  try {
    return readFile(path).replace(/\r\n?/gu, "\n");
  } catch (error) {
    return error instanceof Error &&
        "code" in error &&
        error.code === "ENOENT"
      ? undefined
      : null;
  }
}
