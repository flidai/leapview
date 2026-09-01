package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
)

const (
	dashboardNativeArrowContract         = "native-v1"
	dashboardNativeArrowContractHeader   = "X-LeapView-Arrow-Contract"
	dashboardNativeArrowNextCursorHeader = "X-Next-Cursor"
	dashboardNativeArrowCursorPrefix     = "d3"
	dashboardNativeArrowDefaultLimit     = 100
	dashboardNativeArrowMinimumLimit     = 1
	dashboardNativeArrowMaximumLimit     = 1000
	dashboardNativeArrowRowCap           = 10_000
)

var (
	errDashboardArrowContractNotAcceptable         = errors.New("dashboard Arrow contract is not acceptable")
	errDashboardNativeArrowCursorInvalid           = errors.New("invalid native dashboard page token")
	errDashboardNativeArrowCursorTrailerUndeclared = errors.New("native dashboard cursor trailer was not declared before response commitment")
)

type dashboardArrowContractMode uint8

const (
	dashboardArrowContractInvalid dashboardArrowContractMode = iota
	dashboardArrowContractLegacy
	dashboardArrowContractNativeV1
)

// negotiateDashboardArrowContract defines the version boundary without
// activating native serving. The production dashboard handler remains on the
// legacy path until a later adoption change explicitly wires the native mode.
func negotiateDashboardArrowContract(accept, contract string) (dashboardArrowContractMode, error) {
	contract = strings.TrimSpace(contract)
	if contract == "" {
		return dashboardArrowContractLegacy, nil
	}
	if contract != dashboardNativeArrowContract || !acceptsDashboardNativeArrowMediaType(accept) {
		return dashboardArrowContractInvalid, errDashboardArrowContractNotAcceptable
	}
	return dashboardArrowContractNativeV1, nil
}

func acceptsDashboardNativeArrowMediaType(header string) bool {
	for _, item := range strings.Split(header, ",") {
		mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil || !strings.EqualFold(mediaType, dashboardArrowMediaType) {
			continue
		}
		quality := 1.0
		if value, ok := parameters["q"]; ok {
			quality, err = parseDashboardMediaQuality(value)
			if err != nil {
				continue
			}
		}
		if quality > 0 {
			return true
		}
	}
	return false
}

func parseDashboardMediaQuality(value string) (float64, error) {
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole != "0" && whole != "1" {
		return 0, fmt.Errorf("invalid media quality")
	}
	if hasFraction {
		if len(fraction) > 3 {
			return 0, fmt.Errorf("invalid media quality")
		}
		for _, digit := range fraction {
			if digit < '0' || digit > '9' || whole == "1" && digit != '0' {
				return 0, fmt.Errorf("invalid media quality")
			}
		}
	}
	quality, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid media quality: %w", err)
	}
	return quality, nil
}

func normalizeDashboardNativeArrowLimit(requested *int) (int, error) {
	if requested == nil {
		return dashboardNativeArrowDefaultLimit, nil
	}
	if *requested < dashboardNativeArrowMinimumLimit {
		return 0, fmt.Errorf("limit must be at least %d", dashboardNativeArrowMinimumLimit)
	}
	if *requested > dashboardNativeArrowMaximumLimit {
		return 0, fmt.Errorf("limit must not exceed %d", dashboardNativeArrowMaximumLimit)
	}
	return *requested, nil
}

type dashboardNativeArrowPagePlan struct {
	Offset             int
	RequestedLimit     int
	EmitLimit          int
	QueryLimit         int
	ProbesContinuation bool
}

func planDashboardNativeArrowPage(offset, limit int) (dashboardNativeArrowPagePlan, error) {
	if limit < dashboardNativeArrowMinimumLimit || limit > dashboardNativeArrowMaximumLimit {
		return dashboardNativeArrowPagePlan{}, fmt.Errorf("native dashboard limit must be between %d and %d", dashboardNativeArrowMinimumLimit, dashboardNativeArrowMaximumLimit)
	}
	if offset < 0 || offset > dashboardNativeArrowRowCap {
		return dashboardNativeArrowPagePlan{}, fmt.Errorf("native dashboard offset exceeds the cumulative row cap")
	}
	remaining := dashboardNativeArrowRowCap - offset
	emitLimit := min(limit, remaining)
	queryLimit := emitLimit
	probe := emitLimit > 0 && remaining > emitLimit
	if probe {
		queryLimit++
	}
	return dashboardNativeArrowPagePlan{
		Offset: offset, RequestedLimit: limit, EmitLimit: emitLimit,
		QueryLimit: queryLimit, ProbesContinuation: probe,
	}, nil
}

