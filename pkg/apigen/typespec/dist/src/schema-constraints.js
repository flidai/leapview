import { getMaxItems, getMaxLength, getMaxValue, getMinItems, getMinLength, getMinValue, getPattern, } from "@typespec/compiler";
import { normalizeDocument } from "./phase-normalization.js";
export function withSchemaConstraints(program, target, schema) {
    const candidates = schemaConstraintCandidates(target);
    const minimum = firstSchemaConstraint(candidates, (candidate) => getMinValue(program, candidate));
    const maximum = firstSchemaConstraint(candidates, (candidate) => getMaxValue(program, candidate));
    const minLength = firstSchemaConstraint(candidates, (candidate) => getMinLength(program, candidate));
    const maxLength = firstSchemaConstraint(candidates, (candidate) => getMaxLength(program, candidate));
    const minItems = firstSchemaConstraint(candidates, (candidate) => getMinItems(program, candidate));
    const maxItems = firstSchemaConstraint(candidates, (candidate) => getMaxItems(program, candidate));
    const pattern = firstSchemaConstraint(candidates, (candidate) => getPattern(program, candidate));
    return normalizeDocument({
        ...schema,
        minimum,
        maximum,
        min_length: minLength,
        max_length: maxLength,
        min_items: minItems,
        max_items: maxItems,
        pattern,
    });
}
function schemaConstraintCandidates(target) {
    const candidates = [target];
    let current = target.kind === "ModelProperty" ? target.type : target;
    if (current !== target) {
        candidates.push(current);
    }
    while (current?.kind === "Scalar" && current.baseScalar) {
        current = current.baseScalar;
        candidates.push(current);
    }
    return candidates;
}
function firstSchemaConstraint(candidates, read) {
    for (const candidate of candidates) {
        const value = read(candidate);
        if (value !== undefined) {
            return value;
        }
    }
    return undefined;
}
