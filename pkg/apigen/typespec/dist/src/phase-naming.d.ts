import type { Namespace, Program } from "@typespec/compiler";
/** Returns the fully-qualified namespace used by generated IR names. */
export declare function qualifiedNamespaceName(namespace: Namespace | undefined): string | undefined;
/** Resolves package metadata once at the discovery boundary with stable defaults. */
export declare function readPackageMetadata(program: Program): {
    title: string;
    version: string;
    description?: string;
    namespace?: string;
};
