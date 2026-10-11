package postgrestest

import "testing"

// PhysicalBackupURL enables authenticated replication only on this harness's
// disposable container. Package-shared and caller-supplied servers cannot be
// modified through this test capability. The URL contains private credentials.
func (h *Harness) PhysicalBackupURL(t *testing.T) string {
	t.Helper()
	if h == nil || h.container == nil {
		t.Fatal("physical backup requires a harness-owned disposable server")
	}
	// sqlc-exception:analyzer-incompatible -- this is container execution, not PostgreSQL SQL.
	code, _, err := h.container.Exec(t.Context(), []string{"sh", "-ec", `printf '\nhost replication postgres all scram-sha-256\n' >> "$PGDATA/pg_hba.conf"; read -r postmaster_pid < "$PGDATA/postmaster.pid"; kill -HUP "$postmaster_pid"`})
	if err != nil || code != 0 {
		t.Fatal("configure disposable physical backup authentication")
	}
	return h.adminURL
}
