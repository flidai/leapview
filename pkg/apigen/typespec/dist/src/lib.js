import { createTypeSpecLibrary, paramMessage } from "@typespec/compiler";
export const $lib = createTypeSpecLibrary({
    name: "@yacobolo/apigen",
    diagnostics: {
        "unsupported-type": {
            severity: "error",
            messages: {
                default: paramMessage `Unsupported TypeSpec type '${"kind"}' in ${"context"}.`,
            },
        },
        "unsupported-response-status": {
            severity: "error",
            messages: {
                default: paramMessage `Unsupported response status '${"status"}' for operation '${"operation"}'. APIGen IR v4 requires concrete numeric status codes.`,
            },
        },
        "unsupported-response-content": {
            severity: "error",
            messages: {
                default: paramMessage `Unsupported response content for operation '${"operation"}': incompatible response content for status ${"status"} and content type ${"contentType"}. APIGen IR v4 requires one concrete content entry per status/content type.`,
            },
        },
        "unsupported-cookie": {
            severity: "error",
            messages: {
                default: "cookie parameters are not supported by APIGen generated server, OpenAPI, and CLI outputs.",
            },
        },
        "unsupported-shared-route": {
            severity: "error",
            messages: {
                default: paramMessage `Unsupported shared route: ${"reason"}.`,
            },
        },
        "unsupported-auth": {
            severity: "error",
            messages: {
                default: paramMessage `Unsupported authentication shape in ${"context"}. ${"reason"}`,
            },
        },
        "multiple-services": {
            severity: "error",
            messages: {
                default: paramMessage `APIGen TypeSpec emitter requires exactly one TypeSpec service, found ${"count"}.`,
            },
        },
        "reserved-extension": {
            severity: "error",
            messages: {
                default: paramMessage `Extension '${"key"}' is reserved for APIGen-owned metadata. Use the matching APIGen decorator instead.`,
            },
        },
        "invalid-extension-key": {
            severity: "error",
            messages: {
                default: paramMessage `Extension '${"key"}' must start with 'x-'.`,
            },
        },
        "invalid-extension-value": {
            severity: "error",
            messages: {
                default: paramMessage `Extension '${"key"}' contains a non-JSON-compatible value at ${"path"}.`,
            },
        },
        "invalid-command": {
            severity: "error",
            messages: {
                default: paramMessage `Invalid command contract: ${"reason"}.`,
            },
        },
        "invalid-operation-kind": {
            severity: "error",
            messages: {
                default: paramMessage `Invalid operation kind: ${"reason"}.`,
            },
        },
        "invalid-property-names": {
            severity: "error",
            messages: {
                default: paramMessage `Invalid @apigen.propertyNames usage: ${"reason"}.`,
            },
        },
        "invalid-min-properties": {
            severity: "error",
            messages: {
                default: paramMessage `Invalid @apigen.minProperties usage: ${"reason"}.`,
            },
        },
        "unnamed-schema": {
            severity: "error",
            messages: {
                default: paramMessage `APIGen IR v4 requires ${"context"} to resolve to a named model schema.`,
            },
        },
        "missing-output-file": {
            severity: "error",
            messages: {
                default: "The APIGen emitter option 'output-file' is required.",
            },
        },
    },
    emitter: {
        options: {
            type: "object",
            additionalProperties: false,
            properties: {
                "output-file": {
                    type: "string",
                    format: "absolute-path",
                    nullable: true,
                },
                "base-path": {
                    type: "string",
                    nullable: true,
                },
                "require-explicit-operation-kind": {
                    type: "boolean",
                    nullable: true,
                },
            },
            required: [],
        },
    },
});
export const { reportDiagnostic } = $lib;
