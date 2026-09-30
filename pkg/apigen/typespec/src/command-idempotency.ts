import type { HttpPayloadBody } from "@typespec/http";

type HeaderParameter = { name: string; in: string; required?: boolean };

interface IdempotencyPolicyInput {
  method: string;
  nonReplayable?: boolean;
  parameters: readonly HeaderParameter[];
  requestBody?: HttpPayloadBody;
  asyncExecution: boolean;
  auditRequired: boolean;
  auditGuarantee?: string;
  authzMode?: string;
  invalidCommand(reason: string): void;
}

export function commandTransportPolicies(input: IdempotencyPolicyInput): {
  idempotency: "required" | "forbidden" | undefined;
  concurrency: "if-match" | undefined;
} {
  const hasHeader = (name: string, required = false) => input.parameters.some(
    (parameter) => parameter.in === "header" && parameter.name.toLowerCase() === name.toLowerCase() &&
      (!required || parameter.required),
  );
  const nonReplayable = input.nonReplayable === true;
  const idempotency = nonReplayable ? "forbidden" : hasHeader("Idempotency-Key", true) ? "required" : undefined;

  if (nonReplayable && input.method !== "post") {
    input.invalidCommand("nonReplayable commands must use POST");
  }
  if (nonReplayable && hasHeader("Idempotency-Key")) {
    input.invalidCommand("nonReplayable commands cannot declare an Idempotency-Key header");
  }
  const requestBody = input.requestBody;
  if (nonReplayable && (!requestBody || requestBody.bodyKind !== "single" || requestBody.property?.optional ||
      requestBody.contentTypes.length === 0 || !requestBody.contentTypes.every(isJSONContentType))) {
    input.invalidCommand("nonReplayable commands require a required JSON request body");
  }
  if (nonReplayable && input.asyncExecution) {
    input.invalidCommand("nonReplayable commands cannot use async execution");
  }
  if (nonReplayable && (!input.auditRequired || input.auditGuarantee !== "transactional")) {
    input.invalidCommand("nonReplayable commands require required transactional audit");
  }
  if (nonReplayable && input.authzMode !== "authenticated" && input.authzMode !== "privilege") {
    input.invalidCommand("nonReplayable commands require authenticated or privilege authorization");
  }
  if (!nonReplayable && input.method === "post" && idempotency === undefined) {
    input.invalidCommand("POST commands require a required Idempotency-Key header");
  }
  const concurrency = hasHeader("If-Match", true) ? "if-match" : undefined;
  if (input.method === "patch" && concurrency === undefined) {
    input.invalidCommand("PATCH commands require a required If-Match header");
  }
  return { idempotency, concurrency };
}

function isJSONContentType(contentType: string): boolean {
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase();
  return mediaType === "application/json" || /^application\/[a-z0-9!#$&^_.+-]+\+json$/.test(mediaType);
}
