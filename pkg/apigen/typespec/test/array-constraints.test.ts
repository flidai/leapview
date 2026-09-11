import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { describe, expect, it } from "vitest";

const execFileAsync = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tsp = join(root, "node_modules", "@typespec", "compiler", "cmd", "tsp.js");

describe("APIGen TypeSpec array constraints", () => {
  it("emits minimum and maximum array item constraints", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Array constraints" })
      namespace ArrayConstraints;

      @minItems(1)
      @maxItems(4)
      model Tags is string[];

      model Payload {
        @minItems(2)
        @maxItems(5)
        @apigen.uniqueItems
        values: string[];
        tags: Tags;
      }

      @route("/payload") @post
      op create(@body body: Payload): Payload;
    `);

    expect(doc.schemas.Payload.properties.values.schema).toEqual({
      type: "array",
      items: { type: "string" },
      min_items: 2,
      max_items: 5,
      unique_items: true,
    });
    expect(doc.schemas.Payload.properties.tags.schema).toEqual({
      type: "array",
      items: { type: "string" },
      min_items: 1,
      max_items: 4,
    });
  });

  it("rejects uniqueItems on non-array properties", async () => {
    await expectCompileFails(`
      using Http;

      @service(#{ title: "Invalid unique items" })
      namespace InvalidUniqueItems;

      model Payload {
        @apigen.uniqueItems
        value: string;
      }

      @route("/payload") @post
      op create(@body body: Payload): Payload;
    `);
  });
});

async function compileSource(source: string, outputFile?: string) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const fixtureDir = join(outDir, "source");
  const irPath = outputFile ?? join(outDir, "json-ir.json");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(join(fixtureDir, "main.tsp"), source);
  await compileDirectory(fixtureDir, irPath);
  return JSON.parse(await readFile(irPath, "utf8"));
}

async function expectCompileFails(source: string) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const irPath = join(outDir, "json-ir.json");
  await expect(compileSource(source, irPath)).rejects.toThrow();
  await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
}

async function compileDirectory(sourceDir: string, outputFile: string) {
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
    ],
    { cwd: root },
  );
}
