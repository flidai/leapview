import {readdir, readFile} from "node:fs/promises";
import path from "node:path";

export type PrimerAlignmentViolation = {
  file: string;
  line: number;
  kind:
    | "raw-color"
    | "raw-font-size"
    | "raw-var-fallback"
    | "undefined-token"
    | "runtime-undefined-token"
    | "local-primer-token"
    | "standard-state-color-mix"
    | "asset-token"
    | "primer-primary-button-token"
    | "primitive-typography-alias"
    | "button-contract";
  message: string;
};

type CheckPrimerAlignmentOptions = {
  root?: string;
  referenceRoot?: string;
  sourceFiles?: string[];
};

const cssSourceExtensions = new Set([".css", ".ts"]);
const productSourceRoots = [path.join("web", "components"), path.join("site", "web")];
const staticSourceFiles = [path.join("static", "app.input.css"), path.join("site", "static", "site.css")];
const compiledRuntimeSourceFiles = [path.join("static", "app.css")];
const excludedPathParts = [
  `${path.sep}web${path.sep}components${path.sep}inspector${path.sep}datastar-inspector.ts`,
  `${path.sep}web${path.sep}vendor${path.sep}`,
];
const runtimeTokenNames = new Set([
  "--lv-cell-bar-color",
  "--lv-cell-bar-width",
  "--lv-cell-bg-color",
  "--lv-cell-bg-fade",
  "--lv-group-head-height",
  "--lv-head-top",
  "--lv-pin-left",
  "--lv-resize-guide-x",
  "--lv-row-height",
  "--lv-table-columns",
  "--lv-table-width",
  "--lv-windowed-resize-guide-x",
  "--lv-windowed-row-height",
  "--lv-windowed-table-columns",
  "--lv-windowed-table-width",
  "--report-canvas-height",
  "--report-canvas-scale",
  "--report-canvas-width",
]);
const checkedTokenPattern =
  /^--(?:site|lv|base|motion|control|controlStack|border|borderColor|zIndex|shadow|fgColor|bgColor|data|label|button|overlay|text|fontStack|stack|selection|card|dashboard|report|color|spacing|container|radius|duration|ease|breakpoint|outline|focus)-/;
