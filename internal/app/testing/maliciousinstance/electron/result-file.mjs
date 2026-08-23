import { randomBytes } from "node:crypto";
import { open, rename, unlink } from "node:fs/promises";
import { dirname } from "node:path";

export async function writeJSONAtomic(path, input) {
  const body = `${JSON.stringify(input, null, 2)}\n`;
  const temporaryPath = `${path}.${process.pid}.${randomBytes(8).toString("hex")}.tmp`;
  const handle = await open(temporaryPath, "wx", 0o600);
  try {
    try {
      await handle.writeFile(body, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporaryPath, path);
  } catch (error) {
    await unlink(temporaryPath).catch(() => undefined);
    throw error;
  }

  if (process.platform !== "win32") {
    const directory = await open(dirname(path), "r");
    try {
      await directory.sync();
    } finally {
      await directory.close();
    }
  }
}
