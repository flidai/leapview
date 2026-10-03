import type { HttpPayloadBody } from "@typespec/http";
type HeaderParameter = {
    name: string;
    in: string;
    required?: boolean;
};
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
export declare function commandTransportPolicies(input: IdempotencyPolicyInput): {
    idempotency: "required" | "forbidden" | undefined;
    concurrency: "if-match" | undefined;
};
export {};