// dashboardNativeArrowCursorScope contains only server-computed request and
// governance identities. Raw filter, selection, sort, policy, and principal
// values must never be passed to or serialized by this cursor domain.
type dashboardNativeArrowCursorScope struct {
	DashboardID                string
	PageID                     string
	VisualID                   string
	NormalizedFiltersDigest    string
	NormalizedSelectionsDigest string
	EffectiveSortingDigest     string
	RequestedLimit             int
	EffectivePolicyIdentity    string
	ServingSnapshot            string
}

type dashboardNativeArrowCursor struct {
	Contract        string `json:"contract"`
	Scope           string `json:"scope"`
	ServingSnapshot string `json:"servingSnapshot"`
	RequestedLimit  int    `json:"requestedLimit"`
	NextOffset      int    `json:"nextOffset"`
	RowsConsumed    int    `json:"rowsConsumed"`
	RowCap          int    `json:"rowCap"`
	Expires         int64  `json:"expires"`
}

type dashboardNativeArrowCursorState struct {
	NextOffset   int
	RowsConsumed int
	RowCap       int
}

type dashboardNativeArrowCursorScopeIdentity struct {
	Contract                   string `json:"contract"`
	DashboardID                string `json:"dashboard"`
	PageID                     string `json:"page"`
	VisualID                   string `json:"visual"`
	NormalizedFiltersDigest    string `json:"filters"`
	NormalizedSelectionsDigest string `json:"selections"`
	EffectiveSortingDigest     string `json:"sorting"`
	RequestedLimit             int    `json:"limit"`
	EffectivePolicyIdentity    string `json:"policy"`
}

