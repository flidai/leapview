import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { describe, expect, it } from "vitest";

const execFileAsync = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tsp = join(root, "node_modules", "@typespec", "compiler", "cmd", "tsp.js");

describe("APIGen TypeSpec exact number decoding", () => {
  it("emits an explicit union opt-in without changing unmarked unions", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Exact numbers" })
      namespace ExactNumbers;

      @apigen.exactNumbers
      union ExactValues {
        text: string[],
        number: float64[],
      }

      union DefaultValues {
        text: string[],
        number: float64[],
      }

      model Payload {
        exact: ExactValues;
        defaults: DefaultValues;
      }

      @route("/payload") @post
      op create(@body body: Payload): Payload;
    `);

    expect(doc.schemas.ExactValues.exact_numbers).toBe(true);
    expect(doc.schemas.DefaultValues.exact_numbers).toBeUndefined();
    expect(doc.schemas.ExactValues.extensions).toBeUndefined();
  });
});

async function compileSource(source: string) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-exact-numbers-"));
  const fixtureDir = join(outDir, "source");
  const irPath = join(outDir, "json-ir.json");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(join(fixtureDir, "main.tsp"), source);
  await execFileAsync(
    process.execPath,
    [
      tsp,
      "compile",
      fixtureDir,
      "--import",
      root,
      "--emit",
      root,
      "--option",
      `@yacobolo/apigen.output-file=${irPath}`,
      "--option",
      "@yacobolo/apigen.base-path=/",
    ],
    { cwd: root },
  );
  return JSON.parse(await readFile(irPath, "utf8"));
}
