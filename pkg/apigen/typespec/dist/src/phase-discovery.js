import { getAllHttpServices, } from "@typespec/http";
export function discoverHttpServices(program) {
    const [services, diagnostics] = getAllHttpServices(program);
    return { services, diagnostics };
}
