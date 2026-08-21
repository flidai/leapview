import {
  getAllHttpServices,
  type HttpService,
} from "@typespec/http";
import type { Program } from "@typespec/compiler";

/** The resolved HTTP graph and compiler diagnostics discovered for an emitter run. */
export interface HttpServiceDiscovery {
  services: HttpService[];
  diagnostics: ReturnType<typeof getAllHttpServices>[1];
}

export function discoverHttpServices(program: Program): HttpServiceDiscovery {
  const [services, diagnostics] = getAllHttpServices(program);
  return { services, diagnostics };
}
