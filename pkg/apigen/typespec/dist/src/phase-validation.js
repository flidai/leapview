import { reportDiagnostic } from "./lib.js";
export function hasErrorDiagnostics(diagnostics) {
    return diagnostics.some((diagnostic) => diagnostic.severity === "error");
}
export function validateOutputFile(program, outputFile) {
    if (outputFile) {
        return true;
    }
    reportDiagnostic(program, { code: "missing-output-file", target: program.getGlobalNamespaceType() });
    return false;
}
export function validateServiceCount(program, count) {
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
export function validateServicePresence(program, count) {
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
