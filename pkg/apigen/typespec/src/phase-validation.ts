import type { Diagnostic, Program } from "@typespec/compiler";
import { reportDiagnostic } from "./lib.js";

export function hasErrorDiagnostics(diagnostics: readonly Diagnostic[]): boolean {
  return diagnostics.some((diagnostic) => diagnostic.severity === "error");
}

export function validateOutputFile(program: Program, outputFile: string | undefined): outputFile is string {
  if (outputFile) {
    return true;
  }
  reportDiagnostic(program, { code: "missing-output-file", target: program.getGlobalNamespaceType() });
  return false;
}

export function validateServiceCount(program: Program, count: number): boolean {
  if (count <= 1) {
    return true;
  }
  reportDiagnostic(program, {
    code: "multiple-services",
    format: { count: String(count) },
    target: program.getGlobalNamespaceType(),
  });
  return false;
}

/** Reports the legacy zero-service diagnostic used when no contract roots exist. */
export function validateServicePresence(program: Program, count: number): boolean {
  if (count > 0) {
    return true;
  }
  reportDiagnostic(program, {
    code: "multiple-services",
    format: { count: String(count) },
    target: program.getGlobalNamespaceType(),
  });
  return false;
}
