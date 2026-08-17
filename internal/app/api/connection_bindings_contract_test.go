package api_test

import "testing"

func TestTargetConnectionBindingAPIContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/targets/{target}/connection-bindings"
	item := base + "/{connection}"

	operations := []struct {
		path      string
		method    string
		id        string
		privilege string
	}{
		{base, "get", "listTargetConnectionBindings", "PROJECT_ADMIN"},
		{base, "post", "createTargetConnectionBinding", "PROJECT_ADMIN"},
		{item, "get", "getTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/plan", "post", "planTargetConnectionBindingChange", "RESOURCE_MANAGE"},
		{item, "put", "updateTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/test", "post", "testTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/refresh", "post", "refreshTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/enable", "post", "enableTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/disable", "post", "disableTargetConnectionBinding", "RESOURCE_MANAGE"},
		{item + "/health", "get", "getTargetConnectionBindingHealth", "RESOURCE_READ"},
	}
	for _, want := range operations {
		operation := openAPIOperation(t, paths, want.path, want.method)
		if operation["operationId"] != want.id {
			t.Fatalf("%s %s operation = %#v", want.method, want.path, operation)
		}
		if privilege := openAPIMap(t, operation, "x-authz")["privilege"]; privilege != want.privilege {
			t.Fatalf("%s privilege = %#v, want %q", want.id, privilege, want.privilege)
		}
	}

	for _, action := range []string{"test", "refresh", "enable", "disable"} {
		operation := openAPIOperation(t, paths, item+"/"+action, "post")
		if !operationHasParameter(operation, "header", "Idempotency-Key") {
			t.Fatalf("%s operation is missing Idempotency-Key", action)
		}
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	binding := openAPISchema(t, schemas, "TargetConnectionBindingResponse")
	for _, field := range []string{
		"id", "targetId", "logicalConnection", "connectorKind", "authenticationMode",
		"environment", "endpoint", "enabled", "health", "revision",
	} {
		_ = schemaProperty(t, binding, field)
	}
	for _, forbidden := range []string{"secretValue", "credentials", "password", "token"} {
		if _, exists := openAPIMap(t, binding, "properties")[forbidden]; exists {
			t.Fatalf("connection binding response exposes forbidden field %q", forbidden)
		}
	}

	health := openAPISchema(t, schemas, "TargetConnectionBindingHealthResponse")
	for _, field := range []string{
		"bindingId", "targetId", "logicalConnection", "connectorKind",
		"environment", "bindingRevision", "health", "hasActivePool",
	} {
		_ = schemaProperty(t, health, field)
	}
	for _, forbidden := range []string{"credential", "secret", "providerError", "rawError"} {
		if _, exists := openAPIMap(t, health, "properties")[forbidden]; exists {
			t.Fatalf("connection health response exposes forbidden field %q", forbidden)
		}
	}
}
