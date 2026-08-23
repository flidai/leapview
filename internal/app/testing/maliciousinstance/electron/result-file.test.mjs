import assert from "node:assert/strict";
import {
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { writeJSONAtomic } from "./result-file.mjs";

test("atomic result writes publish complete private JSON", async () => {
  const directory = await mkdtemp(join(tmpdir(), "leapview-electron-result-"));
  try {
    const path = join(directory, "electron-proof.json");
    const result = { passed: true, phase: "complete", observations: [] };

    await writeJSONAtomic(path, result);

    assert.deepEqual(JSON.parse(await readFile(path, "utf8")), result);
    if (process.platform !== "win32") {
      assert.equal((await stat(path)).mode & 0o777, 0o600);
    }
    assert.deepEqual(await readdir(directory), ["electron-proof.json"]);
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});

test("failed and concurrent replacements never corrupt the published result", async () => {
  const directory = await mkdtemp(join(tmpdir(), "leapview-electron-result-"));
  try {
    const path = join(directory, "electron-proof.json");
    const bootstrap = { passed: false, phase: "bootstrap" };
    await writeJSONAtomic(path, bootstrap);

    await assert.rejects(
      writeJSONAtomic(path, { invalid: 1n }),
      /BigInt/,
    );
    assert.deepEqual(JSON.parse(await readFile(path, "utf8")), bootstrap);

    const orphan = `${path}.interrupted.tmp`;
    await writeFile(orphan, '{"passed":', { mode: 0o600 });
    assert.deepEqual(JSON.parse(await readFile(path, "utf8")), bootstrap);

    const replacements = Array.from({ length: 12 }, (_, index) => ({
      passed: true,
      phase: "complete",
      sequence: index,
      padding: String(index).repeat(64 * 1024),
    }));
    let writing = true;
    const reader = (async () => {
      while (writing) {
        const observed = JSON.parse(await readFile(path, "utf8"));
        assert.ok(
          observed.phase === "bootstrap" ||
            replacements.some((candidate) => candidate.sequence === observed.sequence),
        );
      }
    })();
    try {
      await Promise.all(replacements.map((value) => writeJSONAtomic(path, value)));
    } finally {
      writing = false;
    }
    await reader;

    const final = JSON.parse(await readFile(path, "utf8"));
    assert.ok(replacements.some((candidate) => candidate.sequence === final.sequence));
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});
