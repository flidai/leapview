package deploymentpostgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	deployment "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reservationDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func reservationRequest() deploymentmodule.NativeDeliveryBuildRequest {
	return deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: projectgraph.ResourceID("project-reservation"), TargetID: "target-reservation",
		Environment: "prod", PlanID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000601"),
		PrincipalID: "principal-reservation", IdempotencyKey: "reservation-1",
	}
}

func TestReserveNativeBuildOperationRenewsFreshLeaseAndReplaysWithoutRenewal(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "native_build_reservation")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	delivery := deploymentnative.New(db)
	operations := deploymentoperation.New(operationpostgres.New(db))
	request := reservationRequest()
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	owner := "0198f2c0-7c7a-7f00-8a11-000000000602"
	input := NativeBuildOperationReservationInput{Request: request, RequestDigest: digest, OwnerID: owner, LeaseDuration: time.Minute}

	first, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, input)
	if err != nil {
		t.Fatalf("fresh reservation: %v", err)
	}
	if first.Disposition != deploymentmodule.NativeOperationAcquired || first.Operation.OperationType != nativeBuildOperationType || first.Operation.OperationID == "" || first.Operation.OwnerID != owner || first.Operation.FencingGeneration <= 0 || first.Lease.OperationID != first.Operation.OperationID || first.Lease.OwnerID != owner || first.Lease.FencingGeneration != first.Operation.FencingGeneration || first.Lease.AttemptID != "" || first.Operation.AttemptID != "" {
		t.Fatalf("fresh reservation = %#v", first)
	}
	if !first.Operation.LeaseExpiresAt.Equal(first.Lease.LeaseExpiresAt) {
		t.Fatalf("operation and renewed lease expiry differ: operation=%v lease=%v", first.Operation.LeaseExpiresAt, first.Lease.LeaseExpiresAt)
	}

	reacquired, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, input)
	if err != nil {
		t.Fatalf("same-owner reacquisition: %v", err)
	}
	if reacquired.Disposition != deploymentmodule.NativeOperationAcquired || reacquired.Operation.OperationID != first.Operation.OperationID || reacquired.Operation.FencingGeneration != first.Operation.FencingGeneration || !reacquired.Lease.LeaseExpiresAt.After(first.Lease.LeaseExpiresAt) {
		t.Fatalf("same-owner reacquisition = %#v, first=%#v", reacquired, first)
	}
	busyInput := input
	busyInput.OwnerID = "0198f2c0-7c7a-7f00-8a11-000000000604"
	busy, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, busyInput)
	if !errors.Is(err, deploymentmodule.ErrNativeOperationBusy) || busy.Disposition != deploymentmodule.NativeOperationBusy {
		t.Fatalf("busy reservation = %#v, %v; want native operation busy", busy, err)
	}

	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.CompleteTx(t.Context(), tx, reacquired.Lease, []byte(`{"status":"sealed"}`)); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	replay, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, input)
	if err != nil {
		t.Fatalf("terminal replay: %v", err)
	}
	if replay.Disposition != deploymentmodule.NativeOperationReplay || replay.Operation.OperationID != first.Operation.OperationID || !nativeBuildLeaseIsZero(replay.Lease) {
		t.Fatalf("terminal replay = %#v", replay)
	}
}

func TestReserveNativeBuildOperationTakesOverOnlyExpiredNoAttempt(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "native_build_reservation_takeover")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	delivery := deploymentnative.New(db)
	operations := deploymentoperation.New(operationpostgres.NewWithConfig(db, 100*time.Millisecond, time.Hour))
	request := reservationRequest()
	request.IdempotencyKey = "takeover-1"
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{
		Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000605", LeaseDuration: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(750 * time.Millisecond)
	taken, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{
		Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000606", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("expired no-attempt takeover: %v", err)
	}
	if taken.Disposition != deploymentmodule.NativeOperationAcquired || taken.Operation.OperationID != first.Operation.OperationID || taken.Operation.FencingGeneration != first.Operation.FencingGeneration+1 || taken.Operation.AttemptID != "" || taken.Lease.AttemptID != "" || taken.Lease.OwnerID != "0198f2c0-7c7a-7f00-8a11-000000000606" {
		t.Fatalf("expired no-attempt takeover = %#v, first=%#v", taken, first)
	}

	attemptRequest := request
	attemptRequest.IdempotencyKey = "takeover-with-attempt"
	attemptDigest, err := nativeBuildRequestDigest(attemptRequest)
	if err != nil {
		t.Fatal(err)
	}
	attemptOwner := "0198f2c0-7c7a-7f00-8a11-000000000607"
	reserved, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{Request: attemptRequest, RequestDigest: attemptDigest, OwnerID: attemptOwner, LeaseDuration: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bound, err := operations.BeginAttemptTx(t.Context(), tx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: reserved.Lease, AttemptIdentity: "external-writer"})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Once the bound external attempt expires, the authority must retain
	// positive lease-expiry evidence and return an indeterminate disposition;
	// it must never hand the operation to a successor or renew an executable
	// lease.
	time.Sleep(750 * time.Millisecond)
	indeterminate, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{Request: attemptRequest, RequestDigest: attemptDigest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000608", LeaseDuration: time.Minute})
	if err != nil {
		t.Fatalf("expired bound-attempt acquisition: %v", err)
	}
	if indeterminate.Disposition != deploymentmodule.NativeOperationIndeterminate || indeterminate.Operation.State != deploymentmodule.NativeOperationStateIndeterminate || indeterminate.Operation.AttemptID != bound.AttemptID || string(indeterminate.Operation.AttemptEvidence) != string(operationpostgres.ExpiredAttemptEvidence) || !nativeBuildLeaseIsZero(indeterminate.Lease) {
		t.Fatalf("expired bound-attempt result = %#v", indeterminate)
	}
}

func TestReserveNativeBuildOperationRejectsDigestOwnerAndLeaseInput(t *testing.T) {
	request := reservationRequest()
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	base := NativeBuildOperationReservationInput{Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000000603", LeaseDuration: time.Minute}
	for name, mutate := range map[string]func(*NativeBuildOperationReservationInput){
		"digest mismatch": func(in *NativeBuildOperationReservationInput) { in.RequestDigest = reservationDigest('f') },
		"owner non-v7":    func(in *NativeBuildOperationReservationInput) { in.OwnerID = "0198f2c0-7c7a-8f00-8a11-000000000603" },
		"zero duration":   func(in *NativeBuildOperationReservationInput) { in.LeaseDuration = 0 },
		"submicro lease":  func(in *NativeBuildOperationReservationInput) { in.LeaseDuration = time.Nanosecond },
		"oversized lease": func(in *NativeBuildOperationReservationInput) { in.LeaseDuration = 24*time.Hour + time.Nanosecond },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := normalizeNativeBuildReservationInput(input); !errors.Is(err, deployment.ErrDeliveryInvalid) && !errors.Is(err, deployment.ErrDeliveryConflict) {
				t.Fatalf("normalization error = %v", err)
			}
		})
	}
	var nilAuthority *deploymentoperation.Adapter
	if !nativeBuildOperationAuthorityIsNil(nilAuthority) {
		t.Fatal("typed-nil operation authority was accepted")
	}
}