func (scope dashboardNativeArrowCursorScope) digest() (string, error) {
	if strings.TrimSpace(scope.DashboardID) == "" || strings.TrimSpace(scope.PageID) == "" || strings.TrimSpace(scope.VisualID) == "" {
		return "", fmt.Errorf("native dashboard cursor identity is incomplete")
	}
	if strings.TrimSpace(scope.ServingSnapshot) == "" {
		return "", fmt.Errorf("native dashboard cursor serving snapshot is unavailable")
	}
	if scope.RequestedLimit < dashboardNativeArrowMinimumLimit || scope.RequestedLimit > dashboardNativeArrowMaximumLimit {
		return "", fmt.Errorf("native dashboard cursor limit is invalid")
	}
	identities := []struct {
		name  string
		value string
	}{
		{name: "normalized filters", value: scope.NormalizedFiltersDigest},
		{name: "normalized selections", value: scope.NormalizedSelectionsDigest},
		{name: "effective sorting", value: scope.EffectiveSortingDigest},
		{name: "effective policy", value: scope.EffectivePolicyIdentity},
	}
	for _, identity := range identities {
		if err := platformdigest.ValidateSHA256Identity(identity.value); err != nil {
			return "", fmt.Errorf("%s identity: %w", identity.name, err)
		}
	}
	payload, err := json.Marshal(dashboardNativeArrowCursorScopeIdentity{
		Contract:    dashboardNativeArrowContract,
		DashboardID: scope.DashboardID, PageID: scope.PageID, VisualID: scope.VisualID,
		NormalizedFiltersDigest:    scope.NormalizedFiltersDigest,
		NormalizedSelectionsDigest: scope.NormalizedSelectionsDigest,
		EffectiveSortingDigest:     scope.EffectiveSortingDigest,
		RequestedLimit:             scope.RequestedLimit,
		EffectivePolicyIdentity:    scope.EffectivePolicyIdentity,
	})
	if err != nil {
		return "", fmt.Errorf("encode native dashboard cursor scope: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func dashboardNativeArrowCompletionCursor(scope dashboardNativeArrowCursorScope, plan dashboardNativeArrowPagePlan, observedRows int, streamErr error, now time.Time) (string, error) {
	if streamErr != nil {
		return "", streamErr
	}
	wantPlan, err := planDashboardNativeArrowPage(plan.Offset, plan.RequestedLimit)
	if err != nil || wantPlan != plan {
		return "", fmt.Errorf("invalid native dashboard pagination plan")
	}
	if scope.RequestedLimit != plan.RequestedLimit {
		return "", fmt.Errorf("native dashboard cursor limit does not match pagination plan")
	}
	if observedRows < 0 || observedRows > plan.QueryLimit {
		return "", fmt.Errorf("native dashboard probe observed an invalid row count")
	}
	if !plan.ProbesContinuation || observedRows <= plan.EmitLimit {
		return "", nil
	}
	if observedRows != plan.EmitLimit+1 {
		return "", fmt.Errorf("native dashboard continuation probe is invalid")
	}
	nextOffset := plan.Offset + plan.EmitLimit
	if nextOffset >= dashboardNativeArrowRowCap {
		return "", nil
	}
	scopeDigest, err := scope.digest()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(dashboardNativeArrowCursor{
		Contract:        dashboardNativeArrowContract,
		Scope:           scopeDigest,
		ServingSnapshot: scope.ServingSnapshot,
		RequestedLimit:  scope.RequestedLimit,
		NextOffset:      nextOffset,
		RowsConsumed:    nextOffset,
		RowCap:          dashboardNativeArrowRowCap,
		Expires:         now.Add(dashboardCursorLifetime).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode native dashboard cursor: %w", err)
	}
	return cursorsigning.Sign(dashboardNativeArrowCursorPrefix, payload), nil
}

func decodeDashboardNativeArrowCursor(token string, scope dashboardNativeArrowCursorScope, now time.Time) (dashboardNativeArrowCursorState, error) {
	if token == "" {
		return dashboardNativeArrowCursorState{RowCap: dashboardNativeArrowRowCap}, nil
	}
	if !strings.HasPrefix(token, dashboardNativeArrowCursorPrefix+".") {
		return dashboardNativeArrowCursorState{}, errDashboardNativeArrowCursorInvalid
	}
	payload, err := cursorsigning.Verify(dashboardNativeArrowCursorPrefix, token)
	if err != nil {
		return dashboardNativeArrowCursorState{}, errDashboardNativeArrowCursorInvalid
	}
	var cursor dashboardNativeArrowCursor
	if json.Unmarshal(payload, &cursor) != nil ||
		cursor.Contract != dashboardNativeArrowContract ||
		cursor.RequestedLimit < dashboardNativeArrowMinimumLimit || cursor.RequestedLimit > dashboardNativeArrowMaximumLimit ||
		cursor.NextOffset <= 0 || cursor.NextOffset >= dashboardNativeArrowRowCap ||
		cursor.RowsConsumed != cursor.NextOffset || cursor.RowCap != dashboardNativeArrowRowCap ||
		cursor.Expires <= now.Unix() {
		return dashboardNativeArrowCursorState{}, errDashboardNativeArrowCursorInvalid
	}
	if cursor.ServingSnapshot != scope.ServingSnapshot {
		return dashboardNativeArrowCursorState{}, errDashboardCursorSnapshot
	}
	scopeDigest, err := scope.digest()
	if err != nil || cursor.Scope != scopeDigest || cursor.RequestedLimit != scope.RequestedLimit {
		return dashboardNativeArrowCursorState{}, errDashboardNativeArrowCursorInvalid
	}
	return dashboardNativeArrowCursorState{
		NextOffset: cursor.NextOffset, RowsConsumed: cursor.RowsConsumed, RowCap: cursor.RowCap,
	}, nil
}

// declareDashboardNativeArrowCursorTrailer must run before response commitment.
// Keeping declaration separate from publication lets pre-commit failures retain
// their structured problem response while reserving the completion-only trailer.
func declareDashboardNativeArrowCursorTrailer(w stdhttp.ResponseWriter) error {
	if w == nil {
		return errDashboardNativeArrowCursorTrailerUndeclared
	}
	header := w.Header()
	if header.Get(dashboardNativeArrowNextCursorHeader) != "" {
		return fmt.Errorf("native dashboard cursor was set before response commitment")
	}
	if !dashboardNativeArrowCursorTrailerDeclared(header) {
		header.Add("Trailer", dashboardNativeArrowNextCursorHeader)
	}
	return nil
}

// publishDashboardNativeArrowCursor is deliberately separate from cursor
// derivation and trailer declaration. Callers invoke it only after the IPC
// writer closes cleanly. A missing pre-commit declaration fails without
// setting X-Next-Cursor as an ordinary response header.
func publishDashboardNativeArrowCursor(w stdhttp.ResponseWriter, cursor string) error {
	if w == nil || !dashboardNativeArrowCursorTrailerDeclared(w.Header()) {
		return errDashboardNativeArrowCursorTrailerUndeclared
	}
	if cursor != "" {
		w.Header().Set(dashboardNativeArrowNextCursorHeader, cursor)
	}
	return nil
}

func dashboardNativeArrowCursorTrailerDeclared(header stdhttp.Header) bool {
	for _, value := range header.Values("Trailer") {
		for _, name := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(name), dashboardNativeArrowNextCursorHeader) {
				return true
			}
		}
	}
	return false
}
