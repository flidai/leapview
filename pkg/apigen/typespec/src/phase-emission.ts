import { emitFile, type Program } from "@typespec/compiler";
import { renderDocumentJSON } from "./phase-rendering.js";

/** Writes an already normalized document through the TypeSpec emitter API. */
export async function emitDocumentFile(program: Program, outputFile: string, document: unknown): Promise<void> {
  await emitFile(program, { path: outputFile, content: renderDocumentJSON(document) });
}
