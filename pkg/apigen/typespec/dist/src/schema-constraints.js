import { getMaxItems, getMaxLength, getMaxValue, getMinItems, getMinLength, getMinValue, getPattern, } from "@typespec/compiler";
import { normalizeDocument } from "./phase-normalization.js";
export function withSchemaConstraints(program, target, schema) {
    const candidates = schemaConstraintCandidates(target);
    return normalizeDocument({
        ...schema,
        minimum: firstSchemaConstraint(candidates, (candidate) => getMinValue(program, candidate)),
        maximum: firstSchemaConstraint(candidates, (candidate) => getMaxValue(program, candidate)),
        min_length: firstSchemaConstraint(candidates, (candidate) => getMinLength(program, candidate)),
        max_length: firstSchemaConstraint(candidates, (candidate) => getMaxLength(program, candidate)),
        min_items: firstSchemaConstraint(candidates, (candidate) => getMinItems(program, candidate)),
        max_items: firstSchemaConstraint(candidates, (candidate) => getMaxItems(program, candidate)),
        pattern: firstSchemaConstraint(candidates, (candidate) => getPattern(program, candidate)),
    });
}
function schemaConstraintCandidates(target) {
    const candidates = [target];
    let current = target.kind === "ModelProperty" ? target.type : target;
    if (current !== target)
        candidates.push(current);
    while (current?.kind === "Scalar" && current.baseScalar) {
        current = current.baseScalar;
        candidates.push(current);
    }
    return candidates;
}
function firstSchemaConstraint(candidates, read) {
    for (const candidate of candidates) {
        const value = read(candidate);
        if (value !== undefined)
            return value;
    }
    return undefined;
}
