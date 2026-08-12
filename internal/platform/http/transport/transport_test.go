package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

type testCommandOperationID struct{ value string }

func (operation testCommandOperationID) APIGenOperationID() string { return operation.value }

func TestWriteJSONNormalizesTimestampsAndRequiredCollections(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteJSON(recorder, 200, map[string]any{
		"createdAt":               "2026-01-02 03:04:05",
		"activeServingStateSince": "2026-01-02T05:04:05+02:00",
		"items":                   nil,
		"bindings":                nil,
		"workspaces":              nil,
		"optional":                nil,
	})
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["createdAt"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("createdAt = %#v", body["createdAt"])
	}
	if body["activeServingStateSince"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("activeServingStateSince = %#v", body["activeServingStateSince"])
	}
	if items, ok := body["items"].([]any); !ok || items == nil {
		t.Fatalf("items = %#v, want empty array", body["items"])
	}
	for _, field := range []string{"bindings", "workspaces"} {
		if value, ok := body[field].([]any); !ok || value == nil {
			t.Fatalf("%s = %#v, want empty array", field, body[field])
		}
	}
	if body["optional"] != nil {
		t.Fatalf("optional = %#v, want null", body["optional"])
	}
}

func TestKeysetPagePreservesEmptyArray(t *testing.T) {
	items, next, err := KeysetPage([]string(nil), nil, nil, func(value string) string { return value })
	encoded, marshalErr := json.Marshal(items)
	if err != nil || marshalErr != nil || next != nil || string(encoded) != "[]" {
		t.Fatalf("empty page = %s, next=%v, error=%v/%v; want []", encoded, next, err, marshalErr)
	}
}

func TestKeysetPageRejectsCursorFromAnotherCollection(t *testing.T) {
	items := []string{"a", "b", "c"}
	_, token, err := KeysetPage(items, int32Pointer(1), nil, func(value string) string { return value })
	if err != nil || token == nil {
		t.Fatalf("first page token = %v, %v", token, err)
	}
	if _, _, err := KeysetPage([]string{"x", "y"}, nil, token, func(value string) string { return value }); err == nil {
		t.Fatal("expected unavailable cursor key to fail")
	}
}

func int32Pointer(value int32) *int32 { return &value }

func TestWriteAPIGenCommandFailureUsesGeneratedPublicContract(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/widgets/widget_1/finalize", nil)
	response := httptest.NewRecorder()
	operation := testCommandOperationID{value: "finalizeWidget"}
	lookup := func(operationID testCommandOperationID) ([]apigenfailure.Contract, bool) {
		if operationID.APIGenOperationID() != "finalizeWidget" {
			return nil, false
		}
		return []apigenfailure.Contract{{
			Kind: "conflict", StatusCode: http.StatusConflict,
			Code: "WIDGET_CONFLICT", PublicDetail: "The widget conflicts with its current state.",
		}}, true
	}

	WriteAPIGenCommandFailure(context.Background(), response, request, nil, operation, lookup, apigenfailure.New("conflict", "private storage detail"))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	var problem ProblemDetails
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "WIDGET_CONFLICT" || problem.Detail != "The widget conflicts with its current state." {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestWriteAPIGenCommandFailureHidesUnknownCause(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/widgets/widget_1/finalize", nil)
	response := httptest.NewRecorder()

	WriteAPIGenCommandFailure(context.Background(), response, request, nil, testCommandOperationID{value: "finalizeWidget"}, nil, errors.New("database password is secret"))

	var problem ProblemDetails
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || problem.Code != "INTERNAL_ERROR" || problem.Detail != "The request could not be completed." {
		t.Fatalf("problem = %#v", problem)
	}
}
