// devcredentials prepares a real local browser login and a separate bounded
// publisher token for the worktree-local PostgreSQL development target.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/app/config"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/cliapi"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func main() {
	out := flag.String("out", ".tmp/dev-auth/credentials.json", "private worktree-local credential bundle")
	rotatePublisher := flag.Bool("rotate-publisher", false, "revoke and replace the current development publisher token")
	flag.Parse()
	if err := run(context.Background(), *out, *rotatePublisher); err != nil {
		fmt.Fprintln(os.Stderr, "prepare development credentials:", err)
		os.Exit(1)
	}
	fmt.Println("Development login and publisher credentials are stored privately at", *out)
}

func run(ctx context.Context, path string, rotatePublisher bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Production || cfg.Environment != "dev" || cfg.DevAuthBypass || !cfg.LocalAuth {
		return errors.New("credential preparation requires non-production dev environment with local auth and without authentication bypass")
	}
	dbConfig := cfg.PostgresControlPlaneConfig().Runtime
	if !isLoopbackDatabase(dbConfig.URL) {
		return errors.New("development credential preparation requires a loopback PostgreSQL target")
	}
	pool, err := platformpostgres.OpenControl(ctx, dbConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	fingerprintKey := []byte(strings.TrimSpace(cfg.TokenHashKey))
	if len(fingerprintKey) < 32 {
		fingerprintKey = []byte(strings.TrimSpace(cfg.CSRFKey))
	}
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: fingerprintKey})
	if err != nil {
		return err
	}
	principalID := accessmodule.DevelopmentPrincipalID
	principal, err := repo.PrincipalByID(ctx, principalID)
	if err != nil {
		return fmt.Errorf("development principal is unavailable; start the dev server for schema migration and principal seeding first: %w", err)
	}
	if principal.Kind != access.PrincipalKindUser || principal.AccessDisabled() {
		return errors.New("development principal is not an active local user")
	}
	admin, err := repo.IsPlatformAdmin(ctx, principalID)
	if err != nil {
		return err
	}
	if !admin {
		return errors.New("development principal lacks its platform-administrator binding")
	}
	instanceID, err := platformbootstrap.New(pool).InstanceID(ctx)
	if err != nil {
		return err
	}
	authority, err := cliapi.NewProfileStore(cfg.ClientConfigPath()).ResolveProjectAuthority("", func(value string) error {
		_, err := projectgraph.NewResourceID(value)
		return err
	})
	if err != nil {
		return err
	}
	bootstrapPermissions, err := developmentBootstrapPermissions(instanceID, projectgraph.ResourceID(authority.ProjectUID))
	if err != nil {
		return err
	}
	publisherPermissions, err := developmentPublisherPermissions(projectgraph.ResourceID(authority.ProjectUID))
	if err != nil {
		return err
	}
	if _, err := repo.LocalCredential(ctx, principalID); errors.Is(err, pgx.ErrNoRows) {
		_, err = repo.ProvisionDevelopmentOperator(ctx, principalID, bootstrapPermissions, publisherPermissions, func(credentials accesspostgres.DevelopmentCredentials) error {
			return writeCredentials(path, credentials)
		})
		return err
	} else if err != nil {
		return err
	}
	encoded, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return fmt.Errorf("existing login cannot be recovered from %s; use an explicit password reset, never silently replace it: %w", path, err)
	}
	var credentials accesspostgres.DevelopmentCredentials
	if err := json.Unmarshal(encoded, &credentials); err != nil || credentials.Email != principal.Email || credentials.Password == "" || credentials.PublisherToken == "" {
		return errors.New("private development credential bundle is incomplete or does not match the principal")
	}
	verified, _, err := repo.VerifyLocalPassword(ctx, credentials.Email, credentials.Password)
	if err != nil || verified.ID != principalID {
		return errors.New("private development login no longer matches the stored credential; reset it explicitly")
	}
	bootstrap, err := repo.CredentialForAPIToken(ctx, credentials.BootstrapToken)
	if err != nil || bootstrap.Principal.ID != principalID || bootstrap.Token.PermissionProfile != access.PermissionCatalogProfile || !samePermissions(bootstrap.Token.Permissions, bootstrapPermissions) || !tokenValidForDay(bootstrap.Token.ExpiresAt) {
		replaceID := ""
		if err == nil && bootstrap.Principal.ID == principalID {
			replaceID = bootstrap.Token.ID
		}
		if err := repo.ProvisionDevelopmentBootstrapToken(ctx, principalID, bootstrapPermissions, replaceID, func(secret string) error {
			credentials.BootstrapToken = secret
			return writeCredentials(path, credentials)
		}); err != nil {
			return err
		}
	}
	current, err := repo.CredentialForAPIToken(ctx, credentials.PublisherToken)
	if !rotatePublisher && err == nil && current.Principal.ID == principalID && current.Token.PermissionProfile == access.PermissionCatalogProfile && tokenValidForDay(current.Token.ExpiresAt) && samePermissions(current.Token.Permissions, publisherPermissions) {
		return nil
	}
	replaceID := ""
	if err == nil && current.Principal.ID == principalID {
		replaceID = current.Token.ID
	}
	return repo.ProvisionDevelopmentPublisherToken(ctx, principalID, publisherPermissions, replaceID, func(secret string) error {
		credentials.PublisherToken = secret
		credentials.PublisherTokenExpiresAt = time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
		return writeCredentials(path, credentials)
	})
}

func developmentBootstrapPermissions(instanceID string, projectID projectgraph.ResourceID) ([]access.PermissionPair, error) {
	instance, err := access.NewInstancePermissionPair(access.ActionPlatformAccessManage, instanceID)
	if err != nil {
		return nil, err
	}
	result := []access.PermissionPair{instance}
	for _, action := range []access.Action{access.ActionProjectAccessRead, access.ActionProjectAccessManage, access.ActionProjectAccessDelegate} {
		pair, err := access.NewProjectPermissionPair(action, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, pair)
	}
	return result, nil
}

func developmentPublisherPermissions(projectID projectgraph.ResourceID) ([]access.PermissionPair, error) {
	result := make([]access.PermissionPair, 0, 5)
	for _, action := range []access.Action{
		access.ActionDeliveryRead, access.ActionDeliveryPlan, access.ActionDeliveryBuild,
		access.ActionDeliveryPublish, access.ActionDeliveryActivate,
	} {
		pair, err := access.NewProjectPermissionPair(action, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, pair)
	}
	return result, nil
}

func samePermissions(got, want []access.PermissionPair) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[access.PermissionPair]struct{}, len(got))
	for _, pair := range got {
		set[pair] = struct{}{}
	}
	for _, pair := range want {
		if _, ok := set[pair]; !ok {
			return false
		}
	}
	return true
}

func tokenValidForDay(raw string) bool {
	expires, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && expires.After(time.Now().Add(24*time.Hour))
}

func isLoopbackDatabase(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func writeCredentials(path string, credentials accesspostgres.DevelopmentCredentials) error {
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, append(encoded, '\n'))
}
