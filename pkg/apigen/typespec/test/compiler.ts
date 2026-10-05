import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { expect } from "vitest";

const execFileAsync = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tsp = join(root, "node_modules", "@typespec", "compiler", "cmd", "tsp.js");

export async function compileFixture(name: string, outputFile: string) {
  await compileDirectory(join(root, "test", "fixtures", name), outputFile);
}

export async function compileSource(source: string, outputFile?: string, strictOperationKinds = false) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const fixtureDir = join(outDir, "source");
  const irPath = outputFile ?? join(outDir, "json-ir.json");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(join(fixtureDir, "main.tsp"), source);
  await compileDirectory(fixtureDir, irPath, strictOperationKinds);
  return JSON.parse(await readFile(irPath, "utf8"));
}

export async function expectCompileFails(source: string, message: string, strictOperationKinds = false) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const irPath = join(outDir, "json-ir.json");
  await expect(compileSource(source, irPath, strictOperationKinds)).rejects.toSatisfy((error: any) =>
    `${error.stdout}\n${error.stderr}`.includes(message),
  );
  await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
}

async function compileDirectory(sourceDir: string, outputFile: string, strictOperationKinds = false) {
  const strictOptions = strictOperationKinds
    ? ["--option", "@yacobolo/apigen.require-explicit-operation-kind=true"]
    : [];
  await execFileAsync(
    process.execPath,
    [
      tsp,
      "compile",
      sourceDir,
      "--import",
      root,
      "--emit",
      root,
      "--option",
      `@yacobolo/apigen.output-file=${outputFile}`,
      "--option",
      "@yacobolo/apigen.base-path=/",
      ...strictOptions,
    ],
    { cwd: root },
  );
}
