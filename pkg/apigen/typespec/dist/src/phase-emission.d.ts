import { type Program } from "@typespec/compiler";
/** Writes an already normalized document through the TypeSpec emitter API. */
export declare function emitDocumentFile(program: Program, outputFile: string, document: unknown): Promise<void>;
