package client

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProblemErrorPreservesStructuredProblem(t *testing.T) {
	problem := ProblemDetails{Status: 404, Code: "WIDGET_NOT_FOUND", Detail: "Widget not found.", RequestID: "request-1"}
	err := &ProblemError{OperationID: "deleteWidget", Response: Response{StatusCode: http.StatusNotFound}, Problem: problem}
	if err.Error() != "Widget not found." {
		t.Fatalf("error = %q", err.Error())
	}
	var matched *ProblemError
	if !errors.As(err, &matched) || matched.Problem.Code != problem.Code || matched.Response.StatusCode != http.StatusNotFound {
		t.Fatalf("problem error = %#v", matched)
	}
}

func TestAddQueryHandlesOptionalAndCollectionValues(t *testing.T) {
	t.Helper()

	query := url.Values{}
	var absent *string
	value := "present"
	AddQuery(query, "absent", absent, true)
	AddQuery(query, "value", &value, true)
	AddQuery(query, "repeated", []int32{1, 2}, true)
	AddQuery(query, "joined", []string{"a", "b"}, false)

	require.Equal(t, url.Values{
		"value":    {"present"},
		"repeated": {"1", "2"},
		"joined":   {"a,b"},
	}, query)
}

func TestAddHeaderHandlesOptionalAndCollectionValues(t *testing.T) {
	t.Helper()

	headers := http.Header{}
	var absent *string
	AddHeader(headers, "X-Absent", absent)
	AddHeader(headers, "X-Value", []string{"a", "b"})

	require.Empty(t, headers.Values("X-Absent"))
	require.Equal(t, []string{"a", "b"}, headers.Values("X-Value"))
}
