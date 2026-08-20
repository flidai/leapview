import type { Diagnostic, Program } from "@typespec/compiler";
export declare function hasErrorDiagnostics(diagnostics: readonly Diagnostic[]): boolean;
export declare function validateOutputFile(program: Program, outputFile: string | undefined): outputFile is string;
export declare function validateServiceCount(program: Program, count: number): boolean;
/** Reports the legacy zero-service diagnostic used when no contract roots exist. */
export declare function validateServicePresence(program: Program, count: number): boolean;
