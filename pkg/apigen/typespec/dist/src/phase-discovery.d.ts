import { getAllHttpServices, type HttpService } from "@typespec/http";
import type { Program } from "@typespec/compiler";
/** The resolved HTTP graph and compiler diagnostics discovered for an emitter run. */
export interface HttpServiceDiscovery {
    services: HttpService[];
    diagnostics: ReturnType<typeof getAllHttpServices>[1];
}
export declare function discoverHttpServices(program: Program): HttpServiceDiscovery;
