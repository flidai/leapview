package postgres

import "github.com/prometheus/client_golang/prometheus"

const (
	credentialClassAPIToken               = "api_token"
	credentialClassServicePrincipalSecret = "service_principal_secret"
)

var credentialTouchFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "leapview_access_credential_last_used_update_failures_total",
	Help: "Best-effort credential last-used updates that failed after authentication succeeded.",
}, []string{"credential_class"})

// CredentialMetricsCollector returns the bounded access-credential metric
// collector for registration with the application's telemetry registry.
func CredentialMetricsCollector() prometheus.Collector { return credentialTouchFailures }

// ObserveCredentialTouchFailure records a best-effort last-used write
// failure. The class is the only label so credential identities never become
// metric cardinality.
func ObserveCredentialTouchFailure(class string) {
	switch class {
	case credentialClassAPIToken, credentialClassServicePrincipalSecret:
		observeCredentialTouchFailure(class)
	}
}

func observeCredentialTouchFailure(class string) {
	credentialTouchFailures.WithLabelValues(class).Inc()
}
