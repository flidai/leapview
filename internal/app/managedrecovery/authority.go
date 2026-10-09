package managedrecovery

import (
	"context"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuthorityInput struct {
	URLFile    string `json:"urlFile"`
	RootCAFile string `json:"rootCaFile"`
	Role       string `json:"role"`
}

// OpenManagedAuthority authenticates one explicit TLS maintenance authority.
// Its durable ledger/set storage must remain available when original writers
// are fenced; the actual PostgreSQL system ID proves that separation.
func OpenManagedAuthority(ctx context.Context, input AuthorityInput, primaries []providerrestore.PrimaryEnrollment) (*pgxpool.Pool, error) {
	if len(primaries) == 0 {
		return nil, errors.New("managed recovery requires enrolled original primaries")
	}
	value, err := readBoundedManagedPrivateFile(input.URLFile, maxManagedCredentialsBytes)
	if err != nil {
		return nil, errors.New("private recovery authority URL unavailable")
	}
	ca, err := readBoundedManagedPrivateFile(input.RootCAFile, maxManagedCredentialsBytes)
	if err != nil {
		return nil, errors.New("private recovery authority TLS root unavailable")
	}
	connection, err := managedConnectionConfig(strings.TrimSpace(string(value)), input.Role, string(ca))
	if err != nil {
		return nil, errors.New("explicit authenticated recovery authority configuration invalid")
	}
	connection.RuntimeParams["default_transaction_read_only"] = "off"
	configuration, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, errors.New("recovery authority pool configuration invalid")
	}
	configuration.ConnConfig = connection
	configuration.MaxConns = 2
	configuration.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, errors.New("recovery authority connection failed")
	}
	// sqlc-exception:managed-recovery-authority -- read the selected server's
	// actual cluster identity before constructing capability-owned repositories.
	var systemID string
	if err := pool.QueryRow(ctx, "SELECT system_identifier::text FROM pg_control_system()").Scan(&systemID); err != nil {
		pool.Close()
		return nil, errors.New("recovery authority cluster identity cannot be verified")
	}
	for _, primary := range primaries {
		if primary.SystemIdentifier == systemID {
			pool.Close()
			return nil, errors.New("recovery authority must remain independent of every original writer")
		}
	}
	return pool, nil
}
