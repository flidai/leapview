package http

import (
	"bytes"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
)

func TestDashboardNativeArrowNegotiationIsExplicitAndVersioned(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		accept   string
		contract string
		want     dashboardArrowContractMode
		wantErr  bool
	}{
		{name: "json legacy", accept: "application/json", want: dashboardArrowContractLegacy},
		{name: "unversioned Arrow legacy", accept: dashboardArrowMediaType, want: dashboardArrowContractLegacy},
		{name: "unversioned rejected Arrow remains legacy", accept: dashboardArrowMediaType + ";q=0", want: dashboardArrowContractLegacy},
		{name: "missing marker legacy", accept: dashboardArrowMediaType + "; charset=binary", want: dashboardArrowContractLegacy},
		{name: "native-v1", accept: dashboardArrowMediaType, contract: dashboardNativeArrowContract, want: dashboardArrowContractNativeV1},
		{name: "native-v1 in accept list", accept: "application/json, " + dashboardArrowMediaType, contract: dashboardNativeArrowContract, want: dashboardArrowContractNativeV1},
		{name: "native-v1 with explicit quality", accept: "application/json;q=0.5, " + dashboardArrowMediaType + ";q=1", contract: dashboardNativeArrowContract, want: dashboardArrowContractNativeV1},
		{name: "native-v1 with later acceptable match", accept: dashboardArrowMediaType + ";q=0, " + dashboardArrowMediaType + ";q=0.25", contract: dashboardNativeArrowContract, want: dashboardArrowContractNativeV1},
		{name: "native-v1 explicitly rejected", accept: dashboardArrowMediaType + ";q=0", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 rejected in mixed accept list", accept: "application/json, " + dashboardArrowMediaType + ";q=0", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 invalid quality", accept: dashboardArrowMediaType + ";q=invalid", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 invalid quality syntax", accept: dashboardArrowMediaType + ";q=01", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 overprecise quality", accept: dashboardArrowMediaType + ";q=0.0001", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 out-of-range quality", accept: dashboardArrowMediaType + ";q=2", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "native-v1 wildcard remains unsupported", accept: "*/*", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "marker without Arrow accept", accept: "application/json", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "marker without accept", contract: dashboardNativeArrowContract, wantErr: true},
		{name: "unknown version", accept: dashboardArrowMediaType, contract: "native-v2", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := negotiateDashboardArrowContract(test.accept, test.contract)
			if test.wantErr {
				if !errors.Is(err, errDashboardArrowContractNotAcceptable) {
					t.Fatalf("negotiate error = %v, want not acceptable", err)
				}
				if got != dashboardArrowContractInvalid {
					t.Fatalf("failed negotiation mode = %v, want invalid", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("negotiate = (%v, %v), want (%v, nil)", got, err, test.want)
			}
		})
	}
}

func TestDashboardNativeArrowFoundationLeavesLegacyArrowUnchanged(t *testing.T) {
	t.Parallel()
	envelope := rowsetTestEnvelope(
		[]visualizationir.VisualizationField{{ID: "order_id", DataType: visualizationir.VisualizationDataTypeInteger}},
		[][]any{{int64(42)}},
		2,
	)
	rowset, err := dashboardVisualizationRowset(envelope, "a", 0, 1, "legacy-scope", "legacy-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/dashboards/sales/pages/main/visuals/orders/query", nil)
	request.Header.Set("Accept", dashboardArrowMediaType)
	recorder := httptest.NewRecorder()
	writeDashboardTableRowset(recorder, request, rowset, envelope)
	response := recorder.Result()
	defer response.Body.Close()
	if got := response.Header.Get(dashboardNativeArrowContractHeader); got != "" {
		t.Fatalf("legacy response claimed native contract %q", got)
	}
	if got := response.Header.Get("Trailer"); got != "" {
		t.Fatalf("legacy response declared native trailer %q", got)
	}
	if got := response.Header.Get(dashboardNativeArrowNextCursorHeader); !strings.HasPrefix(got, "d1.") {
		t.Fatalf("legacy cursor = %q, want initial d1 header", got)
	}
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	if !reader.Next() {
		t.Fatalf("read legacy record: %v", reader.Err())
	}
	values, ok := reader.Record().Column(0).(*array.String)
	if !ok || values.Value(0) != "42" {
		t.Fatalf("legacy projection = %T/%v, want string 42", reader.Record().Column(0), ok)
	}
}

func TestDashboardNativeArrowLimitPolicy(t *testing.T) {
	t.Parallel()
	value := func(value int) *int { return &value }
	tests := []struct {
		name    string
		input   *int
		want    int
		wantErr bool
	}{
		{name: "default", want: 100},
		{name: "minimum", input: value(1), want: 1},
		{name: "maximum", input: value(1000), want: 1000},
		{name: "zero", input: value(0), wantErr: true},
		{name: "negative", input: value(-1), wantErr: true},
		{name: "above maximum", input: value(1001), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeDashboardNativeArrowLimit(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatal("limit unexpectedly accepted")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("limit = (%d, %v), want (%d, nil)", got, err, test.want)
			}
		})
	}
}

func TestDashboardNativeArrowPagePlanUsesProbeWithinCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		offset             int
		limit              int
		wantEmit, wantRead int
		wantProbe          bool
	}{
		{name: "first page", offset: 0, limit: 100, wantEmit: 100, wantRead: 101, wantProbe: true},
		{name: "middle page", offset: 300, limit: 100, wantEmit: 100, wantRead: 101, wantProbe: true},
		{name: "bounded last page", offset: 9999, limit: 100, wantEmit: 1, wantRead: 1},
		{name: "cap exhausted", offset: 10000, limit: 100, wantEmit: 0, wantRead: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := planDashboardNativeArrowPage(test.offset, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			if plan.EmitLimit != test.wantEmit || plan.QueryLimit != test.wantRead || plan.ProbesContinuation != test.wantProbe {
				t.Fatalf("plan = %#v, want emit=%d query=%d probe=%v", plan, test.wantEmit, test.wantRead, test.wantProbe)
			}
		})
	}
	for _, offset := range []int{-1, dashboardNativeArrowRowCap + 1} {
		if _, err := planDashboardNativeArrowPage(offset, 100); err == nil {
			t.Fatalf("offset %d unexpectedly accepted", offset)
		}
	}
}

func TestDashboardNativeArrowPaginationBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		totalRows  int
		limit      int
		wantRows   int
		wantPages  int
		wantCursor bool
	}{
		{name: "empty", totalRows: 0, limit: 100, wantRows: 0, wantPages: 1},
		{name: "below limit", totalRows: 99, limit: 100, wantRows: 99, wantPages: 1},
		{name: "exact limit", totalRows: 100, limit: 100, wantRows: 100, wantPages: 1},
		{name: "limit plus one", totalRows: 101, limit: 100, wantRows: 101, wantPages: 2, wantCursor: true},
		{name: "9999", totalRows: 9999, limit: 100, wantRows: 9999, wantPages: 100, wantCursor: true},
		{name: "10000", totalRows: 10000, limit: 100, wantRows: 10000, wantPages: 100, wantCursor: true},
		{name: "10001 capped", totalRows: 10001, limit: 100, wantRows: 10000, wantPages: 100, wantCursor: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := dashboardNativeArrowTestScope(test.limit)
			offset := 0
			emitted := 0
			pages := 0
			firstCursor := false
			for {
				pages++
				plan, err := planDashboardNativeArrowPage(offset, test.limit)
				if err != nil {
					t.Fatal(err)
				}
				available := max(0, test.totalRows-offset)
				observed := min(available, plan.QueryLimit)
				emitted += min(observed, plan.EmitLimit)
				cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, observed, nil, time.Unix(1_700_000_000, 0))
				if err != nil {
					t.Fatal(err)
				}
				if pages == 1 {
					firstCursor = cursor != ""
				}
				if cursor == "" {
					break
				}
				state, err := decodeDashboardNativeArrowCursor(cursor, scope, time.Unix(1_700_000_001, 0))
				if err != nil {
					t.Fatal(err)
				}
				offset = state.NextOffset
			}
			if emitted != test.wantRows || pages != test.wantPages || firstCursor != test.wantCursor {
				t.Fatalf("result rows/pages/firstCursor = %d/%d/%v, want %d/%d/%v", emitted, pages, firstCursor, test.wantRows, test.wantPages, test.wantCursor)
			}
		})
	}
}

func TestDashboardNativeArrowCursorBindsGovernedRequestScope(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	scope := dashboardNativeArrowTestScope(100)
	plan, err := planDashboardNativeArrowPage(200, scope.RequestedLimit)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, 101, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cursor, "d3.") {
		t.Fatalf("cursor = %q, want d3 domain", cursor)
	}
	state, err := decodeDashboardNativeArrowCursor(cursor, scope, now.Add(time.Second))
	if err != nil || state.NextOffset != 300 || state.RowsConsumed != 300 || state.RowCap != dashboardNativeArrowRowCap {
		t.Fatalf("decode = (%#v, %v)", state, err)
	}

	mutations := []struct {
		name   string
		mutate func(*dashboardNativeArrowCursorScope)
	}{
		{name: "dashboard", mutate: func(value *dashboardNativeArrowCursorScope) { value.DashboardID = "inventory" }},
		{name: "page", mutate: func(value *dashboardNativeArrowCursorScope) { value.PageID = "summary" }},
		{name: "visual", mutate: func(value *dashboardNativeArrowCursorScope) { value.VisualID = "returns" }},
		{name: "filters", mutate: func(value *dashboardNativeArrowCursorScope) {
			value.NormalizedFiltersDigest = dashboardNativeArrowTestDigest('a')
		}},
		{name: "selections", mutate: func(value *dashboardNativeArrowCursorScope) {
			value.NormalizedSelectionsDigest = dashboardNativeArrowTestDigest('b')
		}},
		{name: "sorting", mutate: func(value *dashboardNativeArrowCursorScope) {
			value.EffectiveSortingDigest = dashboardNativeArrowTestDigest('c')
		}},
		{name: "limit", mutate: func(value *dashboardNativeArrowCursorScope) { value.RequestedLimit = 101 }},
		{name: "policy", mutate: func(value *dashboardNativeArrowCursorScope) {
			value.EffectivePolicyIdentity = dashboardNativeArrowTestDigest('d')
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := scope
			test.mutate(&changed)
			if _, err := decodeDashboardNativeArrowCursor(cursor, changed, now.Add(time.Second)); !errors.Is(err, errDashboardNativeArrowCursorInvalid) {
				t.Fatalf("changed scope error = %v, want invalid cursor", err)
			}
		})
	}

	changedSnapshot := scope
	changedSnapshot.ServingSnapshot = "snapshot-next"
	if _, err := decodeDashboardNativeArrowCursor(cursor, changedSnapshot, now.Add(time.Second)); !errors.Is(err, errDashboardCursorSnapshot) {
		t.Fatalf("changed snapshot error = %v, want conflict", err)
	}
	if _, err := decodeDashboardNativeArrowCursor(cursor, scope, now.Add(dashboardCursorLifetime)); !errors.Is(err, errDashboardNativeArrowCursorInvalid) {
		t.Fatalf("expired cursor error = %v", err)
	}
}

func TestDashboardNativeArrowCursorDomainsAreIsolated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	scope := dashboardNativeArrowTestScope(100)
	legacy := encodeIndexCursor(100, "legacy", scope.ServingSnapshot)
	semantic := cursorsigning.Sign("q1", []byte(`{"offset":100}`))
	for _, cursor := range []string{"malformed", legacy, semantic} {
		if _, err := decodeDashboardNativeArrowCursor(cursor, scope, now); !errors.Is(err, errDashboardNativeArrowCursorInvalid) {
			t.Fatalf("native decoder accepted %q: %v", cursor, err)
		}
	}

	plan, _ := planDashboardNativeArrowPage(0, scope.RequestedLimit)
	native, err := dashboardNativeArrowCompletionCursor(scope, plan, 101, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeIndexCursor(native, "legacy", scope.ServingSnapshot); err == nil {
		t.Fatal("legacy decoder accepted d3 cursor")
	}

	payload, _ := json.Marshal(dashboardNativeArrowCursor{
		Contract:        dashboardNativeArrowContract,
		Scope:           "invalid",
		ServingSnapshot: scope.ServingSnapshot,
		RequestedLimit:  scope.RequestedLimit,
		NextOffset:      100,
		RowsConsumed:    100,
		RowCap:          dashboardNativeArrowRowCap,
		Expires:         now.Add(dashboardCursorLifetime).Unix(),
	})
	wrongContract := cursorsigning.Sign("d3", bytes.Replace(payload, []byte(dashboardNativeArrowContract), []byte("native-v2"), 1))
	if _, err := decodeDashboardNativeArrowCursor(wrongContract, scope, now); !errors.Is(err, errDashboardNativeArrowCursorInvalid) {
		t.Fatalf("wrong contract error = %v", err)
	}
	var decoded dashboardNativeArrowCursor
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Scope, _ = scope.digest()
	decoded.RowCap++
	wrongCapPayload, _ := json.Marshal(decoded)
	wrongCap := cursorsigning.Sign("d3", wrongCapPayload)
	if _, err := decodeDashboardNativeArrowCursor(wrongCap, scope, now); !errors.Is(err, errDashboardNativeArrowCursorInvalid) {
		t.Fatalf("wrong cap error = %v", err)
	}
}

func TestDashboardNativeArrowCursorPayloadDoesNotExposeGovernanceInputs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	scope := dashboardNativeArrowTestScope(100)
	plan, _ := planDashboardNativeArrowPage(0, scope.RequestedLimit)
	cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, 101, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cursorsigning.Verify("d3", cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{
		scope.DashboardID,
		scope.PageID,
		scope.VisualID,
		scope.NormalizedFiltersDigest,
		scope.NormalizedSelectionsDigest,
		scope.EffectiveSortingDigest,
		scope.EffectivePolicyIdentity,
	} {
		if bytes.Contains(payload, []byte(sensitive)) {
			t.Fatalf("cursor payload leaked scope value %q: %s", sensitive, payload)
		}
	}
}

func TestDashboardNativeArrowCursorRequiresCanonicalGovernanceDigests(t *testing.T) {
	scope := dashboardNativeArrowTestScope(100)
	plan, _ := planDashboardNativeArrowPage(0, scope.RequestedLimit)
	for _, mutate := range []func(*dashboardNativeArrowCursorScope){
		func(value *dashboardNativeArrowCursorScope) { value.NormalizedFiltersDigest = "raw-filter-state" },
		func(value *dashboardNativeArrowCursorScope) { value.NormalizedSelectionsDigest = "" },
		func(value *dashboardNativeArrowCursorScope) { value.EffectiveSortingDigest = "sha256:ABC" },
		func(value *dashboardNativeArrowCursorScope) { value.EffectivePolicyIdentity = "principal:alice" },
	} {
		invalid := scope
		mutate(&invalid)
		if _, err := dashboardNativeArrowCompletionCursor(invalid, plan, 101, nil, time.Unix(1_700_000_000, 0)); err == nil {
			t.Fatal("non-canonical scope unexpectedly encoded")
		}
	}
}

func TestDashboardNativeArrowCursorPublishedOnlyAfterSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	scope := dashboardNativeArrowTestScope(2)
	plan, _ := planDashboardNativeArrowPage(20, 2)
	tests := []struct {
		name string
		err  error
	}{
		{name: "cancellation", err: errors.New("request cancelled")},
		{name: "timeout", err: errors.New("stream timeout")},
		{name: "budget", err: errors.New("result budget exceeded")},
		{name: "partial write", err: errors.New("short write")},
		{name: "IPC close", err: errors.New("close IPC")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, 3, test.err, time.Unix(1_700_000_000, 0))
			if !errors.Is(err, test.err) || cursor != "" {
				t.Fatalf("completion = (%q, %v), want no cursor and %v", cursor, err, test.err)
			}
			recorder := httptest.NewRecorder()
			if err := declareDashboardNativeArrowCursorTrailer(recorder); err != nil {
				t.Fatal(err)
			}
			recorder.WriteHeader(stdhttp.StatusOK)
			if err := publishDashboardNativeArrowCursor(recorder, cursor); err != nil {
				t.Fatal(err)
			}
			response := recorder.Result()
			defer response.Body.Close()
			if got := response.Trailer.Get(dashboardNativeArrowNextCursorHeader); got != "" {
				t.Fatalf("failed stream cursor = %q", got)
			}
		})
	}

	cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, 3, nil, time.Unix(1_700_000_000, 0))
	if err != nil || cursor == "" {
		t.Fatalf("successful completion = (%q, %v)", cursor, err)
	}
	recorder := httptest.NewRecorder()
	if err := declareDashboardNativeArrowCursorTrailer(recorder); err != nil {
		t.Fatal(err)
	}
	recorder.WriteHeader(stdhttp.StatusOK)
	if err := publishDashboardNativeArrowCursor(recorder, cursor); err != nil {
		t.Fatal(err)
	}
	response := recorder.Result()
	defer response.Body.Close()
	if got := response.Trailer.Get(dashboardNativeArrowNextCursorHeader); got != cursor {
		t.Fatalf("success trailer = %q, want %q", got, cursor)
	}
}

func TestDashboardNativeArrowCursorTrailerPublicationIsDefensive(t *testing.T) {
	t.Parallel()
	const cursor = "d3.next"

	t.Run("declaration before commitment publishes trailer", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		if err := declareDashboardNativeArrowCursorTrailer(recorder); err != nil {
			t.Fatal(err)
		}
		if got := recorder.Header().Get("Trailer"); got != dashboardNativeArrowNextCursorHeader {
			t.Fatalf("trailer declaration = %q", got)
		}
		recorder.WriteHeader(stdhttp.StatusOK)
		if err := publishDashboardNativeArrowCursor(recorder, cursor); err != nil {
			t.Fatal(err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if got := response.Header.Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("cursor leaked as ordinary header %q", got)
		}
		if got := response.Trailer.Get(dashboardNativeArrowNextCursorHeader); got != cursor {
			t.Fatalf("cursor trailer = %q, want %q", got, cursor)
		}
	})

	t.Run("missing declaration before commitment fails safely", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		if err := publishDashboardNativeArrowCursor(recorder, cursor); !errors.Is(err, errDashboardNativeArrowCursorTrailerUndeclared) {
			t.Fatalf("publish error = %v, want undeclared trailer", err)
		}
		if got := recorder.Header().Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("cursor leaked as ordinary header %q", got)
		}
	})

	t.Run("missing declaration after commitment fails safely", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		recorder.WriteHeader(stdhttp.StatusOK)
		if err := publishDashboardNativeArrowCursor(recorder, cursor); !errors.Is(err, errDashboardNativeArrowCursorTrailerUndeclared) {
			t.Fatalf("publish error = %v, want undeclared trailer", err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if got := response.Header.Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("cursor leaked as ordinary header %q", got)
		}
		if got := response.Trailer.Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("cursor leaked as trailer %q", got)
		}
	})

	t.Run("empty cursor remains empty", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		if err := declareDashboardNativeArrowCursorTrailer(recorder); err != nil {
			t.Fatal(err)
		}
		recorder.WriteHeader(stdhttp.StatusOK)
		if err := publishDashboardNativeArrowCursor(recorder, ""); err != nil {
			t.Fatal(err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if got := response.Header.Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("empty cursor leaked as ordinary header %q", got)
		}
		if got := response.Trailer.Get(dashboardNativeArrowNextCursorHeader); got != "" {
			t.Fatalf("empty cursor trailer = %q", got)
		}
	})
}

func TestDashboardNativeArrowCompletionRejectsImpossibleProbeCounts(t *testing.T) {
	t.Parallel()
	scope := dashboardNativeArrowTestScope(100)
	plan, _ := planDashboardNativeArrowPage(0, 100)
	for _, observed := range []int{-1, 102} {
		if cursor, err := dashboardNativeArrowCompletionCursor(scope, plan, observed, nil, time.Now()); err == nil || cursor != "" {
			t.Fatalf("observed %d completion = (%q, %v), want error", observed, cursor, err)
		}
	}
}

func dashboardNativeArrowTestScope(limit int) dashboardNativeArrowCursorScope {
	return dashboardNativeArrowContractCursorScope(limit, "scope-a")
}

func dashboardNativeArrowTestDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
