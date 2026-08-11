using Http;

@service(#{ title: "Invalid API" })
namespace InvalidAPI;

@route("/things")
@post
op createThing(@body body: { name: string }): string;
