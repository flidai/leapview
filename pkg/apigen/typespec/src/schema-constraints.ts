import {
  getMaxItems,
  getMaxLength,
  getMaxValue,
  getMinItems,
  getMinLength,
  getMinValue,
  getPattern,
  type Program,
  type Type,
} from "@typespec/compiler";
import { normalizeDocument } from "./phase-normalization.js";

export interface SchemaConstraintFields {
  minimum?: number;
  maximum?: number;
  min_length?: number;
  max_length?: number;
  min_items?: number;
  max_items?: number;
  pattern?: string;
}

export function withSchemaConstraints<T extends object>(
  program: Program,
  target: Type,
  schema: T,
): T & SchemaConstraintFields {
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
  }) as T & SchemaConstraintFields;
}

function schemaConstraintCandidates(target: Type): Type[] {
  const candidates: Type[] = [target];
  let current: Type | undefined = target.kind === "ModelProperty" ? target.type : target;
  if (current !== target) {
    candidates.push(current);
  }
  while (current?.kind === "Scalar" && current.baseScalar) {
    current = current.baseScalar;
    candidates.push(current);
  }
  return candidates;
}

function firstSchemaConstraint<T>(candidates: Type[], read: (candidate: Type) => T | undefined): T | undefined {
  for (const candidate of candidates) {
    const value = read(candidate);
    if (value !== undefined) {
      return value;
    }
  }
  return undefined;
}
