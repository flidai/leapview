import { getPackages } from "./decorators.js";
/** Returns the fully-qualified namespace used by generated IR names. */
export function qualifiedNamespaceName(namespace) {
    const parts = [];
    let current = namespace;
    while (current) {
        if (current.name)
            parts.unshift(current.name);
        current = current.namespace;
    }
    return parts.length > 0 ? parts.join(".") : undefined;
}
/** Resolves package metadata once at the discovery boundary with stable defaults. */
export function readPackageMetadata(program) {
    const packages = getPackages({ program });
    if (packages.length > 0) {
        const [namespace, pkg] = packages[0];
        return {
            title: pkg.title,
            version: pkg.version,
            description: pkg.description,
            namespace: qualifiedNamespaceName(namespace),
        };
    }
    return { title: "Data Contracts", version: "0.1.0" };
}