const standardStateTokenPattern = /^--lv-bg-(?:hover|control-hover|control-active|selected)$/;
const standardStateSelectorPattern = /(?:\[aria-pressed=['"]true['"]\]|:focus-visible|:focus(?![-\w])|\.day\.in-range)/;
const standardButtonContractSelectorPattern =
  /(?:\.icon-action|\.options\s+summary|\.visual-options\s+summary|\.menu\s+button|button\.header-button|\.collapse-button|\.theme-button|\.storage-(?:table-button|breadcrumb-button|schema-table-link))/;
const directButtonStylingPattern =
  /(?:\b(?:min-height|width|height|padding)\s*:\s*[^;]*var\(--control-|\bborder\s*:\s*0\b|\bbackground\s*:\s*(?:transparent|var\(--(?:control|lv-bg-panel-muted|lv-bg-control-hover))|\boutline\s*:\s*0\b)/;

async function listFiles(root: string, relativeDir: string): Promise<string[]> {
  const absoluteDir = path.join(root, relativeDir);
  const entries = await readdir(absoluteDir, {withFileTypes: true});
  const files = await Promise.all(
    entries.map(async entry => {
      const relativePath = path.join(relativeDir, entry.name);
      const absolutePath = path.join(root, relativePath);

      if (entry.isDirectory()) {
        return listFiles(root, relativePath);
      }

      if (!entry.isFile() || !cssSourceExtensions.has(path.extname(entry.name))) {
        return [];
      }

      if (entry.name.endsWith(".test.ts") || entry.name.endsWith(".dom.test.ts")) {
        return [];
      }

      if (excludedPathParts.some(excluded => absolutePath.includes(excluded))) {
        return [];
      }

      return [relativePath];
    }),
  );

  return files.flat();
}

async function defaultSourceFiles(root: string): Promise<string[]> {
  const files = await Promise.all(productSourceRoots.map(sourceRoot => listFiles(root, sourceRoot)));

  return [...staticSourceFiles, ...files.flat()].sort((left, right) => left.localeCompare(right));
}

function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, match => " ".repeat(match.length));
}

function cssBlocksForFile(file: string, content: string): string[] {
  if (file.endsWith(".css")) {
    return [content];
  }

  return Array.from(content.matchAll(/css`([\s\S]*?)`/g), match => match[1] ?? []);
}

function lineNumber(content: string, index: number): number {
  return content.slice(0, index).split("\n").length;
}

function tokenDefinitions(content: string): Set<string> {
  return new Set(Array.from(content.matchAll(/(--[A-Za-z0-9_-]+)\s*:/g), match => match[1]));
}

function tokenReferences(content: string): Set<string> {
  return new Set(Array.from(content.matchAll(/var\(\s*(--[A-Za-z0-9_-]+)/g), match => match[1]));
}

async function referenceTokenDefinitions(referenceRoot: string): Promise<Set<string>> {
  const entries = await readdir(referenceRoot, {withFileTypes: true});
  const definitions = new Set<string>();

  for (const entry of entries) {
    if (!entry.isFile() || !entry.name.endsWith(".css")) continue;
    const content = await readFile(path.join(referenceRoot, entry.name), "utf8");
    for (const token of tokenDefinitions(content)) {
      definitions.add(token);
    }
  }

  return definitions;
}

function addViolation(
  violations: PrimerAlignmentViolation[],
  file: string,
  css: string,
  index: number,
  kind: PrimerAlignmentViolation["kind"],
  message: string,
): void {
  violations.push({
    file,
    line: lineNumber(css, index),
    kind,
    message,
  });
}

function scanCssForValueViolations(file: string, css: string, violations: PrimerAlignmentViolation[]): void {
  const uncommented = stripCssComments(css);

  for (const match of uncommented.matchAll(/#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(/g)) {
    addViolation(violations, file, uncommented, match.index ?? 0, "raw-color", "Use a Primer or LeapView semantic token instead of a raw color.");
  }

  for (const match of uncommented.matchAll(/\bfont-size\s*:[^;{}]*[0-9]*\.?[0-9]+(?:px|rem)\b/g)) {
    addViolation(violations, file, uncommented, match.index ?? 0, "raw-font-size", "Use a Primer typography token or LeapView semantic type recipe instead of a raw font size.");
  }

  for (const match of uncommented.matchAll(/--lv-(?:font-(?:family|size|weight)|line-height)-[A-Za-z0-9_-]+\s*:\s*var\(/g)) {
    addViolation(
      violations,
      file,
      uncommented,
      match.index ?? 0,
      "primitive-typography-alias",
      "Use Primer typography primitives directly or define a complete --lv-type-* semantic recipe.",
    );
  }

  for (const match of uncommented.matchAll(/var\(\s*(--[A-Za-z0-9_-]+)\s*,\s*(#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(|[0-9.]+(?:px|rem|em|ms|s)\b|white\b|black\b|transparent\b)/g)) {
    const tokenName = match[1];
    if (runtimeTokenNames.has(tokenName)) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "raw-var-fallback", `Use a central token fallback for ${tokenName}, not a raw design value.`);
  }

  for (const match of uncommented.matchAll(/(--lv-bg-[A-Za-z0-9_-]+)\s*:\s*([^;]*color-mix\([^;]+);/g)) {
    const tokenName = match[1];
    if (!standardStateTokenPattern.test(tokenName)) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "standard-state-color-mix", `${tokenName} is a standard UI state token; map it directly to Primer control tokens.`);
  }

  for (const match of uncommented.matchAll(/([^{}]+)\{([^{}]*color-mix\([^{}]+)\}/g)) {
    const selector = match[1] ?? "";
    if (!standardStateSelectorPattern.test(selector)) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "standard-state-color-mix", "Standard pressed, focus, and date-range states must use Primer state tokens instead of color-mix().");
  }

  for (const match of uncommented.matchAll(/(--lv-asset-[A-Za-z0-9_-]+)\s*:\s*([^;]+);/g)) {
    const tokenName = match[1];
    const value = match[2] ?? "";
    if (!value.includes("color-mix(") && !value.includes("--data-")) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "asset-token", `${tokenName} styles standard asset labels; use Primer label tokens instead of data palette or color-mix values.`);
  }

  for (const match of uncommented.matchAll(/var\(\s*(--button-primary-[A-Za-z0-9_-]+)/g)) {
    const tokenName = match[1];
    addViolation(violations, file, uncommented, match.index ?? 0, "primer-primary-button-token", `${tokenName} is Primer's success-colored primary button token; use LeapView accent button aliases instead.`);
  }

  for (const match of uncommented.matchAll(/([^{}]+)\{([^{}]+)\}/g)) {
    const selector = match[1] ?? "";
    const body = match[2] ?? "";
    if (!standardButtonContractSelectorPattern.test(selector)) continue;
    if (body.includes("--lv-button-")) continue;
    if (!directButtonStylingPattern.test(body)) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "button-contract", "Standard button selectors must use LeapView --lv-button-* aliases instead of direct control sizing, transparent backgrounds, or outline resets.");
  }
}

function scanCssForTokenViolations(
  file: string,
  css: string,
  allDefinitions: Set<string>,
  primerDefinitions: Set<string>,
  violations: PrimerAlignmentViolation[],
): void {
  const uncommented = stripCssComments(css);

  for (const match of uncommented.matchAll(/(--base-size-[A-Za-z0-9_-]+)\s*:/g)) {
    const tokenName = match[1];
    if (primerDefinitions.has(tokenName)) continue;
    addViolation(violations, file, uncommented, match.index ?? 0, "local-primer-token", `${tokenName} extends the Primer base-size namespace locally; use an --lv-* alias.`);
  }

  for (const tokenName of tokenReferences(uncommented)) {
    if (!checkedTokenPattern.test(tokenName) || runtimeTokenNames.has(tokenName)) continue;
    if (allDefinitions.has(tokenName)) continue;
    const index = uncommented.indexOf(tokenName);
    addViolation(violations, file, uncommented, index, "undefined-token", `${tokenName} is referenced but is not defined by Primer or the LeapView token layer.`);
  }
}

function scanCssForRuntimeTokenViolations(
  file: string,
  css: string,
  sourceDefinitions: Set<string>,
  runtimeDefinitions: Set<string>,
  violations: PrimerAlignmentViolation[],
): void {
  const uncommented = stripCssComments(css);

  for (const tokenName of tokenReferences(uncommented)) {
    if (!checkedTokenPattern.test(tokenName) || runtimeTokenNames.has(tokenName)) continue;
    if (!sourceDefinitions.has(tokenName) || runtimeDefinitions.has(tokenName)) continue;
    const index = uncommented.indexOf(tokenName);
    addViolation(
      violations,
      file,
      uncommented,
      index,
      "runtime-undefined-token",
      `${tokenName} is defined in source but is absent from the compiled CSS loaded by the site.`,
    );
  }
}

export async function checkPrimerAlignment(options: CheckPrimerAlignmentOptions = {}): Promise<PrimerAlignmentViolation[]> {
  const root = options.root ?? process.cwd();
  const referenceRoot = options.referenceRoot ?? path.join(root, "docs", "reference", "primer-primitives-css");
  const sourceFiles = options.sourceFiles ?? (await defaultSourceFiles(root));
  const primerDefinitions = await referenceTokenDefinitions(referenceRoot);
  const cssByFile = new Map<string, string[]>();
  const allDefinitions = new Set<string>([...primerDefinitions, ...runtimeTokenNames]);

  for (const file of sourceFiles) {
    const content = await readFile(path.join(root, file), "utf8");
    const blocks = cssBlocksForFile(file, content);
    cssByFile.set(file, blocks);
    for (const block of blocks) {
      for (const tokenName of tokenDefinitions(stripCssComments(block))) {
        allDefinitions.add(tokenName);
      }
    }
  }

  const violations: PrimerAlignmentViolation[] = [];

  for (const [file, blocks] of cssByFile) {
    for (const block of blocks) {
      scanCssForValueViolations(file, block, violations);
      scanCssForTokenViolations(file, block, allDefinitions, primerDefinitions, violations);
    }
  }

  const siteFiles = sourceFiles.filter(file => file.startsWith(`${path.join("site")}${path.sep}`));
  if (siteFiles.length > 0) {
    const runtimeDefinitions = new Set<string>(runtimeTokenNames);
    for (const file of [...compiledRuntimeSourceFiles, ...siteFiles]) {
      const content = await readFile(path.join(root, file), "utf8");
      for (const block of cssBlocksForFile(file, content)) {
        for (const tokenName of tokenDefinitions(stripCssComments(block))) {
          runtimeDefinitions.add(tokenName);
        }
      }
    }
    for (const file of siteFiles) {
      for (const block of cssByFile.get(file) ?? []) {
        scanCssForRuntimeTokenViolations(file, block, allDefinitions, runtimeDefinitions, violations);
      }
    }
  }

  return violations.sort((left, right) => left.file.localeCompare(right.file) || left.line - right.line || left.kind.localeCompare(right.kind));
}

if (import.meta.main) {
  const violations = await checkPrimerAlignment();

  if (violations.length > 0) {
    for (const violation of violations) {
      console.error(`${violation.file}:${violation.line}: ${violation.kind}: ${violation.message}`);
    }
    process.exit(1);
  }
}
