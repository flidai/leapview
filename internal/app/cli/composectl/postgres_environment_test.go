package composectl

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePostgresConnectionRejectsIdentityOverridesAndRepeatedOptions(t *testing.T) {
	base := "postgres://leapview_control_runtime:runtime-secret@db.example/leapview_control?sslmode=verify-full"
	for name, value := range map[string]string{
		"query user":             base + "&user=other",
		"query password":         base + "&password=other",
		"query database":         base + "&dbname=other",
		"query host":             base + "&host=other.example",
		"query hostaddr":         base + "&hostaddr=192.0.2.1",
		"query port":             base + "&port=6432",
		"query service":          base + "&service=alternate",
		"duplicate sslmode":      base + "&sslmode=require",
		"duplicate options":      base + "&options=-csearch_path%3Dpublic&options=-crole%3Dother",
		"case duplicate sslmode": base + "&SSLMode=require",
		"URL newline":            strings.Replace(base, "db.example", "db.example\nLEAPVIEW_X=y", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := validatePostgresConnection(postgresConnection{
				name: "control runtime", value: value,
				role: postgresControlRuntimeRole, database: postgresControlDatabase,
			})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "runtime-secret")
		})
	}
}

func TestCanonicalPostgresConnectionURLEscapesComposeInterpolation(t *testing.T) {
	connection := postgresConnection{
		name:  "control runtime",
		value: "postgres://leapview_control_runtime:runtime$'secret@db.example/leapview_control?sslmode=verify-full&options=-capplication_name%3D%24app",
		role:  postgresControlRuntimeRole, database: postgresControlDatabase,
	}
	canonical, err := canonicalPostgresConnectionURL(connection)
	require.NoError(t, err)
	require.NotContains(t, canonical, "$")
	parsed, err := url.Parse(canonical)
	require.NoError(t, err)
	password, present := parsed.User.Password()
	require.True(t, present)
	require.Equal(t, "runtime$'secret", password)
	query, err := url.ParseQuery(parsed.RawQuery)
	require.NoError(t, err)
	require.Equal(t, "-capplication_name=$app", query.Get("options"))
}

func TestValidatePostgresConnectionAllowsProviderTrustAndSessionOptions(t *testing.T) {
	value := "postgres://leapview_control_runtime:runtime-secret@db.example/leapview_control?sslmode=verify-full&sslrootcert=%2Fetc%2Fssl%2Fprovider-ca.pem&options=-csearch_path%3Dpublic"
	_, _, err := validatePostgresConnection(postgresConnection{
		name: "control runtime", value: value,
		role: postgresControlRuntimeRole, database: postgresControlDatabase,
	})
	require.NoError(t, err)
}
