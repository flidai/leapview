using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

@route("/api/v1")
namespace LeapViewAPI;

model InstanceResponse {
  id: string;
  canonicalOrigin: string;
  environment: string;
}

alias PublicInstanceErrors = BadRequest | RateLimited | InternalServerError | ServiceUnavailable;

@tag("Instance")
@summary("Get this LeapView instance identity")
@route("/instance")
@get
@apigen.authz(#{ mode: "none" })
op getInstance(): OkJson<InstanceResponse> | PublicInstanceErrors;
