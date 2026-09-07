import { type Program, type Type } from "@typespec/compiler";
export interface SchemaConstraintFields {
    minimum?: number;
    maximum?: number;
    min_length?: number;
    max_length?: number;
    min_items?: number;
    max_items?: number;
    pattern?: string;
}
export declare function withSchemaConstraints<T extends object>(program: Program, target: Type, schema: T): T & SchemaConstraintFields;
