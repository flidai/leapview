package app

import (
	"strings"
	"testing"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSourceCredentialPublicationIdentityBindsRetryAndGeneration(t *testing.T) {
	operation := "12345678-1234-4234-9234-123456789abc"
	generation := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001001")
	first, err := sourceCredentialPublicationID(operation, generation)
	require.NoError(t, err)
	repeated, err := sourceCredentialPublicationID(operation, generation)
	require.NoError(t, err)
	require.Equal(t, first, repeated)
	parsed, err := uuid.Parse(first)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version(), "publication identity is also the canonical activation event identity")
	require.Equal(t, uuid.RFC4122, parsed.Variant())
	require.Equal(t, generation[:6], parsed[:6], "preserve the server-created generation timestamp")
	otherOperation, err := sourceCredentialPublicationID("12345678-1234-4234-9234-123456789abd", generation)
	require.NoError(t, err)
	require.NotEqual(t, first, otherOperation)
	otherGeneration, err := sourceCredentialPublicationID(operation, uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001002"))
	require.NoError(t, err)
	require.NotEqual(t, first, otherGeneration)
	require.NotEqual(t, generation.String(), first)
}

func TestSourceCredentialPublicationIdentityRejectsInvalidSeeds(t *testing.T) {
	operation := "12345678-1234-4234-9234-123456789abc"
	generation := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001001")
	for _, invalid := range []string{"", "not-a-uuid", uuid.Nil.String(), strings.ToUpper(operation), " " + operation, strings.ReplaceAll(operation, "-", "")} {
		_, err := sourceCredentialPublicationID(invalid, generation)
		require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	}
	for _, invalid := range []uuid.UUID{uuid.Nil, uuid.MustParse(operation), uuid.MustParse("0198f2c0-7c7a-7f00-0a11-000000001001")} {
		_, err := sourceCredentialPublicationID(operation, invalid)
		require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	}
}

func TestSourceCredentialPublicationIdentityAppendsCanonicalActivationEvent(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "source_credential_publication_event")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	_, err = db.Exec(t.Context(), eventspostgres.SchemaSQL())
	require.NoError(t, err)
	publicationID, err := sourceCredentialPublicationID("12345678-1234-4234-9234-123456789abc", uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001001"))
	require.NoError(t, err)
	input := eventspostgres.EventInput{EventID: publicationID, ScopeID: "target:test", AggregateType: "delivery_target", AggregateID: "target:test", EventType: "activation_committed", SchemaVersion: 1, CorrelationID: "12345678-1234-4234-9234-123456789abc", Payload: []byte(`{"generationId":"0198f2c0-7c7a-7f00-8a11-000000001001"}`)}
	repository := eventspostgres.New()
	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(t.Context())
	first, err := repository.AppendEvent(t.Context(), tx, input)
	require.NoError(t, err, "credential publication ID must satisfy the actual canonical event envelope")
	require.NoError(t, tx.Commit(t.Context()))
	tx, err = db.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(t.Context())
	retried, err := repository.AppendEvent(t.Context(), tx, input)
	require.NoError(t, err)
	require.Equal(t, first, retried)
	require.NoError(t, tx.Commit(t.Context()))
	var count int
	require.NoError(t, db.QueryRow(t.Context(), "SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid", publicationID).Scan(&count))
	require.Equal(t, 1, count, "publication retry must preserve one immutable activation event")
}
