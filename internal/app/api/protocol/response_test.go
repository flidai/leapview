package protocol

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseBufferStreamsWithoutReplayAndForwardsFinalTrailer(t *testing.T) {
	for _, mediaType := range []string{"application/vnd.apache.arrow.stream", "text/event-stream"} {
		t.Run(mediaType, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			response := NewResponseBuffer(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))
			response.Header().Set("Content-Type", mediaType)
			response.Header().Set("Trailer", "X-Next-Cursor")
			_, _ = response.Write([]byte("first"))
			response.Flush()
			if !recorder.Flushed || recorder.Body.String() != "first" {
				t.Fatal("first batch was not delivered before the response completed")
			}
			_, _ = response.Write([]byte("second"))
			response.Flush()
			response.Header().Set("X-Next-Cursor", "next-page")
			response.Header().Set("Trailer", "X-Next-Cursor, X-Too-Late")
			response.Header().Set("X-Too-Late", "not-declared-at-commit")
			response.Flush()
			response.Flush()
			if got := recorder.Body.String(); got != "firstsecond" {
				t.Fatalf("stream bytes were replayed: %q", got)
			}
			result := recorder.Result()
			defer result.Body.Close()
			if got := result.Trailer.Get("X-Next-Cursor"); got != "next-page" {
				t.Fatalf("final trailer = %q", got)
			}
			if recorder.Header().Get("X-Too-Late") != "" {
				t.Fatal("trailer declaration changed after header commit")
			}
		})
	}
}

func TestResponseBufferStillBuffersJSONAndHonorsETag(t *testing.T) {
	write := func(etag string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/example", nil)
		request.Header.Set("If-None-Match", etag)
		response := NewResponseBuffer(recorder, request)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"value":1}`))
		if recorder.Body.Len() != 0 {
			t.Fatal("JSON response escaped before normalization")
		}
		response.Flush()
		return recorder
	}
	first := write("")
	if first.Code != http.StatusOK || first.Body.String() != `{"value":1}` || first.Header().Get("ETag") == "" {
		t.Fatalf("JSON response = %d %q headers=%v", first.Code, first.Body.String(), first.Header())
	}
	matched := write(first.Header().Get("ETag"))
	if matched.Code != http.StatusNotModified || matched.Body.Len() != 0 {
		t.Fatalf("conditional JSON response = %d %q", matched.Code, matched.Body.String())
	}
}
