import { type Program, type Type } from "@typespec/compiler";
export declare function withSchemaConstraints<T extends object>(program: Program, target: Type, schema: T): T;
